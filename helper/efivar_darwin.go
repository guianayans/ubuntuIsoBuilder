//go:build darwin

package main

import (
	"errors"
	"os/exec"
)

// macOS não deixa programas escolherem o próximo boot por uma entrada UEFI própria.
func efiSupported() bool { return fakeEFIDir() != "" }

func efiGet(name string) ([]byte, error) {
	if fakeEFIDir() != "" {
		return fakeEFIGet(name)
	}
	return nil, errors.New("indisponível no macOS")
}

func efiSet(name string, data []byte) error {
	if fakeEFIDir() != "" {
		return fakeEFISet(name, data)
	}
	return errors.New("indisponível no macOS")
}

func osBeforeInstallReboot() {}
func osBootUSBCleanup()      {}
func osRebootNow() error {
	return exec.Command("osascript", "-e", `tell application "System Events" to restart`).Run()
}
