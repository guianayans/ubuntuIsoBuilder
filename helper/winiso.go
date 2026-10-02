package main

// Pendrive do Windows 11: confere a ISO (UDF + XML do install.wim) e grava um
// pendrive FAT32 com os arquivos dela, o install.wim dividido em .swm quando
// passa de 4 GB e o autounattend.xml (configuração do painel) na raiz.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

const winLabel = "WIN11"

type winISOInfo struct {
	Build      int        `json:"build"`
	Lang       string     `json:"lang"`
	Arch       string     `json:"arch"`
	Edition    string     `json:"edition"` // nome da imagem escolhida (ex.: "Windows 11 Pro")
	Index      int        `json:"index"`
	Images     []wimImage `json:"-"`
	Install    string     `json:"install"` // sources/install.wim ou .esd
	InstallSz  int64      `json:"install_size"`
	TotalBytes int64      `json:"total_bytes"` // soma dos arquivos da ISO
}

var wimArch = map[string]int{"x86": 0, "x64": 9, "arm64": 12}

// editionIDs: nome do painel -> EDITIONID do XML do WIM.
var editionIDs = map[string]string{"pro": "Professional", "home": "Core", "education": "Education",
	"enterprise": "Enterprise", "pro education": "ProfessionalEducation", "pro for workstations": "ProfessionalWorkstation"}

// inspectWindowsISO confere que a ISO é do Windows 11 na arquitetura, com a
// edição e (se dado) no idioma pedidos.
func inspectWindowsISO(isoPath, arch, edition, lang string) (*winISOInfo, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	u, err := OpenUDF(f)
	if err != nil {
		return nil, errors.New("esta ISO não é um instalador do Windows (" + err.Error() + ")")
	}
	return inspectUDF(u, arch, edition, lang)
}

// winVersionFromBuild: nome comercial do Windows pelo número de build do install.wim.
func winVersionFromBuild(b int) string {
	switch {
	case b >= 22000:
		return "11"
	case b >= 10240:
		return "10"
	case b >= 9600:
		return "8.1"
	case b >= 9200:
		return "8"
	case b >= 7600:
		return "7"
	default:
		return ""
	}
}

// inspectWindowsISOList lista TODAS as edições de uma ISO do Windows (qualquer versão: 7/8/10/11),
// SEM travar por build nem por edição. Base da "biblioteca de ISOs do Windows" do painel: o painel
// chama isto no upload para saber versão/edições e deixar o admin escolher.
func inspectWindowsISOList(isoPath string) (map[string]any, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	u, err := OpenUDF(f)
	if err != nil {
		return nil, errors.New("esta ISO não é um instalador do Windows (" + err.Error() + ")")
	}
	if u.Lookup("sources/boot.wim") == nil {
		return nil, errors.New("esta ISO não é um instalador do Windows (falta sources/boot.wim)")
	}
	var inst *UDFFile
	for _, n := range []string{"sources/install.wim", "sources/install.esd"} {
		if inst = u.Lookup(n); inst != nil {
			break
		}
	}
	if inst == nil {
		return nil, errors.New("esta ISO não tem sources/install.wim (não é a ISO de instalação do Windows)")
	}
	r := u.Open(inst)
	h, err := readWIMHeader(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", inst.Path, err)
	}
	xml, err := readWIMXML(r, h)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", inst.Path, err)
	}
	imgs := wimImages(xml)
	if len(imgs) == 0 {
		return nil, errors.New("não encontrei edições no install.wim")
	}
	archName := map[int]string{0: "x86", 9: "x64", 12: "arm64"}
	maxBuild, lang := 0, ""
	archSeen := map[string]bool{}
	eds := make([]map[string]any, 0, len(imgs))
	for _, im := range imgs {
		if im.Build > maxBuild {
			maxBuild = im.Build
		}
		if lang == "" {
			lang = im.Lang
		}
		if a := archName[im.Arch]; a != "" {
			archSeen[a] = true
		}
		eds = append(eds, map[string]any{
			"index": im.Index, "name": im.Name, "edition_id": im.EditionID,
			"build": im.Build, "lang": im.Lang, "arch": archName[im.Arch],
		})
	}
	archs := make([]string, 0, len(archSeen))
	for a := range archSeen {
		archs = append(archs, a)
	}
	var total int64
	for _, fl := range u.Files {
		if !fl.Dir {
			total += fl.Size
		}
	}
	return map[string]any{
		"version": winVersionFromBuild(maxBuild), "build": maxBuild, "lang": lang,
		"archs": archs, "install": inst.Path, "install_size": inst.Size, "total_bytes": total,
		"editions": eds,
	}, nil
}

