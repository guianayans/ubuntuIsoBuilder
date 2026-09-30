//go:build windows

package main

// "Formatar este computador (sem pendrive)" instalando UBUNTU num PC que hoje roda Windows.
//
// Como não há kexec no Windows, o gravador reserva uma partição FAT32 pequena no disco, baixa
// o kernel (vmlinuz, que é uma aplicação EFI — EFISTUB) e o initrd do Ubuntu do painel, cria
// uma entrada de boot UEFI apontando direto para o vmlinuz (com a MESMA linha de comando do
// kexec: boot=casper url=<painel>/…/iso autoinstall) e marca BootNext (boot de UMA vez só).
// Ao reiniciar, o firmware carrega o kernel, o EFISTUB carrega o initrd, o casper baixa a ISO
// do painel para a RAM e o autoinstall apaga o disco e instala — sem pendrive e sem ninguém.
//
// Requisitos: UEFI (BIOS não dá — use pendrive) e Secure Boot DESLIGADO (o kernel do Ubuntu,
// bootado direto pelo EFISTUB, não passa pelo shim assinado). ~4 GB de RAM e rede.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const ubuntuBootDesc = "isoBuilder - Instalar Ubuntu"

// secureBootOn: estado do Secure Boot (true = ligado). Confirm-SecureBootUEFI lança em BIOS/CSM.
func secureBootOn() bool {
	out, _ := psOut("$v = try { if (Confirm-SecureBootUEFI) {1} else {0} } catch {0}; $v")
	return strings.TrimSpace(lastNonEmpty(out)) == "1"
}

// ubuntuCmdline: a mesma linha do kexec (autoinstall + seed em /cdrom/nocloud/ + url= para o
// casper baixar a ISO do painel). No UEFI/EFISTUB é prefixada com "initrd=\initrd"; no grub4dos
// o initrd vai numa linha própria (initrd …).
func ubuntuCmdline(isoURL string) string {
	return "boot=casper url=" + isoURL + " ip=dhcp autoinstall ds=nocloud;s=/cdrom/nocloud/ ---"
}

// linuxBootPartition: reaproveita a partição FAT32 "UABLINUX" se já existir; senão encolhe o
// volume do sistema e cria uma nova (300 MB). Devolve a letra e o alvo UEFI (GUID/offset/…).
func linuxBootPartition() (string, *usbTarget, error) {
	const label = "UABLINUX"
	sysLetter := strings.TrimSuffix(os.Getenv("SystemDrive"), ":")
	if sysLetter == "" {
		sysLetter = "C"
	}
	letter, _ := psOut("(Get-Volume -FileSystemLabel '" + label + "' -ErrorAction SilentlyContinue).DriveLetter")
	letter = strings.TrimSpace(lastNonEmpty(letter))
	if len(letter) != 1 {
		script := strings.Join([]string{
			"$d = (Get-Partition -DriveLetter " + sysLetter + ").DiskNumber",
			"$s = Get-PartitionSupportedSize -DriveLetter " + sysLetter,
			"$cur = (Get-Partition -DriveLetter " + sysLetter + ").Size",
			"$new = $cur - 400MB",
			"if ($new -lt $s.SizeMin) { Write-Error 'sem espaco para encolher'; exit 1 }",
			"Resize-Partition -DriveLetter " + sysLetter + " -Size $new",
			"$p = New-Partition -DiskNumber $d -Size 300MB",
			"Format-Volume -Partition $p -FileSystem FAT32 -NewFileSystemLabel '" + label + "' -Confirm:$false | Out-Null",
			"$p | Add-PartitionAccessPath -AssignDriveLetter",
			"(Get-Volume -FileSystemLabel '" + label + "').DriveLetter",
		}, "; ")
		out, err := psOut(script)
		letter = strings.TrimSpace(lastNonEmpty(out))
		if err != nil || len(letter) != 1 {
			return "", nil, fmt.Errorf("não consegui reservar a partição de boot: %s", out)
		}
	}
	t, err := partitionTarget(letter)
	if err != nil {
		return "", nil, err
	}
	return letter + ":", t, nil
}

