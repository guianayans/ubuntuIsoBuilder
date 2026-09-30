//go:build windows

package main

// "Formatar este computador (sem pendrive)" no Windows: o gravador (no Windows, como
// administrador) reserva uma partição no próprio disco com a imagem do Windows, e faz o PC
// reiniciar num WinPE pequeno via ramdisk — o mesmo mecanismo do WinRE, que funciona em
// BIOS e UEFI. Como só o WinPE (pequeno) vai para a RAM, cabe no boot do BIOS antigo. O
// pe.js roda no WinPE, PRESERVA a partição da imagem, apaga o resto do disco e instala.
//
// Segurança: backup do BCD, só entradas novas, boot de UMA vez só (bootsequence). No modo
// de teste (dry-run) o pe.js só escreve o plano na partição reservada e volta ao Windows,
// sem apagar nada. Qualquer falha antes do apagamento volta ao Windows normal.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unsafe"
)

const mediaLabel = "UABMEDIA" // rótulo da partição reservada (o pe.js a reconhece e preserva)

func osTotalRAMGB() int {
	var m struct {
		Length               uint32
		MemoryLoad           uint32
		TotalPhys            uint64
		AvailPhys            uint64
		TotalPage            uint64
		AvailPage            uint64
		TotalVirtual         uint64
		AvailVirtual         uint64
		AvailExtendedVirtual uint64
	}
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := k32.NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return int(m.TotalPhys / (1 << 30))
}

// A RAM já é conferida em localInstallInfo; aqui só o que é específico do Windows.
func osLocalInstallReason(kind string) string { return "" }

var reBCDGUID = regexp.MustCompile(`\{[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}`)