func inspectUDF(u *UDF, arch, edition, lang string) (*winISOInfo, error) {
	if arch == "" {
		arch = "x64"
	}
	if edition == "" {
		edition = "Pro"
	}
	efi := map[string]string{"x64": "efi/boot/bootx64.efi", "arm64": "efi/boot/bootaa64.efi"}[arch]
	if efi == "" {
		return nil, fmt.Errorf("arquitetura %q não suportada", arch)
	}
	if u.Lookup("sources/boot.wim") == nil || u.Lookup("bootmgr.efi") == nil {
		return nil, errors.New("esta ISO não é um instalador do Windows (faltam sources/boot.wim e bootmgr.efi)")
	}
	if u.Lookup(efi) == nil {
		return nil, fmt.Errorf("esta ISO não é para PCs %s (falta %s)", arch, efi)
	}
	info := &winISOInfo{Arch: arch}
	for _, fl := range u.Files {
		if !fl.Dir {
			info.TotalBytes += fl.Size
		}
	}
	var inst *UDFFile
	for _, n := range []string{"sources/install.wim", "sources/install.esd"} {
		if inst = u.Lookup(n); inst != nil {
			info.Install = inst.Path
			break
		}
	}
	if inst == nil {
		return nil, errors.New("esta ISO não tem sources/install.wim (não é a ISO de instalação do Windows)")
	}
	info.InstallSz = inst.Size
	r := u.Open(inst)
	h, err := readWIMHeader(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", inst.Path, err)
	}
	xml, err := readWIMXML(r, h)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", inst.Path, err)
	}
	info.Images = wimImages(xml)
	wantID := editionIDs[strings.ToLower(edition)]
	if wantID == "" {
		wantID = edition // já é um EDITIONID (ex.: "EnterpriseEval")
	}
	for _, im := range info.Images {
		if im.Build > info.Build {
			info.Build = im.Build
		}
		if info.Lang == "" {
			info.Lang = im.Lang
		}
		if info.Index == 0 && im.Arch == wimArch[arch] && (edition == "*" || strings.EqualFold(im.EditionID, wantID) ||
			wantID == "" && strings.HasSuffix(strings.ToLower(im.Name), " "+strings.ToLower(edition))) {
			info.Index, info.Edition = im.Index, im.Name
		}
	}
	if info.Build < 22000 {
		return nil, fmt.Errorf("esta ISO não é do Windows 11 (build %d)", info.Build)
	}
	if info.Index == 0 {
		var names []string
		for _, im := range info.Images {
			names = append(names, im.Name)
		}
		return nil, fmt.Errorf("esta ISO do Windows 11 não tem a edição %s %s (tem: %s)", edition, arch, strings.Join(names, ", "))
	}
	if lang != "" && info.Lang != "" && !strings.EqualFold(lang, info.Lang) {
		return nil, fmt.Errorf("esta ISO é do Windows em %s, mas a configuração pede %s. Use a ISO no idioma certo", info.Lang, lang)
	}
	return info, nil
}

// windowsStickFiles lista o que vai para o pendrive: arquivos da ISO (com o
// install.wim dividido se precisar), autounattend.xml e a pasta INSTALAR.
func windowsStickFiles(u *UDF, unattend []byte, tools map[string][]byte) ([]fsFile, int, error) {
	var out []fsFile
	parts := 0
	for _, fl := range u.Files {
		lp := strings.ToLower(fl.Path)
		if fl.Dir {
			out = append(out, fsFile{Path: fl.Path, Dir: true})
			continue
		}
		if lp == "autounattend.xml" || lp == "unattend.xml" {
			continue // a do painel substitui
		}
		r := u.Open(fl)
		if lp == "sources/install.wim" && fl.Size > 0xFFFFFFFF {
			base := strings.TrimSuffix(fl.Path, path.Ext(fl.Path))
			ps, err := planSplit(r, path.Base(base), wimMaxPart)
			if err != nil {
				return nil, 0, fmt.Errorf("não foi possível dividir o install.wim: %w", err)
			}
			for _, p := range ps {
				out = append(out, fsFile{Path: path.Join(path.Dir(fl.Path), p.Name), Size: p.Size, Segs: p.Segs})
			}
			parts = len(ps)
			continue
		}
		if fl.Size > 0xFFFFFFFF {
			return nil, 0, fmt.Errorf("%s tem mais de 4 GB e não pode ir para FAT32", fl.Path)
		}
		var segs []segment
		if fl.Data != nil {
			segs = []segment{{Data: fl.Data}}
		} else if fl.Size > 0 {
			segs = []segment{{R: r, Off: 0, N: fl.Size}}
		}
		out = append(out, fsFile{Path: fl.Path, Size: fl.Size, Segs: segs})
	}
	if len(unattend) > 0 {
		out = append(out, fsFile{Path: "autounattend.xml", Size: int64(len(unattend)), Segs: []segment{{Data: unattend}}})
	}
	for rel, data := range tools {
		out = append(out, fsFile{Path: path.Join(toolsLabel, rel), Size: int64(len(data)), Segs: []segment{{Data: data}}})
	}
	return out, parts, nil
}

