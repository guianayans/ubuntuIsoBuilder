package main

// "Próximo boot pelo pendrive" (UEFI): em vez de depender de a placa-mãe ter
// criado sozinha uma entrada "UEFI: <pendrive>", o isoBuilder cria a PRÓPRIA
// entrada de boot apontando para a partição exata do pendrive que ele gravou
// (GUID da partição na GPT ou assinatura do disco na MBR) e marca BootNext, que a
// placa usa UMA vez só; depois a ordem de boot volta a ser a de sempre.
//
// Também é o modo "--boot-usb" usado pelo botão "Formatar esta máquina" do painel:
// fica esperando alguém conectar o pendrive, avisa o painel e reinicia por ele.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	efiGlobalGUID  = "8be4df61-93ca-11d2-aa0d-00e098032b8c"
	bootEntryDesc  = "isoBuilder - Instalar"
	bootEntryFirst = 0x1B00 // faixa própria (Boot1B00..Boot1B0F), longe das entradas da placa
	bootEntryLast  = 0x1B0F
)

// tempos do modo --boot-usb (os testes encurtam)
var (
	bootPollEvery     = 3 * time.Second
	bootBeatEvery     = 15 * time.Second
	bootCountdownStep = 5 * time.Second
	bootCountdownN    = 6 // 6 x 5 s = 30 s para o técnico ainda poder cancelar
)

var (
	gptTypeESP = mustGUID("C12A7328-F81F-11D2-BA4B-00A0C93EC93B")
	errNoUEFI  = errors.New("este computador liga pelo modo BIOS antigo (Legacy/CSM): não dá para escolher o próximo boot por software")
)

// usbTarget: a partição de boot (EFI) do pendrive gravado pelo isoBuilder.
type usbTarget struct {
	Disk    Disk
	Stick   StickInfo
	PartNum uint32
	Start   uint64 // setores de 512 bytes
	Size    uint64
	MBR     bool     // pendrive do Windows (tabela MBR); senão GPT (Ubuntu)
	Sig     [16]byte // GUID da partição (GPT) ou assinatura do disco nos 4 primeiros bytes (MBR)
}

func (t *usbTarget) String() string {
	s := t.Stick.Hostname
	if t.Stick.OS == "windows" {
		s += " (Windows 11)"
	} else {
		s += " (Ubuntu Server)"
	}
	return fmt.Sprintf("pendrive de %s em %s", s, strings.TrimSpace(t.Disk.Name+" "+human(t.Disk.Size)))
}

func efiLoader() string {
	if runtime.GOARCH == "arm64" {
		return `\EFI\BOOT\BOOTAA64.EFI`
	}
	return `\EFI\BOOT\BOOTX64.EFI`
}

func readStickInfo(v *fatVol, p string) (*StickInfo, error) {
	b, err := v.readFile(p, 64<<10)
	if err != nil {
		return nil, err
	}
	var s StickInfo
	if json.Unmarshal(b, &s) != nil || s.ISOName == "" {
		return nil, errors.New("marca do isoBuilder inválida")
	}
	return &s, nil
}

