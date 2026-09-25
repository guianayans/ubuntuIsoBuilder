//go:build windows

package main

import (
	"os/exec"
	"regexp"
	"strings"
	"syscall"
)

var fwGUID = regexp.MustCompile(`\{[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}`)

func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return c
}

// usbFirmwareEntry procura, entre as opções de boot da placa-mãe (UEFI), uma de
// USB. Quando existe, dá para mandar o próximo boot direto para ela.
func usbFirmwareEntry() string {
	out, err := hidden("bcdedit.exe", "/enum", "firmware").Output()
	if err != nil {
		return ""
	}
	// blocos separados por linha em branco; os rótulos mudam com o idioma do Windows,
	// então só o GUID e a palavra "USB" na descrição importam
	for _, blk := range regexp.MustCompile(`\r?\n\s*\r?\n`).Split(string(out), -1) {
		id := fwGUID.FindString(blk)
		if id != "" && strings.Contains(strings.ToUpper(blk), "USB") {
			return id
		}
	}
	return ""
}

func osRebootInfo() rebootInfo {
	if usbFirmwareEntry() != "" {
		return rebootInfo{Available: true,
			How:  "O computador reinicia direto pelo pendrive (só no próximo boot).",
			Note: "Se mesmo assim o Windows abrir de novo, reinicie apertando a tecla do menu de boot (F12, F11, F8 ou Esc) e escolha o pendrive."}
	}
	return rebootInfo{Available: true,
		How:  "O Windows reinicia na tela azul \"Escolher uma opção\".",
		Note: "Nela, clique em \"Usar um dispositivo\" e escolha o pendrive (EFI USB Device ou o nome dele). Se essa opção não aparecer, reinicie apertando a tecla do menu de boot (F12, F11, F8 ou Esc)."}
}

func osRebootToUSB() error {
	if id := usbFirmwareEntry(); id != "" {
		// próximo boot (uma vez só) direto pela entrada USB da placa-mãe
		if hidden("bcdedit.exe", "/set", "{fwbootmgr}", "bootsequence", id).Run() == nil {
			return hidden("shutdown.exe", "/r", "/t", "5", "/c", "isoBuilder: reiniciando pelo pendrive para instalar").Run()
		}
	}
	// sem entrada USB: menu de inicialização avançada ("Usar um dispositivo")
	return hidden("shutdown.exe", "/r", "/o", "/t", "5", "/c", "isoBuilder: reiniciando para instalar pelo pendrive").Run()
}
