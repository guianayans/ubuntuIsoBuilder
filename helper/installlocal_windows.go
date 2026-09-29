//go:build windows

package main

// "Formatar este computador (sem pendrive)" no Windows: o gravador (rodando no Windows,
// como administrador) monta um WinPE que leva a imagem do Windows DENTRO dele e faz o PC
// reiniciar por ele via ramdisk — o mesmo mecanismo que o Windows já usa para o WinRE, e
// que funciona igual em BIOS e UEFI. Como tudo vai para a RAM (por isso a checagem de
// memória), o disco inteiro pode ser apagado; o pe.js que instala é o MESMO do pendrive.
//
// Segurança: faz backup do BCD, cria só entradas novas e usa "bootsequence" (boot de UMA
// vez só). Se qualquer passo falhar antes de reiniciar, nada no disco é tocado; se a
// entrada de boot falhar, o PC volta ao Windows normal.

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

// bcd roda o bcdedit e devolve a saída (para pegar o GUID criado).
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
		idx = 2 // padrão das mídias do Windows
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

func osInstallLocal(a *app, isoPath, isoURL, unattendPath string, p *progress) {
	fake := os.Getenv("UAB_FAKE_DISK") != ""
	sysDrive := os.Getenv("SystemDrive")
	if sysDrive == "" {
		sysDrive = "C:"
	}
	stage := sysDrive + `\uab-reinstall`
	mnt := filepath.Join(stage, "mnt")

	fail := func(msg string) { p.finish("error", msg, "") }
	step := func(phase, msg string) { p.set(phase, msg, 0) }

	// limpa uma tentativa anterior (desmonta um wim que tenha ficado montado)
	hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Discard").Run()
	hidden("dism.exe", "/Cleanup-Wim").Run()
	os.RemoveAll(stage)
	for _, d := range []string{filepath.Join(stage, "sources"), filepath.Join(stage, "boot")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail("não consegui criar a pasta de trabalho em " + stage + ": " + err.Error())
			return
		}
	}

	// 1. monta a ISO do Windows
	step("staging", "Abrindo a imagem do Windows…")
	var drive string
	if fake {
		drive = os.Getenv("UAB_FAKE_ISO_DRIVE") // teste: aponta para uma pasta com sources\boot.wim
	} else {
		out, err := psOut("$v = Mount-DiskImage -ImagePath '" + isoPath + "' -PassThru | Get-Volume; $v.DriveLetter")
		letter := strings.TrimSpace(out)
		if err != nil || len(letter) != 1 {
			fail("não consegui abrir a ISO do Windows: " + out)
			return
		}
		drive = letter + ":"
		defer hidden("powershell.exe", "-NoProfile", "-Command", "Dismount-DiskImage -ImagePath '"+isoPath+"'").Run()
	}
	srcBoot := drive + `\sources\boot.wim`
	if _, err := os.Stat(srcBoot); err != nil {
		fail("a ISO não tem sources\\boot.wim (ISO não suportada)")
		return
	}
	// imagem do sistema: install.wim, .esd ou dividida (.swm)
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

	// 2. WinPE de instalação (índice "Setup") -> boot.wim de um índice só
	step("staging", "Preparando o instalador (1/3)…")
	dstBoot := filepath.Join(stage, "sources", "boot.wim")
	if !fake {
		idx := setupWimIndex(srcBoot)
		if out, err := hidden("dism.exe", "/Export-Image", "/SourceImageFile:"+srcBoot,
			fmt.Sprintf("/SourceIndex:%d", idx), "/DestinationImageFile:"+dstBoot).CombinedOutput(); err != nil {
			fail("falha ao preparar o WinPE: " + lastLines(string(out)))
			return
		}
	}

	// 3. injeta a imagem do Windows + o autounattend DENTRO do WinPE (vai tudo para a RAM)
	step("staging", "Preparando o instalador (2/3): copiando a imagem do Windows… (alguns minutos)")
	if !fake {
		if out, err := hidden("dism.exe", "/Mount-Wim", "/WimFile:"+dstBoot, "/Index:1", "/MountDir:"+mnt).CombinedOutput(); err != nil {
			fail("falha ao montar o WinPE: " + lastLines(string(out)))
			return
		}
		if err := copyFile(srcInstall, filepath.Join(mnt, "sources", dstInstall)); err != nil {
			hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Discard").Run()
			fail("falha ao copiar a imagem do Windows: " + err.Error())
			return
		}
		if err := copyFile(unattendPath, filepath.Join(mnt, "autounattend.xml")); err != nil {
			hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Discard").Run()
			fail("falha ao gravar as respostas: " + err.Error())
			return
		}
		step("staging", "Preparando o instalador (3/3): finalizando o WinPE… (alguns minutos)")
		if out, err := hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Commit").CombinedOutput(); err != nil {
			hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Discard").Run()
			fail("falha ao finalizar o WinPE: " + lastLines(string(out)))
			return
		}
	}
	if err := copyFile(drive+`\boot\boot.sdi`, filepath.Join(stage, "boot", "boot.sdi")); err != nil {
		fail("falha ao copiar o boot.sdi: " + err.Error())
		return
	}

	// 4. entradas de boot (modeladas na entrada do WinRE deste PC), com backup e boot de uma vez só
	step("staging", "Configurando a inicialização…")
	if !fake {
		bcd("/export", filepath.Join(stage, "bcd-backup"))
	}
	winload := `\windows\system32\winload.efi`
	mgr := "{fwbootmgr}"
	if !uefiFirmware() {
		winload, mgr = `\windows\system32\winload.exe`, "{bootmgr}"
	}
	ramdisk := "ramdisk=[" + sysDrive + `]\uab-reinstall\sources\boot.wim,`

	if fake { // teste: só registra o que faria, sem tocar no BCD nem reiniciar
		plan := fmt.Sprintf("winload=%s mgr=%s device=%s{OPTS}\nsdipath=\\uab-reinstall\\boot\\boot.sdi\ninstall=%s", winload, mgr, ramdisk, dstInstall)
		if f := os.Getenv("UAB_FAKE_KEXEC"); f != "" {
			os.WriteFile(f, []byte(plan), 0o644)
		}
		p.finish("done", "(teste) instalador preparado; nada foi reiniciado.", "")
		return
	}

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

	osBeforeInstallReboot() // suspende o BitLocker por um reinício (inofensivo se estiver desligado)
	step("rebooting", "Tudo pronto. Reiniciando para instalar o Windows; o computador volta a aparecer no painel quando terminar.")
	time.Sleep(1500 * time.Millisecond)
	if err := exec.Command("shutdown.exe", "/r", "/t", "5", "/c", "isoBuilder: reiniciando para instalar o Windows").Run(); err != nil {
		fail("não consegui reiniciar: " + err.Error())
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
