//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func osListDisks() ([]Disk, error) {
	out, err := exec.Command("diskutil", "list", "-plist", "external", "physical").Output()
	if err != nil {
		return nil, fmt.Errorf("diskutil list falhou: %w", err)
	}
	root, err := parsePlist(out)
	if err != nil {
		return nil, err
	}
	var disks []Disk
	whole, _ := root["WholeDisks"].([]any)
	for _, w := range whole {
		id, _ := w.(string)
		if id == "" {
			continue
		}
		info, err := exec.Command("diskutil", "info", "-plist", id).Output()
		if err != nil {
			continue
		}
		m, err := parsePlist(info)
		if err != nil {
			continue
		}
		if b, ok := m["Internal"].(bool); ok && b {
			continue
		}
		if b, ok := m["VirtualOrPhysical"].(string); ok && b == "Virtual" {
			continue
		}
		size, _ := m["TotalSize"].(int64)
		if size == 0 {
			size, _ = m["Size"].(int64)
		}
		name, _ := m["MediaName"].(string)
		bus, _ := m["BusProtocol"].(string)
		disks = append(disks, Disk{
			ID: id, Path: "/dev/r" + id, Size: size, Bus: strings.ToUpper(bus), Name: strings.TrimSpace(name),
		})
	}
	return disks, nil
}

func osPrepareDisk(d Disk) error {
	out, err := exec.Command("diskutil", "unmountDisk", "force", "/dev/"+d.ID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("não foi possível desmontar o pendrive: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func osOpenDevice(d Disk) (*os.File, error) { return os.OpenFile(d.Path, os.O_RDWR, 0) }

func osFinalize(d Disk) { _ = exec.Command("diskutil", "eject", "/dev/"+d.ID).Run() }

func osEnsureElevated() (bool, error) { return false, nil }

func osOpenBrowser(url string) { _ = exec.Command("open", url).Start() }

// No macOS a interface roda como usuário comum; só a gravação roda como root,
// autorizada pela janela de senha nativa do sistema.
func osNeedsPrivilegedWorker() bool { return os.Getenv("UAB_FAKE_DISK") == "" && os.Geteuid() != 0 }

func appleQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func osRunPrivileged(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	parts := []string{"quoted form of " + appleQuote(exe)}
	for _, a := range args {
		parts = append(parts, `" "`, "quoted form of "+appleQuote(a))
	}
	script := "do shell script " + strings.Join(parts, " & ") +
		` with prompt "O Gravador de Pendrive precisa da sua senha para gravar o pendrive." with administrator privileges`
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "-128") {
			return errors.New("autorização cancelada")
		}
		return errors.New(msg)
	}
	return nil
}

