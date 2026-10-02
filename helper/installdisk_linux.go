//go:build linux

package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
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
// Com toram, o casper copia a imagem para a RAM e NÃO baixa de novo após o switch_root (a
// mídia some junto com o initramfs); sem toram, ele baixa duas vezes (uma por estágio).
func kexecCmdline(isoURL string, toram bool) string {
	extra := ""
	if toram {
		extra = " toram"
	}
	return "boot=casper url=" + isoURL + extra + " ip=dhcp autoinstall ds=nocloud;s=/cdrom/nocloud/ ---"
}

// isoContentLength: tamanho da ISO pelo cabeçalho (HEAD). 0 se não der para saber.
func isoContentLength(url string) int64 {
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Head(url)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 || resp.ContentLength <= 0 {
		return 0
	}
	return resp.ContentLength
}

// useToram: só usa toram se a RAM comportar com folga. Pico do toram ≈ ISO baixada + cópia do
// squashfs (~a própria ISO) + sistema → exijo RAM ≥ 2×ISO + 1 GiB. Sem o tamanho da ISO (HEAD
// falhou), NÃO arrisca (cai no comportamento atual: duas baixadas, menos RAM).
func useToram(isoURL string) bool {
	sz := isoContentLength(isoURL)
	if sz <= 0 {
		return false
	}
	needKB := (sz*2)/1024 + (1 << 20) // 2×ISO + 1 GiB, em KB
	return memTotalKB() >= needKB
}

// osStageKexec carrega o kernel/initrd do instalador (kexec -l). No modo de teste, só
// grava a linha de comando pretendida em UAB_FAKE_KEXEC, sem tocar no kexec real.
func osStageKexec(kernel, initrd, isoURL string) error {
	toram := false
	if os.Getenv("UAB_FAKE_KEXEC") == "" { // real: decide toram pela RAM vs tamanho da ISO
		toram = useToram(isoURL)
	}
	cmdline := kexecCmdline(isoURL, toram)
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
