//go:build !linux

package main

import "errors"

// "Sem pendrive" (kexec) é só para Linux/Ubuntu por enquanto.
func diskInstallSupported() bool { return false }

func osStageKexec(kernel, initrd, isoURL string) error {
	return errors.New("indisponível neste sistema")
}
func osKexecReboot() error { return errors.New("indisponível neste sistema") }