// inspectStick reconhece o pendrive do isoBuilder e devolve a partição de boot.
//   Ubuntu:  GPT da ISO; boot = partição "EFI System"; marca na partição INSTALAR.
//   Windows: MBR com uma partição FAT32; marca em INSTALAR/uab-stick.json.
func inspectStick(dev io.ReaderAt) (*usbTarget, error) {
	r := alignedReader{dev}
	le := binary.LittleEndian
	if g, ents, err := readGPT(r); err == nil {
		esp, tools := -1, -1
		for i := 0; i < int(g.numEntries); i++ {
			e := ents[i*128 : (i+1)*128]
			var t [16]byte
			copy(t[:], e[0:16])
			if t == ([16]byte{}) {
				continue
			}
			if t == gptTypeESP && esp < 0 {
				esp = i
			}
			var name []uint16
			for j := 56; j+1 < 128; j += 2 {
				c := le.Uint16(e[j:])
				if c == 0 {
					break
				}
				name = append(name, c)
			}
			if string(utf16.Decode(name)) == toolsLabel {
				tools = i
			}
		}
		if esp < 0 || tools < 0 {
			return nil, errors.New("não é um pendrive do isoBuilder")
		}
		v, err := openFAT32(r, int64(le.Uint64(ents[tools*128+32:]))*gptSector)
		if err != nil {
			return nil, err
		}
		si, err := readStickInfo(v, stickMarker)
		if err != nil {
			return nil, err
		}
		e := ents[esp*128 : (esp+1)*128]
		t := &usbTarget{Stick: *si, PartNum: uint32(esp + 1), Start: le.Uint64(e[32:])}
		t.Size = le.Uint64(e[40:]) - t.Start + 1
		copy(t.Sig[:], e[16:32])
		return t, nil
	}
	mbr := make([]byte, 512)
	if _, err := r.ReadAt(mbr, 0); err != nil {
		return nil, err
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil, errors.New("pendrive sem tabela de partições")
	}
	for i := 0; i < 4; i++ {
		p := mbr[446+i*16 : 446+(i+1)*16]
		if p[4] != 0x0C && p[4] != 0x0B {
			continue
		}
		start, size := uint64(le.Uint32(p[8:])), uint64(le.Uint32(p[12:]))
		v, err := openFAT32(r, int64(start)*512)
		if err != nil {
			continue
		}
		si, err := readStickInfo(v, toolsLabel+"/"+stickMarker)
		if err != nil {
			continue
		}
		if _, err := v.readFile(efiLoader(), 64<<20); err != nil {
			return nil, errors.New("o pendrive não tem o carregador UEFI deste computador")
		}
		t := &usbTarget{Stick: *si, PartNum: uint32(i + 1), Start: start, Size: size, MBR: true}
		copy(t.Sig[:4], mbr[440:444])
		return t, nil
	}
	return nil, errors.New("não é um pendrive do isoBuilder")
}

// findInstallStick procura, entre os pendrives conectados, um gravado pelo isoBuilder.
func findInstallStick() (*usbTarget, error) {
	disks, err := listDisks()
	if err != nil {
		return nil, err
	}
	for _, d := range disks {
		f, err := os.Open(d.Path)
		if err != nil {
			continue
		}
		t, err := inspectStick(f)
		f.Close()
		if err == nil {
			t.Disk = d
			return t, nil
		}
	}
	return nil, nil
}

// efiLoadOption monta o EFI_LOAD_OPTION (especificação UEFI 3.1.3): atributos,
// descrição e o caminho HD(partição) + arquivo do carregador.
func efiLoadOption(desc string, t *usbTarget, loader string) []byte {
	return efiLoadOptionData(desc, t, loader, nil)
}

// efiLoadOptionData: igual, com OptionalData no fim (o firmware entrega ao carregador como
// LoadOptions). Usado pelo EFISTUB do Ubuntu para receber a linha de comando do kernel.
func efiLoadOptionData(desc string, t *usbTarget, loader string, optional []byte) []byte {
	le := binary.LittleEndian
	hd := make([]byte, 42)
	hd[0], hd[1] = 0x04, 0x01 // Media Device Path / Hard Drive
	le.PutUint16(hd[2:], 42)
	le.PutUint32(hd[4:], t.PartNum)
	le.PutUint64(hd[8:], t.Start)
	le.PutUint64(hd[16:], t.Size)
	copy(hd[24:40], t.Sig[:])
	if t.MBR {
		hd[40], hd[41] = 0x01, 0x01
	} else {
		hd[40], hd[41] = 0x02, 0x02
	}
	u := utf16.Encode([]rune(loader + "\x00"))
	fp := make([]byte, 4+2*len(u))
	fp[0], fp[1] = 0x04, 0x04 // Media Device Path / File Path
	le.PutUint16(fp[2:], uint16(len(fp)))
	for i, c := range u {
		le.PutUint16(fp[4+2*i:], c)
	}
	list := append(append(hd, fp...), 0x7F, 0xFF, 0x04, 0x00) // fim do caminho
	var b bytes.Buffer
	_ = binary.Write(&b, le, uint32(1)) // LOAD_OPTION_ACTIVE
	_ = binary.Write(&b, le, uint16(len(list)))
	for _, c := range utf16.Encode([]rune(desc + "\x00")) {
		_ = binary.Write(&b, le, c)
	}
	b.Write(list)
	b.Write(optional)
	return b.Bytes()
}