func psOut(script string) (string, error) {
	out, err := hidden("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func bcd(args ...string) (string, error) {
	out, err := hidden("bcdedit.exe", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// setupWimIndex: índice da imagem "Windows Setup" no boot.wim (normalmente 2).
func setupWimIndex(bootWim string) int {
	out, _ := hidden("dism.exe", "/English", "/Get-WimInfo", "/WimFile:"+bootWim).CombinedOutput()
	idx, cur := 0, 0
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Index :") {
			fmt.Sscanf(l, "Index : %d", &cur)
		} else if strings.HasPrefix(l, "Name :") && strings.Contains(strings.ToLower(l), "setup") {
			idx = cur
		}
	}
	if idx == 0 {
		idx = 2
	}
	return idx
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// dismRun roda o dism e devolve as últimas linhas em caso de erro.
func dismRun(args ...string) error {
	out, err := hidden("dism.exe", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", lastLines(string(out)))
	}
	return nil
}

// mediaPartitionLetter: reaproveita a partição UABMEDIA se já existe; senão encolhe o maior
// volume fixo e cria uma nova (NTFS, rótulo UABMEDIA) com espaço para a imagem. Devolve "P:".
func mediaPartitionLetter(needBytes int64) (string, error) {
	if l, _ := psOut("(Get-Volume -FileSystemLabel '" + mediaLabel + "' -ErrorAction SilentlyContinue).DriveLetter"); len(strings.TrimSpace(l)) == 1 {
		return strings.TrimSpace(l) + ":", nil // já existe: reusa
	}
	needMB := needBytes/(1<<20) + 1536 // imagem + folga
	// encolhe o volume do sistema e cria a partição reservada no espaço liberado
	script := strings.Join([]string{
		"$d = (Get-Partition -DriveLetter " + strings.TrimSuffix(os.Getenv("SystemDrive"), ":") + ").DiskNumber",
		fmt.Sprintf("$s = Get-PartitionSupportedSize -DriveLetter %s", strings.TrimSuffix(os.Getenv("SystemDrive"), ":")),
		fmt.Sprintf("$cur = (Get-Partition -DriveLetter %s).Size", strings.TrimSuffix(os.Getenv("SystemDrive"), ":")),
		fmt.Sprintf("$new = $cur - %dMB", needMB),
		"if ($new -lt $s.SizeMin) { Write-Error 'sem espaco para encolher'; exit 1 }",
		fmt.Sprintf("Resize-Partition -DriveLetter %s -Size $new", strings.TrimSuffix(os.Getenv("SystemDrive"), ":")),
		"$p = New-Partition -DiskNumber $d -UseMaximumSize",
		"Format-Volume -Partition $p -FileSystem NTFS -NewFileSystemLabel '" + mediaLabel + "' -Confirm:$false | Out-Null",
		"$p | Add-PartitionAccessPath -AssignDriveLetter",
		"(Get-Volume -FileSystemLabel '" + mediaLabel + "').DriveLetter",
	}, "; ")
	out, err := psOut(script)
	letter := strings.TrimSpace(lastNonEmpty(out))
	if err != nil || len(letter) != 1 {
		return "", fmt.Errorf("não consegui reservar a partição: %s", out)
	}
	return letter + ":", nil
}

func lastNonEmpty(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// deletePrevBCD apaga entradas isoBuilder de tentativas anteriores (para não acumular).
func deletePrevBCD() {
	out, _ := bcd("/enum", "osloader")
	block := ""
	for _, l := range strings.Split(out, "\n") {
		block += l + "\n"
		if strings.TrimSpace(l) == "" {
			if strings.Contains(block, "isoBuilder") {
				if g := reBCDGUID.FindString(block); g != "" {
					bcd("/delete", g, "/f")
				}
			}
			block = ""
		}
	}
}

func osInstallLocal(a *app, isoPath, isoURL, unattendPath string, dry bool, p *progress) {
	fake := os.Getenv("UAB_FAKE_DISK") != ""
	sysDrive := os.Getenv("SystemDrive")
	if sysDrive == "" {
		sysDrive = "C:"
	}
	stage := sysDrive + `\uab-reinstall`
	mnt := filepath.Join(stage, "mnt")
	fail := func(msg string) { p.finish("error", msg, "") }
	step := func(msg string) { p.set("staging", msg, 0) }

	hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Discard").Run()
	hidden("dism.exe", "/Cleanup-Wim").Run()
	os.RemoveAll(stage)
	for _, d := range []string{filepath.Join(stage, "sources"), filepath.Join(stage, "boot"), mnt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail("não consegui criar a pasta de trabalho: " + err.Error())
			return
		}
	}

	// 1. abre a ISO do Windows
	step("Abrindo a imagem do Windows…")
	var drive string
	if fake {
		drive = os.Getenv("UAB_FAKE_ISO_DRIVE")
	} else {
		out, err := psOut("$v = Mount-DiskImage -ImagePath '" + isoPath + "' -PassThru | Get-Volume; $v.DriveLetter")
		letter := strings.TrimSpace(lastNonEmpty(out))
		if err != nil || len(letter) != 1 {
			fail("não consegui abrir a ISO do Windows: " + out)
			return
		}
		drive = letter + ":"
		defer hidden("powershell.exe", "-NoProfile", "-Command", "Dismount-DiskImage -ImagePath '"+isoPath+"'").Run()
	}
	if _, err := os.Stat(drive + `\sources\boot.wim`); err != nil {
		fail("a ISO não tem sources\\boot.wim (ISO não suportada)")
		return
	}
	var srcInstall, dstInstall string
	for _, n := range []string{"install.wim", "install.esd"} {
		if _, err := os.Stat(drive + `\sources\` + n); err == nil {
			srcInstall, dstInstall = drive+`\sources\`+n, n
			break
		}
	}
	if srcInstall == "" {
		fail("a ISO não tem sources\\install.wim/.esd")
		return
	}
	fi, _ := os.Stat(srcInstall)
	installSize := int64(0)
	if fi != nil {
		installSize = fi.Size()
	}

	// 2. WinPE pequeno (índice "Setup") -> stage\sources\boot.wim (poucas centenas de MB: cabe no ramdisk do BIOS)
	step("Preparando o WinPE (1/3)…")
	dstBoot := filepath.Join(stage, "sources", "boot.wim")
	if !fake {
		if err := dismRun("/Export-Image", "/SourceImageFile:"+drive+`\sources\boot.wim`,
			fmt.Sprintf("/SourceIndex:%d", setupWimIndex(drive+`\sources\boot.wim`)), "/DestinationImageFile:"+dstBoot); err != nil {
			fail("falha ao preparar o WinPE: " + err.Error())
			return
		}
	}

	// 3. injeta o autounattend (com pe.js) no WinPE; o marcador uab-dryrun liga o modo teste
	step("Preparando o WinPE (2/3)…")
	if !fake {
		if err := dismRun("/Mount-Wim", "/WimFile:"+dstBoot, "/Index:1", "/MountDir:"+mnt); err != nil {
			fail("falha ao montar o WinPE: " + err.Error())
			return
		}
		// o Setup, num WinPE por ramdisk, procura o arquivo de respostas em \Windows\Panther;
		// gravamos lá (para o Setup achar) e na raiz (o extrator do pe.js também procura nas raízes).
		panther := filepath.Join(mnt, "Windows", "Panther")
		os.MkdirAll(panther, 0o755)
		okAny := false
		for _, dst := range []string{filepath.Join(mnt, "autounattend.xml"), filepath.Join(panther, "unattend.xml")} {
			if err := copyFile(unattendPath, dst); err == nil {
				okAny = true
			}
		}
		if !okAny {
			hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Discard").Run()
			fail("falha ao gravar as respostas no WinPE")
			return
		}
		// marcadores lidos pelo pe.js: "sem pendrive" (preserva a partição da imagem e não
		// abre janelas, pois não há técnico na tela) e, no teste, "só escreva o plano".
		os.WriteFile(filepath.Join(mnt, "uab-nopendrive"), []byte("1"), 0o644)
		if dry {
			os.WriteFile(filepath.Join(mnt, "uab-dryrun"), []byte("1"), 0o644)
		}
		step("Preparando o WinPE (3/3)…")
		if err := dismRun("/Unmount-Wim", "/MountDir:"+mnt, "/Commit"); err != nil {
			hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Discard").Run()
			fail("falha ao finalizar o WinPE: " + err.Error())
			return
		}
	}
	if err := copyFile(drive+`\boot\boot.sdi`, filepath.Join(stage, "boot", "boot.sdi")); err != nil {
		fail("falha ao copiar o boot.sdi: " + err.Error())
		return
	}

	// 4. partição reservada com a imagem do Windows (o pe.js instala a partir dela e a preserva)
	step("Reservando espaço no disco e copiando a imagem do Windows… (alguns minutos)")
	var media string
	if fake {
		media = os.Getenv("UAB_FAKE_MEDIA_DIR")
	} else {
		var err error
		if media, err = mediaPartitionLetter(installSize); err != nil {
			fail(err.Error())
			return
		}
		os.MkdirAll(media+`\sources`, 0o755)
		if err := copyFile(srcInstall, media+`\sources\`+dstInstall); err != nil {
			fail("falha ao copiar a imagem do Windows para a partição reservada: " + err.Error())
			return
		}
	}

	// 5. entradas de boot (ramdisk do WinPE pequeno em C:), modeladas na entrada do WinRE
	step("Configurando a inicialização…")
	winload, mgr := `\windows\system32\winload.efi`, "{fwbootmgr}"
	if !uefiFirmware() {
		winload, mgr = `\windows\system32\winload.exe`, "{bootmgr}"
	}
	ramdisk := "ramdisk=[" + sysDrive + `]\uab-reinstall\sources\boot.wim,`
	if fake {
		if f := os.Getenv("UAB_FAKE_KEXEC"); f != "" {
			os.WriteFile(f, []byte(fmt.Sprintf("dry=%v media=%s winload=%s mgr=%s device=%s{OPTS}\nsdi=%s\\uab-reinstall\\boot\\boot.sdi\ninstall=%s",
				dry, media, winload, mgr, ramdisk, sysDrive, dstInstall)), 0o644)
		}
		p.finish("done", "(teste) instalador preparado; nada foi reiniciado.", "")
		return
	}
	bcd("/export", filepath.Join(stage, "bcd-backup"))
	deletePrevBCD()
	optsOut, err := bcd("/create", "/d", "isoBuilder ramdisk", "/device")
	opts := reBCDGUID.FindString(optsOut)
	if err != nil || opts == "" {
		fail("a inicialização não aceitou a configuração do ramdisk: " + optsOut)
		return
	}
	bcd("/set", opts, "ramdisksdidevice", "partition="+sysDrive)
	bcd("/set", opts, "ramdisksdipath", `\uab-reinstall\boot\boot.sdi`)
	entOut, err := bcd("/create", "/d", "isoBuilder - Instalar Windows", "/application", "osloader")
	ent := reBCDGUID.FindString(entOut)
	if err != nil || ent == "" {
		fail("a inicialização não aceitou a entrada de boot: " + entOut)
		return
	}
	for _, c := range [][]string{
		{"/set", ent, "device", ramdisk + opts},
		{"/set", ent, "osdevice", ramdisk + opts},
		{"/set", ent, "path", winload},
		{"/set", ent, "systemroot", `\windows`},
		{"/set", ent, "winpe", "yes"},
		{"/set", ent, "nx", "OptIn"},
		{"/set", ent, "detecthal", "yes"},
	} {
		if out, err := bcd(c...); err != nil {
			fail("falha ao configurar a inicialização: " + out)
			return
		}
	}
	if out, err := bcd("/set", mgr, "bootsequence", ent); err != nil {
		fail("falha ao marcar o próximo boot: " + out)
		return
	}

	if dry {
		p.finish("done", "Modo teste: o PC vai reiniciar no instalador, escrever o plano (sem apagar nada) e voltar ao Windows. Depois me mande o arquivo "+media+`\uab-plan.txt`+".", "")
	} else {
		osBeforeInstallReboot()
		p.set("rebooting", "Tudo pronto. Reiniciando para instalar o Windows; o computador volta a aparecer no painel quando terminar.", 0)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := exec.Command("shutdown.exe", "/r", "/t", "5", "/c", "isoBuilder: reiniciando").Run(); err != nil {
		fail("não consegui reiniciar: " + err.Error())
		return
	}
	if dry {
		return
	}
	p.finish("done", "Reiniciando para instalar…", "")
}

func lastLines(s string) string {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "\n")
	if len(parts) > 6 {
		parts = parts[len(parts)-6:]
	}
	return strings.TrimSpace(strings.Join(parts, " | "))
}
