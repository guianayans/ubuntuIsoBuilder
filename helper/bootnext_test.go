package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

var testStickJSON = []byte(`{"hostname":"srv-teste","iso_name":"srv-teste-autoinstall.iso","created":"2026-09-29T10:00:00Z"}`)

func TestFATReadBack(t *testing.T) {
	files := map[string][]byte{
		"uab-stick.json":                   testStickJSON,
		"Instalar-no-Windows.exe":          bytes.Repeat([]byte("MZ"), 3000),
		"EFI/BOOT/BOOTX64.EFI":             []byte("carregador"),
		"pasta com nome longo/arquivo.txt": []byte("olá"),
	}
	img, err := buildFAT32(toolsPartBytes, toolsLabel, files, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v, err := openFAT32(alignedReader{bytes.NewReader(img)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range files {
		got, err := v.readFile(strings.ToUpper(p), 1<<20) // sem diferenciar maiúsculas
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v (%d bytes)", p, err, len(got))
		}
	}
	if _, err := v.readFile("nao-existe.txt", 1<<20); err == nil {
		t.Fatal("arquivo inexistente encontrado")
	}
	if _, err := v.readFile("Instalar-no-Windows.exe", 100); err == nil {
		t.Fatal("limite de tamanho ignorado")
	}
}

// gptImage cria um "pendrive" com GPT e uma partição EFI (como a ISO do Ubuntu).
func gptImage(t *testing.T, size int64) (string, [16]byte) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stick.img")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	secs := uint64(size / 512)
	ents := make([]byte, 128*128)
	e := ents[0:128]
	copy(e[0:16], gptTypeESP[:])
	uniq := mustGUID("4638F312-6BD4-4428-9DAE-150B8B6885AA")
	copy(e[16:32], uniq[:])
	le.PutUint64(e[32:], 2048)
	le.PutUint64(e[40:], 2048+10160-1)
	for i, c := range utf16.Encode([]rune("Appended2")) {
		le.PutUint16(e[56+i*2:], c)
	}
	h := make([]byte, 512)
	copy(h, "EFI PART")
	le.PutUint32(h[8:], 0x00010000)
	le.PutUint32(h[12:], 92)
	le.PutUint64(h[24:], 1)
	le.PutUint64(h[32:], secs-1)
	le.PutUint64(h[40:], 34)
	le.PutUint64(h[48:], secs-34)
	le.PutUint64(h[72:], 2)
	le.PutUint32(h[80:], 128)
	le.PutUint32(h[84:], 128)
	le.PutUint32(h[88:], crc32.ChecksumIEEE(ents))
	le.PutUint32(h[16:], crc32.ChecksumIEEE(h[:92]))
	mbr := make([]byte, 512)
	mbr[446+4] = 0xEE
	le.PutUint32(mbr[446+8:], 1)
	le.PutUint32(mbr[446+12:], uint32(secs-1))
	mbr[510], mbr[511] = 0x55, 0xAA
	for off, b := range map[int64][]byte{0: mbr, 512: h, 1024: ents} {
		if _, err := f.WriteAt(b, off); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := addToolsPartition(f, size, map[string][]byte{stickMarker: testStickJSON}); err != nil {
		t.Fatal(err)
	}
	return p, uniq
}

// mbrImage cria um pendrive do Windows: MBR + uma partição FAT32 com INSTALAR/.
func mbrImage(t *testing.T, withLoader bool) string {
	t.Helper()
	files := map[string][]byte{toolsLabel + "/" + stickMarker: []byte(`{"hostname":"pc-win","iso_name":"Win11.iso","os":"windows"}`)}
	if withLoader {
		files["efi/boot/bootx64.efi"] = []byte("bootmgr")
	}
	img, err := buildFAT32(toolsPartBytes, "WIN11", files, 2048, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "win.img")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	mbr := make([]byte, 512)
	copy(mbr[440:444], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	pe := mbr[446:462]
	pe[0], pe[4] = 0x80, 0x0C
	binary.LittleEndian.PutUint32(pe[8:], 2048)
	binary.LittleEndian.PutUint32(pe[12:], uint32(len(img)/512))
	mbr[510], mbr[511] = 0x55, 0xAA
	if _, err := f.WriteAt(mbr, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(img, 2048*512); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspectStickUbuntuGPT(t *testing.T) {
	p, uniq := gptImage(t, 200<<20)
	f, _ := os.Open(p)
	defer f.Close()
	tg, err := inspectStick(f)
	if err != nil {
		t.Fatal(err)
	}
	if tg.MBR || tg.PartNum != 1 || tg.Start != 2048 || tg.Size != 10160 || tg.Sig != uniq || tg.Stick.Hostname != "srv-teste" {
		t.Fatalf("alvo errado: %+v", tg)
	}
}

func TestInspectStickWindowsMBR(t *testing.T) {
	f, _ := os.Open(mbrImage(t, true))
	defer f.Close()
	tg, err := inspectStick(f)
	if err != nil {
		t.Fatal(err)
	}
	if !tg.MBR || tg.PartNum != 1 || tg.Start != 2048 || !bytes.Equal(tg.Sig[:4], []byte{0xDE, 0xAD, 0xBE, 0xEF}) || tg.Stick.OS != "windows" {
		t.Fatalf("alvo errado: %+v", tg)
	}
	f2, _ := os.Open(mbrImage(t, false))
	defer f2.Close()
	if _, err := inspectStick(f2); err == nil {
		t.Fatal("pendrive sem carregador UEFI aceito")
	}
}

func TestInspectStickRejectsOthers(t *testing.T) {
	// FAT32 comum (sem a marca do isoBuilder) e disco vazio
	img, _ := buildFAT32(toolsPartBytes, "DADOS", map[string][]byte{"foto.jpg": []byte("x")}, 0, time.Now())
	if _, err := inspectStick(bytes.NewReader(img)); err == nil {
		t.Fatal("pendrive qualquer aceito")
	}
	if _, err := inspectStick(bytes.NewReader(make([]byte, 1<<20))); err == nil {
		t.Fatal("disco vazio aceito")
	}
}

func TestEFILoadOption(t *testing.T) {
	tg := &usbTarget{PartNum: 2, Start: 7958876, Size: 10160, Sig: mustGUID("4638F312-6BD4-4428-9DAE-150B8B6885AA")}
	b := efiLoadOption(bootEntryDesc, tg, `\EFI\BOOT\BOOTX64.EFI`)
	le := binary.LittleEndian
	if le.Uint32(b) != 1 || loadOptionDesc(b) != bootEntryDesc {
		t.Fatalf("cabeçalho: %x", b[:16])
	}
	fpl := int(le.Uint16(b[4:]))
	dp := b[6+2*(len(bootEntryDesc)+1):]
	if len(dp) != fpl {
		t.Fatalf("tamanho do caminho %d, esperado %d", len(dp), fpl)
	}
	// HD(2,GPT,<guid>,0x79715c,0x27b0)
	if dp[0] != 4 || dp[1] != 1 || le.Uint16(dp[2:]) != 42 || le.Uint32(dp[4:]) != 2 || le.Uint64(dp[8:]) != 7958876 ||
		le.Uint64(dp[16:]) != 10160 || dp[40] != 2 || dp[41] != 2 || !bytes.Equal(dp[24:40], tg.Sig[:]) {
		t.Fatalf("HD(): %x", dp[:42])
	}
	fp := dp[42:]
	if fp[0] != 4 || fp[1] != 4 || int(le.Uint16(fp[2:])) != 4+2*22 {
		t.Fatalf("File(): %x", fp[:4])
	}
	if !bytes.Equal(dp[len(dp)-4:], []byte{0x7F, 0xFF, 4, 0}) {
		t.Fatal("sem o fim do caminho")
	}
	tg.MBR = true
	if b := efiLoadOption("x", tg, `\a`); b[6+4+40] != 1 || b[6+4+41] != 1 {
		t.Fatal("MBR: tipo de assinatura errado")
	}
}

func TestArmBootNextFake(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UAB_FAKE_EFIVARS", dir)
	// Boot1B00 já usada por outra coisa: precisa ficar intacta
	other := efiLoadOption("Outro sistema", &usbTarget{PartNum: 1}, `\x.efi`)
	if err := fakeEFISet("Boot1B00", other); err != nil {
		t.Fatal(err)
	}
	tg := &usbTarget{PartNum: 1, Start: 2048, Size: 100}
	name, err := armBootNext(tg)
	if err != nil || name != "Boot1B01" {
		t.Fatalf("%s %v", name, err)
	}
	if got, _ := fakeEFIGet("BootNext"); !bytes.Equal(got, []byte{0x01, 0x1B}) {
		t.Fatalf("BootNext = %x", got)
	}
	if got, _ := fakeEFIGet("Boot1B00"); !bytes.Equal(got, other) {
		t.Fatal("entrada de outro sistema alterada")
	}
	if name, _ := armBootNext(tg); name != "Boot1B01" { // 2ª vez reaproveita a nossa
		t.Fatalf("não reaproveitou: %s", name)
	}
}

func TestRunBootUSBFlow(t *testing.T) {
	bootPollEvery, bootBeatEvery, bootCountdownStep, bootCountdownN = 50*time.Millisecond, 0, 20*time.Millisecond, 2
	t.Setenv("UAB_FAKE_EFIVARS", t.TempDir())
	img := mbrImage(t, true)
	missing := filepath.Join(t.TempDir(), "ainda-nao-conectado.img")
	t.Setenv("UAB_FAKE_DISK", missing)

	var mu sync.Mutex
	var states []string
	cancel := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		states = append(states, m["state"])
		n := len(states)
		c := cancel
		mu.Unlock()
		if n == 3 { // "conecta o pendrive" depois de algumas esperas
			_ = os.Rename(img, missing)
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"cancel": c})
	}))
	defer ts.Close()

	if code := runBootUSB(time.Minute, ts.URL); code != 0 {
		t.Fatalf("saída %d, estados %v", code, states)
	}
	joined := strings.Join(states, ",")
	if !strings.HasPrefix(joined, "aguardando,aguardando,aguardando") || !strings.Contains(joined, "encontrado") || !strings.HasSuffix(joined, "reiniciando") {
		t.Fatalf("estados: %s", joined)
	}
	if got, _ := fakeEFIGet("BootNext"); !bytes.Equal(got, []byte{0x00, 0x1B}) {
		t.Fatalf("BootNext = %x", got)
	}

	// cancelado pelo painel enquanto espera
	mu.Lock()
	states, cancel = nil, true
	mu.Unlock()
	_ = os.Remove(missing)
	if code := runBootUSB(time.Minute, ts.URL); code != 5 {
		t.Fatalf("cancelar: saída %d, estados %v", code, states)
	}
	// tempo esgotado sem pendrive
	mu.Lock()
	states, cancel = nil, false
	mu.Unlock()
	if code := runBootUSB(0, ts.URL); code != 4 || states[len(states)-1] != "expirou" {
		t.Fatalf("expirar: saída %d, estados %v", code, states)
	}
}
