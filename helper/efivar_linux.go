//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
)

// Variáveis UEFI pelo efivarfs do kernel (4 bytes de atributos + dados).
const efivarsDir = "/sys/firmware/efi/efivars"

func efiSupported() bool {
	if fakeEFIDir() != "" {
		return true
	}
	if _, err := os.Stat("/sys/firmware/efi"); err != nil {
		return false
	}
	if ents, err := os.ReadDir(efivarsDir); err != nil || len(ents) == 0 {
		_ = exec.Command("mount", "-t", "efivarfs", "efivarfs", efivarsDir).Run()
	}
	return true
}

func efiGet(name string) ([]byte, error) {
	if fakeEFIDir() != "" {
		return fakeEFIGet(name)
	}
	b, err := os.ReadFile(efivarsDir + "/" + name + "-" + efiGlobalGUID)
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, errors.New("variável UEFI inválida")
	}
	return b[4:], nil
}

func efiSet(name string, data []byte) error {
	if fakeEFIDir() != "" {
		return fakeEFISet(name, data)
	}
	p := efivarsDir + "/" + name + "-" + efiGlobalGUID
	if _, err := os.Stat(p); err == nil {
		_ = exec.Command("chattr", "-i", p).Run() // o kernel protege as variáveis existentes
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	// uma única escrita: NON_VOLATILE | BOOTSERVICE_ACCESS | RUNTIME_ACCESS + dados
	_, err = f.Write(append([]byte{7, 0, 0, 0}, data...))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func osBeforeInstallReboot() {}
func osBootUSBCleanup()      {}
func osRebootNow() error     { return exec.Command("systemctl", "reboot").Run() }