// partitionTarget: lê número, offset, tamanho e GUID (GPT) de uma partição pela letra e monta
// o alvo UEFI (usbTarget) que o efiLoadOption usa no caminho HD().
func partitionTarget(letter string) (*usbTarget, error) {
	script := "$p = Get-Partition -DriveLetter " + letter + "; " +
		"if (-not $p.Guid) { Write-Error 'partição não é GPT (o PC precisa ser UEFI)'; exit 1 }; " +
		"$g = ([guid]$p.Guid).ToByteArray(); " +
		"\"$($p.PartitionNumber)`n$($p.Offset)`n$($p.Size)`n$(($g|%{$_.ToString('x2')}) -join '')\""
	out, err := psOut(script)
	if err != nil {
		return nil, fmt.Errorf("não consegui ler a partição de boot: %s", out)
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(out), "\r", ""), "\n")
	if len(lines) < 4 {
		return nil, fmt.Errorf("dados da partição incompletos: %s", out)
	}
	num, _ := strconv.ParseUint(strings.TrimSpace(lines[0]), 10, 32)
	off, _ := strconv.ParseUint(strings.TrimSpace(lines[1]), 10, 64)
	sz, _ := strconv.ParseUint(strings.TrimSpace(lines[2]), 10, 64)
	hexsig := strings.TrimSpace(lines[len(lines)-1])
	if num == 0 || off == 0 || sz == 0 || len(hexsig) != 32 {
		return nil, fmt.Errorf("dados da partição inválidos: %s", out)
	}
	t := &usbTarget{PartNum: uint32(num), Start: off / 512, Size: sz / 512, MBR: false}
	for i := 0; i < 16; i++ {
		b, e := strconv.ParseUint(hexsig[2*i:2*i+2], 16, 8)
		if e != nil {
			return nil, fmt.Errorf("GUID da partição inválido: %s", hexsig)
		}
		t.Sig[i] = byte(b)
	}
	return t, nil
}

// downloadTo baixa uma URL para dest, mostrando o progresso.
func downloadTo(ctx context.Context, url, dest, what string, p *progress) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("servidor respondeu %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	p.set("downloading", "Baixando "+what+"…", resp.ContentLength)
	_, cerr := io.CopyBuffer(counter{f, p}, resp.Body, make([]byte, 1<<20))
	if e := f.Close(); e != nil && cerr == nil {
		cerr = e
	}
	return cerr
}

// osInstallUbuntuLocal instala o Ubuntu neste PC Windows sem pendrive (UEFI/EFISTUB).
func osInstallUbuntuLocal(a *app, kernelURL, initrdURL, isoURL, grldrURL, grldrMbrURL string, dry bool, p *progress) {
	fail := func(msg string) { p.finish("error", msg, "") }
	if osTotalRAMGB() > 0 && osTotalRAMGB() < 4 {
		fail("Este PC tem menos de 4 GB de RAM: a ISO do Ubuntu é carregada na memória. Use a opção com pendrive.")
		return
	}
	if uefiFirmware() { // UEFI: entrada de boot EFISTUB + BootNext (reversível, boot de uma vez)
		if secureBootOn() {
			fail("Secure Boot está ligado: desligue-o na BIOS (Setup) e tente de novo, ou use a opção com pendrive.")
			return
		}
		ubuntuInstallUEFI(kernelURL, initrdURL, isoURL, dry, p)
		return
	}
	// BIOS/Legacy: grub4dos encadeado pelo bootmgr do Windows (bootsequence = boot de uma vez).
	ubuntuInstallBIOS(kernelURL, initrdURL, isoURL, grldrURL, grldrMbrURL, dry, p)
}

// ubuntuInstallUEFI: reserva uma partição FAT32, baixa kernel/initrd e cria uma entrada UEFI
// EFISTUB (com a cmdline) + BootNext. Instala em GPT/UEFI.
func ubuntuInstallUEFI(kernelURL, initrdURL, isoURL string, dry bool, p *progress) {
	fail := func(msg string) { p.finish("error", msg, "") }
	step := func(msg string) { p.set("staging", msg, 0) }

	step("Reservando espaço no disco…")
	letter, target, err := linuxBootPartition()
	if err != nil {
		fail(err.Error())
		return
	}
	if err := downloadTo(context.Background(), kernelURL, letter+`\vmlinuz`, "o instalador do Ubuntu (kernel)", p); err != nil {
		fail("não consegui baixar o kernel do Ubuntu: " + err.Error())
		return
	}
	if err := downloadTo(context.Background(), initrdURL, letter+`\initrd`, "o instalador do Ubuntu (initrd)", p); err != nil {
		fail("não consegui baixar o initrd do Ubuntu: " + err.Error())
		return
	}

	step("Configurando a inicialização…")
	cmdline := "initrd=\\initrd " + ubuntuCmdline(isoURL)
	optional := make([]byte, 0, 2*len(cmdline)+2)
	for _, c := range utf16.Encode([]rune(cmdline + "\x00")) {
		optional = append(optional, byte(c), byte(c>>8))
	}
	lo := efiLoadOptionData(ubuntuBootDesc, target, `\vmlinuz`, optional)

	if dry {
		plan := "=== ISO Builder: PLANO (UEFI, modo teste, nada foi reiniciado) ===\r\n" +
			"partição de boot: " + letter + " (GPT nº " + fmt.Sprint(target.PartNum) + ")\r\n" +
			"kernel: " + letter + "\\vmlinuz\r\ninitrd: " + letter + "\\initrd\r\n" +
			"linha de comando: " + cmdline + "\r\n"
		_ = os.WriteFile(letter+`\uab-plan.txt`, []byte(plan), 0o644)
		p.finish("done", "Modo teste (UEFI): kernel/initrd baixados e a entrada de boot montada (não reiniciei). Plano em "+letter+"\\uab-plan.txt.", "")
		return
	}
	if _, err := armBootNextLO(ubuntuBootDesc, lo); err != nil {
		fail("a placa-mãe não aceitou a entrada de boot do Ubuntu: " + err.Error())
		return
	}
	ubuntuReboot(p)
}

