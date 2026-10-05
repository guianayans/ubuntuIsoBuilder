//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// diskInstallSupported: precisa de kexec e de RAM suficiente para a ISO caber na memória.
func diskInstallSupported() bool {
	if os.Getenv("UAB_FAKE_DISK") != "" {
		return true
	}
	if !haveKexec() {
		return false
	}
	return memTotalKB() >= 3_500_000 // ~3,3 GiB: a ISO (~2,6 GB) vai para a RAM (url=/toram)
}

func haveKexec() bool {
	for _, p := range []string{"kexec", "/sbin/kexec", "/usr/sbin/kexec"} {
		if _, err := exec.LookPath(p); err == nil {
			return true
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func memTotalKB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "MemTotal:") {
			var kb int64
			fmt.Sscanf(l, "MemTotal: %d kB", &kb)
			return kb
		}
	}
	return 0
}

func kexecBin() string {
	for _, p := range []string{"/sbin/kexec", "/usr/sbin/kexec"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "kexec"
}

// kexecCmdline: a MESMA linha do pendrive (autoinstall + seed embutido em /cdrom/nocloud/),
// mais url= para o casper baixar a ISO do painel e ip=dhcp para a rede subir no instalador.
//
// Vídeo após o kexec: o kexec NÃO passa pela firmware, então NÃO há re-POST da GPU e o efifb
// (framebuffer da UEFI) não sobe. A única fonte de vídeo que resta é o nouveau com KMS — mas
// ele precisa POSTar a placa, coisa que a firmware faria num boot normal. `nomodeset` só piora
// (desliga o nouveau e aí não sobra framebuffer nenhum → tela preta). A solução é mandar o
// nouveau POSTar a GPU sozinho: `nouveau.config=NvForcePost=1`. Com a placa POSTada, o KMS seta
// o modo e a tela volta a mostrar o instalador (e some o travamento por falta de POST).
// `console=tty0` garante que o kernel/instalador escreva no monitor. Afeta SÓ o instalador
// kexec; o sistema instalado e o caminho com pendrive têm cmdline própria e não mudam.
func kexecCmdline(isoURL string) string {
	return "boot=casper nouveau.config=NvForcePost=1 console=tty0 url=" + isoURL +
		" ip=dhcp autoinstall ds=nocloud;s=/cdrom/nocloud/ ---"
}

// osStageKexec carrega o kernel/initrd do instalador (kexec -l). No modo de teste, só
// grava a linha de comando pretendida em UAB_FAKE_KEXEC, sem tocar no kexec real.
func osStageKexec(kernel, initrd, isoURL string) error {
	cmdline := kexecCmdline(isoURL)
	if f := os.Getenv("UAB_FAKE_KEXEC"); f != "" {
		return os.WriteFile(f, []byte(kernel+"\n"+initrd+"\n"+cmdline+"\n"), 0o644)
	}
	if _, err := os.Stat(kernel); err != nil {
		return err
	}
	out, err := exec.Command(kexecBin(), "-l", kernel, "--initrd="+initrd, "--command-line="+cmdline).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[len(msg)-200:]
		}
		return fmt.Errorf("kexec -l falhou: %v %s", err, msg)
	}
	return nil
}

// osKexecReboot reinicia direto no kernel carregado (systemctl kexec faz o desligamento limpo).
func osKexecReboot() error {
	if err := exec.Command("systemctl", "kexec").Run(); err == nil {
		return nil
	}
	return exec.Command(kexecBin(), "-e").Run()
}