// winStickSize: espaço mínimo (bytes) de pendrive para esta ISO.
func winStickSize(isoSize int64) int64 { return isoSize + isoSize/20 + 64<<20 }

// writeWindowsStick grava o pendrive do Windows no dispositivo já aberto.
func writeWindowsStick(isoPath string, unattend []byte, tools map[string][]byte, dev rwAt, devSize int64, p *progress) (string, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	p.set("preparing", "Lendo a ISO do Windows…", 0)
	u, err := OpenUDF(f)
	if err != nil {
		return "", errors.New("esta ISO não é um instalador do Windows (" + err.Error() + ")")
	}
	// edição e idioma já foram conferidos ao escolher a ISO; aqui só a estrutura
	if _, err := inspectUDF(u, "", "*", ""); err != nil {
		return "", err
	}
	files, parts, err := windowsStickFiles(u, unattend, tools)
	if err != nil {
		return "", err
	}
	partSize := (devSize - bigPartStart - bigAlign) &^ (bigAlign - 1)
	if partSize <= 0 {
		return "", errors.New("pendrive pequeno demais")
	}
	plan, err := planBigFAT(partSize, winLabel, files, time.Now())
	if err != nil {
		if strings.Contains(err.Error(), "não cabem") {
			return "", fmt.Errorf("o pendrive (%s) é pequeno demais para o Windows (precisa de uns %s)", human(devSize), human(winStickSize(sizeOf(f))))
		}
		return "", err
	}
	if err := plan.write(dev, devSize, p); err != nil {
		return "", err
	}
	msg := ""
	if parts > 1 {
		msg = fmt.Sprintf("install.wim dividido em %d partes", parts)
	}
	return msg, nil
}

func sizeOf(f *os.File) int64 {
	fi, err := f.Stat()
	if err != nil {
		return 0
	}
	return fi.Size()
}

// runWriteWindows: equivalente de runWrite para o pendrive do Windows.
func runWriteWindows(isoPath, diskID, unattendPath, toolsDir string, p *progress) {
	unattend, err := os.ReadFile(unattendPath)
	if err != nil || len(unattend) == 0 {
		p.finish("error", "configuração (autounattend.xml) não encontrada", "")
		return
	}
	var tools map[string][]byte
	if toolsDir != "" {
		tools, _ = readToolsDir(toolsDir)
	}
	d, err := findDisk(diskID, "")
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	if fi, err := os.Stat(isoPath); err == nil && winStickSize(fi.Size()) > d.Size {
		p.finish("error", fmt.Sprintf("o pendrive (%s) é pequeno demais para o Windows: use um de 16 GB ou mais", human(d.Size)), "")
		return
	}
	p.set("preparing", "Preparando o pendrive…", 0)
	if err := prepareDisk(d); err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	dev, err := openDevice(d)
	if err != nil {
		p.finish("error", "não foi possível abrir o pendrive: "+err.Error(), "")
		return
	}
	note, err := writeWindowsStick(isoPath, unattend, tools, dev, d.Size, p)
	dev.Close()
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	finalizeDisk(d)
	msg := "Pendrive do Windows 11 pronto e verificado! Pode removê-lo e usar para instalar."
	if len(tools) > 0 {
		msg += " A pasta INSTALAR dele tem os programas para reiniciar e instalar no computador de destino."
	}
	if note != "" {
		msg += " (" + note + ".)"
	}
	p.finish("done", msg, "")
}

var _ io.ReaderAt = (*udfReader)(nil)
