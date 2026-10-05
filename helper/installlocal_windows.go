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
	// Reaproveita a UABMEDIA se já existir — inclusive SEM letra de unidade (a pós-instalação
	// remove a letra mas mantém a partição, para reformatar sem pendrive à distância de novo):
	// acha pela etiqueta e, se estiver sem letra, atribui uma na hora.
	reuse := "$p = Get-Partition | ? { ($_ | Get-Volume -EA SilentlyContinue).FileSystemLabel -eq '" + mediaLabel + "' } | select -First 1; " +
		"if ($p) { if (-not $p.DriveLetter) { $p | Add-PartitionAccessPath -AssignDriveLetter -EA SilentlyContinue | Out-Null; " +
		"$p = Get-Partition -DiskNumber $p.DiskNumber -PartitionNumber $p.PartitionNumber }; $p.DriveLetter }"
	if l, _ := psOut(reuse); len(lastNonEmpty(l)) == 1 {
		return lastNonEmpty(l) + ":", nil // já existe: reusa (com letra atribuída se preciso)
	}
	needMB := needBytes/(1<<20) + 1536 // imagem + folga
	sd := strings.TrimSuffix(os.Getenv("SystemDrive"), ":")
	// Encolhe o volume do sistema e cria a partição reservada no espaço liberado. Antes, desliga
	// a hibernação (hiberfil.sys costuma travar o encolhimento) — reversível, sem reiniciar.
	// Se mesmo assim não couber, devolve um erro CLARO (pagefile/restauração seguram o fim do C:).
	script := strings.Join([]string{
		"powercfg /h off 2>$null | Out-Null",
		"$d = (Get-Partition -DriveLetter " + sd + ").DiskNumber",
		"$s = Get-PartitionSupportedSize -DriveLetter " + sd,
		"$cur = (Get-Partition -DriveLetter " + sd + ").Size",
		fmt.Sprintf("$new = $cur - %dMB", needMB),
		"if ($new -lt $s.SizeMin) { Write-Output ('UABNOSPACE ' + [int](($cur - $s.SizeMin)/1MB)); exit 1 }",
		"Resize-Partition -DriveLetter " + sd + " -Size $new",
		"$p = New-Partition -DiskNumber $d -UseMaximumSize",
		"Format-Volume -Partition $p -FileSystem NTFS -NewFileSystemLabel '" + mediaLabel + "' -Confirm:$false | Out-Null",
		"$p | Add-PartitionAccessPath -AssignDriveLetter",
		"(Get-Volume -FileSystemLabel '" + mediaLabel + "').DriveLetter",
	}, "; ")
	out, err := psOut(script)
	if strings.Contains(out, "UABNOSPACE") {
		free := strings.TrimSpace(strings.TrimPrefix(lastNonEmpty(out), "UABNOSPACE"))
		return "", fmt.Errorf("o %s: não tem espaço livre para reservar a partição da imagem (~%d MB): só dá para encolher %s MB. "+
			"Libere espaço no Windows (desligar o arquivo de paginação e a restauração do sistema e reiniciar) ou use a opção com pendrive", sd, needMB, free)
	}
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

