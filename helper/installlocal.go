package main

// "Formatar este computador (sem pendrive)" a partir do próprio gravador: o técnico,
// conectado remotamente, abre o gravador no site e manda instalar direto neste PC, sem
// mídia. No Linux usa kexec (funciona igual em UEFI e BIOS antiga). No Windows é mais
// complexo (WinPE) e ainda não está disponível por aqui — o motivo aparece na tela.

import (
	"runtime"
	"strings"
)

// localInstall descreve se dá para instalar neste computador sem pendrive.
type localInstall struct {
	Available bool   `json:"available"`
	Kind      string `json:"kind"`        // "ubuntu" | "windows"
	Reason    string `json:"reason"`      // por que não, quando indisponível
	NeedRAMGB int    `json:"need_ram_gb"` // RAM recomendada
}

// localInstallInfo NÃO trava a.mu: o chamador passa os campos (state() já segura o lock).
func localInstallInfo(b *Bundle, isoPath string, hasUnattend, hasWinInfo bool) localInstall {
	li := localInstall{}
	if b == nil {
		li.Reason = "abra o gravador pelo link do site para usar esta opção"
		return li
	}
	if b.isWindows() {
		li.Kind, li.NeedRAMGB = "windows", 8
		if runtime.GOOS != "windows" {
			li.Reason = "a instalação do Windows sem pendrive só funciona a partir de um Windows"
			return li
		}
		if isoPath == "" || !hasUnattend || !hasWinInfo {
			li.Reason = "aguarde o download e a conferência do Windows"
			return li
		}
	} else {
		li.Kind, li.NeedRAMGB = "ubuntu", 4
		if runtime.GOOS != "linux" {
			li.Reason = "a instalação do Ubuntu sem pendrive só funciona a partir de um Linux"
			return li
		}
		if isoPath == "" {
			li.Reason = "aguarde o download e a conferência da ISO"
			return li
		}
		if b.Server == "" || b.Token == "" {
			li.Reason = "sem endereço da ISO no painel"
			return li
		}
	}
	if r := osLocalInstallReason(li.Kind); r != "" {
		li.Reason = r
		return li
	}
	li.Available = true
	return li
}

// panelISOURL: endereço da ISO no painel (o kexec baixa por ele durante a instalação).
func (a *app) panelISOURL() string {
	return strings.TrimRight(a.bundle.Server, "/") + "/t/" + a.bundle.Token + "/iso"
}