// ubuntuInstallBIOS: baixa o grub4dos (grldr/grldr.mbr) e o kernel/initrd para C:, escreve o
// menu.lst e cria uma entrada BCD "bootsector" apontando para o grldr.mbr, disparada UMA vez
// (bootsequence). O MBR não é tocado: se o grldr falhar, o próximo boot volta ao Windows.
func ubuntuInstallBIOS(kernelURL, initrdURL, isoURL, grldrURL, grldrMbrURL string, dry bool, p *progress) {
	fail := func(msg string) { p.finish("error", msg, "") }
	step := func(msg string) { p.set("staging", msg, 0) }
	ctx := context.Background()
	sysDrive := os.Getenv("SystemDrive")
	if sysDrive == "" {
		sysDrive = "C:"
	}
	dir := sysDrive + `\uab-linux`
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("não consegui criar a pasta de trabalho: " + err.Error())
		return
	}

	step("Baixando o carregador (grub4dos)…")
	if err := downloadTo(ctx, grldrURL, sysDrive+`\grldr`, "o carregador", p); err != nil {
		fail("não consegui baixar o grldr (grub4dos) do painel: " + err.Error())
		return
	}
	if err := downloadTo(ctx, grldrMbrURL, sysDrive+`\grldr.mbr`, "o carregador (mbr)", p); err != nil {
		fail("não consegui baixar o grldr.mbr do painel: " + err.Error())
		return
	}
	if err := downloadTo(ctx, kernelURL, dir+`\vmlinuz`, "o instalador do Ubuntu (kernel)", p); err != nil {
		fail("não consegui baixar o kernel do Ubuntu: " + err.Error())
		return
	}
	if err := downloadTo(ctx, initrdURL, dir+`\initrd`, "o instalador do Ubuntu (initrd)", p); err != nil {
		fail("não consegui baixar o initrd do Ubuntu: " + err.Error())
		return
	}

	menu := "timeout 3\r\ndefault 0\r\ntitle Instalar Ubuntu (ISO Builder)\r\n" +
		"find --set-root /uab-linux/vmlinuz\r\n" +
		"kernel /uab-linux/vmlinuz " + ubuntuCmdline(isoURL) + "\r\n" +
		"initrd /uab-linux/initrd\r\nboot\r\n"
	if err := os.WriteFile(sysDrive+`\menu.lst`, []byte(menu), 0o644); err != nil {
		fail("não consegui gravar o menu.lst: " + err.Error())
		return
	}

	if dry {
		_ = os.WriteFile(dir+`\uab-plan.txt`, []byte("=== ISO Builder: PLANO (BIOS/grub4dos, modo teste) ===\r\n\r\nmenu.lst:\r\n"+menu), 0o644)
		p.finish("done", "Modo teste (BIOS): grub4dos + kernel/initrd baixados e menu.lst montado (não reiniciei). Plano em "+dir+"\\uab-plan.txt.", "")
		return
	}

	step("Configurando a inicialização…")
	bcd("/export", sysDrive+`\uab-bcd-backup`)
	out, err := bcd("/create", "/d", "isoBuilder - Instalar Ubuntu", "/application", "bootsector")
	id := reBCDGUID.FindString(out)
	if err != nil || id == "" {
		fail("a inicialização não aceitou a entrada do Ubuntu: " + out)
		return
	}
	for _, c := range [][]string{
		{"/set", id, "device", "partition=" + sysDrive},
		{"/set", id, "path", `\grldr.mbr`},
	} {
		if o, e := bcd(c...); e != nil {
			fail("falha ao configurar a inicialização: " + o)
			return
		}
	}
	if o, e := bcd("/set", "{bootmgr}", "bootsequence", id); e != nil {
		fail("falha ao marcar o próximo boot: " + o)
		return
	}
	ubuntuReboot(p)
}

func ubuntuReboot(p *progress) {
	osBeforeInstallReboot()
	p.set("rebooting", "Tudo pronto. Reiniciando para instalar o Ubuntu; o computador volta a aparecer no painel quando terminar.", 0)
	time.Sleep(1500 * time.Millisecond)
	if err := exec.Command("shutdown.exe", "/r", "/t", "5", "/c", "isoBuilder: reiniciando para instalar o Ubuntu").Run(); err != nil {
		p.finish("error", "não consegui reiniciar: "+err.Error(), "")
		return
	}
	p.finish("done", "Reiniciando para instalar o Ubuntu…", "")
}
