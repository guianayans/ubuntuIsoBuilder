//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func osLocalInstallReason(kind string) string {
	if os.Getenv("UAB_FAKE_DISK") != "" {
		return ""
	}
	if kind != "ubuntu" {
		return "só Ubuntu/Linux por aqui"
	}
	if !haveKexec() {
		return "instale o kexec-tools neste computador (sudo apt install kexec-tools)"
	}
	if memTotalKB() < 3_500_000 {
		return "este computador precisa de ~4 GB de RAM para instalar sem pendrive"
	}
	return ""
}

// extractCasper monta a ISO (loop, só leitura) e copia o kernel e o initrd do /casper
// para dir. Não precisa manter a ISO montada depois (o kexec -l lê os arquivos na hora).
func extractCasper(isoPath, dir string) (string, string, error) {
	mnt := filepath.Join(dir, "iso")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return "", "", err
	}
	if out, err := exec.Command("mount", "-o", "loop,ro", isoPath, mnt).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("não consegui abrir a ISO (%v): %s", err, out)
	}
	defer exec.Command("umount", mnt).Run()
	kern, init := filepath.Join(dir, "vmlinuz"), filepath.Join(dir, "initrd")
	for _, c := range []struct{ src, dst string }{
		{filepath.Join(mnt, "casper", "vmlinuz"), kern},
		{filepath.Join(mnt, "casper", "initrd"), init},
	} {
		b, err := os.ReadFile(c.src)
		if err != nil {
			return "", "", fmt.Errorf("a ISO não tem %s (ISO não suportada)", filepath.Base(c.src))
		}
		if err := os.WriteFile(c.dst, b, 0o600); err != nil {
			return "", "", err
		}
	}
	return kern, init, nil
}

// osInstallLocal (Linux): kexec para o instalador, que baixa a ISO do painel para a RAM
// (url=) e roda o autoinstall, apagando o disco. Não depende da firmware.
func osInstallLocal(a *app, isoPath, isoURL, unattendPath string, p *progress) {
	p.set("staging", "Preparando o instalador…", 0)
	dir, err := os.MkdirTemp("", "uab-inst-")
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	defer os.RemoveAll(dir)
	kern, init, err := extractCasper(isoPath, dir)
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	if err := osStageKexec(kern, init, isoURL); err != nil {
		p.finish("error", "não consegui preparar o kexec: "+err.Error(), "")
		return
	}
	p.set("rebooting", "Reiniciando para instalar. A instalação começa sozinha; o computador volta a aparecer no painel quando terminar.", 0)
	if os.Getenv("UAB_FAKE_DISK") != "" {
		p.finish("done", "(teste) kexec preparado; nada foi reiniciado.", "")
		return
	}
	time.Sleep(1500 * time.Millisecond) // deixa a resposta chegar à tela
	if err := osKexecReboot(); err != nil {
		p.finish("error", "não consegui reiniciar pelo kexec: "+err.Error(), "")
		return
	}
	p.finish("done", "Reiniciando para instalar…", "")
}
