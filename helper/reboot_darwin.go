//go:build darwin

package main

import (
	"os/exec"
	"runtime"
)

func osRebootInfo() rebootInfo {
	if runtime.GOARCH == "arm64" {
		return rebootInfo{Available: false,
			Note: "Macs com Apple Silicon (M1/M2/M3/M4) não conseguem iniciar este instalador: use o pendrive em um PC ou Mac Intel."}
	}
	return rebootInfo{Available: true, How: "O Mac reinicia (os apps abertos são fechados).",
		Note: "Segure a tecla Option (⌥) assim que o Mac ligar e escolha \"EFI Boot\". Em Macs com chip T2, antes permita iniciar por mídia externa no Utilitário de Segurança da Inicialização."}
}

func osRebootToUSB() error {
	return exec.Command("osascript", "-e", `tell application "System Events" to restart`).Run()
}