// extractPeJS reconstrói o pe.js a partir das linhas "UABPE|..." do autounattend (o mesmo
// que o WinPE faz com find/for). As únicas linhas com "UABPE|" literal são as do pe.js.
func extractPeJS(unattend []byte) string {
	const pfx = "UABPE|"
	var b strings.Builder
	for _, line := range strings.Split(string(unattend), "\n") {
		if i := strings.Index(line, pfx); i >= 0 {
			b.WriteString(strings.TrimRight(line[i+len(pfx):], "\r"))
			b.WriteString("\r\n")
		}
	}
	return b.String()
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
		abort := func(msg string) { hidden("dism.exe", "/Unmount-Wim", "/MountDir:"+mnt, "/Discard").Run(); fail(msg) }
		// O autounattend fica na raiz do WinPE: o pe.js o encontra (tem a marca uab-build) e o
		// copia para o Windows instalado, para os passos specialize/oobeSystem rodarem.
		if err := copyFile(unattendPath, filepath.Join(mnt, "autounattend.xml")); err != nil {
			abort("falha ao gravar as respostas no WinPE: " + err.Error())
			return
		}
		// Rodamos o pe.js DIRETO (não pelo Setup, cuja detecção do autounattend não é confiável
		// no boot por ramdisk): extraímos o pe.js do autounattend e o winpeshl.ini o executa.
		unattendBytes, _ := os.ReadFile(unattendPath)
		pejs := extractPeJS(unattendBytes)
		if len(pejs) < 500 {
			abort("não consegui extrair o instalador (pe.js) do arquivo de respostas")
			return
		}
		// SEGURANÇA: só prossegue se o pe.js tiver as proteções (modo teste e preservação da
		// partição). Um pe.js antigo apagaria o disco ignorando o "sem apagar". Se faltar, o
		// painel está desatualizado — o técnico faz o Redeploy antes de qualquer coisa.
		for _, must := range []string{"uab-dryrun", "uab-nopendrive", "PRESERVE"} {
			if !strings.Contains(pejs, must) {
				abort("o painel está desatualizado (instalador sem as proteções de segurança). Faça o Redeploy no Coolify e baixe o gravador de novo antes de formatar.")
				return
			}
		}
		os.MkdirAll(filepath.Join(mnt, "uab"), 0o755)
		if err := os.WriteFile(filepath.Join(mnt, "uab", "pe.js"), []byte(pejs), 0o644); err != nil {
			abort("falha ao gravar o pe.js no WinPE: " + err.Error())
			return
		}
		// run.cmd: roda o wpeinit e o cscript pe.js SEM redirecionar (o pe.js mostra o progresso
		// na tela com WScript.Echo; ele mesmo grava o log em pe.log e o motivo em uab-crash.txt se
		// falhar) e reinicia. winpeshl.ini usa %SYSTEMROOT% (o winpeshl NÃO expande %windir%; com o
		// valor errado o Setup roda no lugar e apagaria o disco).
		runcmd := "@echo off\r\n%SYSTEMROOT%\\System32\\wpeinit.exe\r\n" +
			"%SYSTEMROOT%\\System32\\cscript.exe //nologo //e:jscript %SYSTEMDRIVE%\\uab\\pe.js\r\n" +
			"%SYSTEMROOT%\\System32\\wpeutil.exe reboot\r\n"
		if err := os.WriteFile(filepath.Join(mnt, "uab", "run.cmd"), []byte(runcmd), 0o644); err != nil {
			abort("falha ao gravar o run.cmd no WinPE: " + err.Error())
			return
		}
		winpeshl := "[LaunchApps]\r\n%SYSTEMROOT%\\System32\\cmd.exe, \"/c %SYSTEMDRIVE%\\uab\\run.cmd\"\r\n"
		if err := os.WriteFile(filepath.Join(mnt, "Windows", "System32", "winpeshl.ini"), []byte(winpeshl), 0o644); err != nil {
			abort("falha ao configurar o WinPE (winpeshl): " + err.Error())
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

	// 5. entradas de boot (ramdisk do WinPE pequeno em C:), modeladas na entrada do WinRE.
	// bootsequence vai SEMPRE no {bootmgr} (Windows Boot Manager): a nossa entrada é um osloader
	// do BCD, e é assim que o WinRE faz o boot único — em BIOS e em UEFI. O {fwbootmgr} (UEFI)
	// só aceita entradas de firmware (NVRAM), não um osloader → dava "Elemento não encontrado".
	if p.cancelled() { // painel cancelou: aborta ANTES de tocar no BCD (nada é reiniciado)
		p.set("staging", "Cancelado pelo painel; o Windows atual foi mantido.", 0)
		return
	}
	step("Configurando a inicialização…")
	winload, mgr := `\windows\system32\winload.efi`, "{bootmgr}"
	if !uefiFirmware() {
		winload = `\windows\system32\winload.exe`
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