// loadOptionDesc lê a descrição de um EFI_LOAD_OPTION ("" se inválido).
func loadOptionDesc(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	var u []uint16
	for i := 6; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			return string(utf16.Decode(u))
		}
		u = append(u, c)
	}
	return ""
}

// armBootNext grava a entrada do pendrive e manda a placa ligar por ela no próximo boot.
func armBootNext(t *usbTarget) (string, error) {
	if !efiSupported() {
		return "", errNoUEFI
	}
	num := -1
	for n := bootEntryFirst; n <= bootEntryLast; n++ { // reaproveita a nossa, senão a 1ª livre
		cur, err := efiGet(fmt.Sprintf("Boot%04X", n))
		if err == nil && loadOptionDesc(cur) == bootEntryDesc {
			num = n
			break
		}
		if err != nil && num < 0 {
			num = n
		}
	}
	if num < 0 {
		return "", errors.New("sem número livre para a entrada de boot")
	}
	name := fmt.Sprintf("Boot%04X", num)
	if err := efiSet(name, efiLoadOption(bootEntryDesc, t, efiLoader())); err != nil {
		return "", fmt.Errorf("a placa-mãe não aceitou a entrada de boot: %w", err)
	}
	next := []byte{byte(num), byte(num >> 8)}
	if err := efiSet("BootNext", next); err != nil {
		return "", fmt.Errorf("a placa-mãe não aceitou o próximo boot: %w", err)
	}
	if got, err := efiGet("BootNext"); err != nil || !bytes.Equal(got, next) {
		return "", errors.New("a placa-mãe não guardou o próximo boot")
	}
	return name, nil
}

// armBootNextLO grava um EFI_LOAD_OPTION já montado (ex.: EFISTUB do Ubuntu, com cmdline) e
// marca BootNext. Mesma faixa/reaproveitamento por descrição do armBootNext.
func armBootNextLO(desc string, lo []byte) (string, error) {
	if !efiSupported() {
		return "", errNoUEFI
	}
	num := -1
	for n := bootEntryFirst; n <= bootEntryLast; n++ {
		cur, err := efiGet(fmt.Sprintf("Boot%04X", n))
		if err == nil && loadOptionDesc(cur) == desc {
			num = n
			break
		}
		if err != nil && num < 0 {
			num = n
		}
	}
	if num < 0 {
		return "", errors.New("sem número livre para a entrada de boot")
	}
	name := fmt.Sprintf("Boot%04X", num)
	if err := efiSet(name, lo); err != nil {
		return "", fmt.Errorf("a placa-mãe não aceitou a entrada de boot: %w", err)
	}
	next := []byte{byte(num), byte(num >> 8)}
	if err := efiSet("BootNext", next); err != nil {
		return "", fmt.Errorf("a placa-mãe não aceitou o próximo boot: %w", err)
	}
	if got, err := efiGet("BootNext"); err != nil || !bytes.Equal(got, next) {
		return "", errors.New("a placa-mãe não guardou o próximo boot")
	}
	return name, nil
}

// ---------------------------------------------------------------- modo --boot-usb

type formatReporter struct {
	url string
	c   *http.Client
}

// send avisa o painel (se houver endereço) e devolve true se o técnico cancelou.
func (r *formatReporter) send(state, msg string, t *usbTarget) bool {
	body := map[string]string{"state": state, "message": msg}
	if t != nil {
		body["stick"] = t.String()
	}
	return r.post(state, msg, body)
}

// sendProgress avisa o painel com uma porcentagem (0-100) durante a preparação.
func (r *formatReporter) sendProgress(state, msg string, percent int) bool {
	body := map[string]string{"state": state, "message": msg}
	if percent >= 0 {
		body["percent"] = strconv.Itoa(percent)
	}
	return r.post(state, msg, body)
}

