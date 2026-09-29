//go:build linux

package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var bootEntryRe = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})\*?\s+(.*)$`)

// usbBootEntry procura uma entrada de boot UEFI do pendrive (quando a placa-mãe a cria).
func usbBootEntry() string {
	if _, err := os.Stat("/sys/firmware/efi"); err != nil {
		return ""
	}
	out, err := exec.Command("efibootmgr").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		m := bootEntryRe.FindStringSubmatch(strings.TrimSpace(l))
		if m != nil && regexp.MustCompile(`(?i)usb|removable`).MatchString(m[2]) {
			return m[1]
		}
	}
	return ""
}

func osRebootInfo() rebootInfo {
	if t, _ := findInstallStick(); t != nil && efiSupported() {
		return rebootInfo{Available: true, How: "O próximo boot vai direto para o pendrive (uma vez só).",
			Note: "Se mesmo assim iniciar o sistema atual, a BIOS bloqueia boot por USB: reinicie apertando a tecla do menu de boot (F12, F11, F8 ou Esc)."}
	}
	if usbBootEntry() != "" {
		return rebootInfo{Available: true, How: "O próximo boot vai direto para o pendrive (uma vez só).",
			Note: "Se mesmo assim iniciar o sistema atual, reinicie apertando a tecla do menu de boot (F12, F11, F8 ou Esc)."}
	}
	return rebootInfo{Available: true, How: "O computador reinicia.",
		Note: "Ao ligar, aperte a tecla do menu de boot (F12, F11, F8 ou Esc) e escolha o pendrive."}
}

func osRebootToUSB() error {
	if t, _ := findInstallStick(); t != nil {
		if _, err := armBootNext(t); err == nil {
			return osRebootNow()
		}
	}
	if n := usbBootEntry(); n != "" {
		_ = exec.Command("efibootmgr", "--bootnext", n).Run()
	}
	return exec.Command("systemctl", "reboot").Run()
}