func (r *formatReporter) post(state, msg string, body map[string]string) bool {
	fmt.Printf("[%s] %s %s\n", time.Now().Format("15:04:05"), state, msg)
	if r.url == "" {
		return false
	}
	body["os"] = osDescription()
	if h, err := os.Hostname(); err == nil {
		body["hostname"] = h
	}
	raw, _ := json.Marshal(body)
	for try := 0; try < 3; try++ {
		resp, err := r.c.Post(r.url, "application/json", bytes.NewReader(raw))
		if err == nil {
			var out struct {
				Cancel bool `json:"cancel"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out)
			resp.Body.Close()
			if resp.StatusCode == http.StatusGone { // pedido apagado no painel
				return true
			}
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return out.Cancel
			}
		}
		time.Sleep(time.Duration(2+try*3) * time.Second)
	}
	return false
}

func validReportURL(u string) bool {
	if u == "" {
		return true
	}
	return strings.HasPrefix(u, "https://") ||
		os.Getenv("UAB_BUNDLE_INSECURE") == "1" && strings.HasPrefix(u, "http://127.0.0.1:")
}

// runBootUSB espera o pendrive (até wait), avisa o painel e reinicia por ele.
// Códigos de saída: 0 reiniciando, 1 erro, 3 BIOS antiga, 4 tempo esgotado, 5 cancelado.
func runBootUSB(wait time.Duration, reportURL string) int {
	rep := &formatReporter{url: reportURL, c: &http.Client{Timeout: 20 * time.Second}}
	defer osBootUSBCleanup()
	if !efiSupported() {
		rep.send("bios", "Este PC liga pelo modo BIOS antigo: o próximo boot não pode ser escolhido à distância. "+
			"Com o pendrive conectado, alguém precisa reiniciar e escolher o pendrive no menu de boot (F12, F11, F8 ou Esc).", nil)
		return 3
	}
	deadline := time.Now().Add(wait)
	var beat time.Time
	for {
		t, _ := findInstallStick()
		if t != nil {
			total := time.Duration(bootCountdownN) * bootCountdownStep
			for i := 0; i <= bootCountdownN; i++ { // o técnico ainda pode cancelar
				if i > 0 {
					time.Sleep(bootCountdownStep)
				}
				left := (total - time.Duration(i)*bootCountdownStep).Round(time.Second)
				if rep.send("encontrado", fmt.Sprintf("Pendrive reconhecido: %s. Reiniciando em %d segundos.", t, int(left.Seconds())), t) {
					rep.send("cancelado", "Cancelado pelo painel.", t)
					return 5
				}
			}
			if t2, _ := findInstallStick(); t2 == nil {
				t = nil // tiraram o pendrive: volta a esperar
				continue
			}
			name, err := armBootNext(t)
			if err != nil {
				rep.send("erro", err.Error()+". Alguém no local pode reiniciar e escolher o pendrive no menu de boot (F12, F11, F8 ou Esc).", t)
				return 1
			}
			osBeforeInstallReboot()
			rep.send("reiniciando", "Reiniciando pelo pendrive (entrada "+name+"). A instalação começa sozinha.", t)
			if os.Getenv("UAB_FAKE_DISK") != "" {
				return 0 // teste: nunca reinicia a máquina de verdade
			}
			if err := osRebootNow(); err != nil {
				rep.send("erro", "não consegui reiniciar: "+err.Error(), t)
				return 1
			}
			return 0
		}
		if time.Now().After(deadline) {
			rep.send("expirou", "Ninguém conectou o pendrive a tempo.", nil)
			return 4
		}
		if time.Since(beat) > bootBeatEvery {
			beat = time.Now()
			if rep.send("aguardando", "Esperando alguém conectar o pendrive do isoBuilder neste PC.", nil) {
				rep.send("cancelado", "Cancelado pelo painel.", nil)
				return 5
			}
		}
		time.Sleep(bootPollEvery)
	}
}

// ---------------------------------------------------------------- variáveis UEFI de teste
// UAB_FAKE_EFIVARS=<pasta>: as variáveis viram arquivos no formato do efivarfs do
// Linux (4 bytes de atributos + dados). Só para testes; nunca toca a placa-mãe.

func fakeEFIDir() string { return os.Getenv("UAB_FAKE_EFIVARS") }

func fakeEFIGet(name string) ([]byte, error) {
	b, err := os.ReadFile(fakeEFIDir() + "/" + name + "-" + efiGlobalGUID)
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, errors.New("variável inválida")
	}
	return b[4:], nil
}

func fakeEFISet(name string, data []byte) error {
	return os.WriteFile(fakeEFIDir()+"/"+name+"-"+efiGlobalGUID, append([]byte{7, 0, 0, 0}, data...), 0o644)
}
