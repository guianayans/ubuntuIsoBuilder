package main

// "Formatar este computador (sem pendrive)" a partir do próprio gravador: o técnico,
// conectado remotamente, abre o gravador no site e manda instalar direto neste PC, sem
// mídia. No Linux usa kexec; no Windows usa um WinPE com a imagem embutida (ramdisk).
// Funciona igual em UEFI e BIOS antiga. O motivo aparece na tela quando indisponível.

import (
	"fmt"
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
// isoSize: tamanho da ISO baixada (0 se ainda não sabe), para calcular a RAM necessária.
func localInstallInfo(b *Bundle, isoPath string, isoSize int64, hasUnattend, hasWinInfo bool) localInstall {
	li := localInstall{}
	if b == nil {
		li.Reason = "abra o gravador pelo link do site para usar esta opção"
		return li
	}
	if b.isWindows() {
		li.Kind = "windows"
		if runtime.GOOS != "windows" {
			li.Reason = "a instalação do Windows sem pendrive só funciona a partir de um Windows"
			return li
		}
		if isoPath == "" || !hasUnattend || !hasWinInfo {
			li.Reason = "aguarde o download e a conferência do Windows"
			return li
		}
	} else {
		li.Kind = "ubuntu"
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
	li.NeedRAMGB = neededRAMGB(li.Kind, isoSize)
	// A instalação sem pendrive roda o instalador NA MEMÓRIA (Windows: a imagem inteira vai
	// para a RAM; Ubuntu: a ISO baixada para a RAM). Sem RAM suficiente, some a opção.
	if have := osTotalRAMGB(); have > 0 && have < li.NeedRAMGB {
		li.Reason = fmt.Sprintf("este computador tem %d GB de memória (RAM); a instalação sem pendrive precisa de ~%d GB. Use o pendrive.", have, li.NeedRAMGB)
		return li
	}
	if r := osLocalInstallReason(li.Kind); r != "" {
		li.Reason = r
		return li
	}
	li.Available = true
	return li
}

// neededRAMGB: RAM recomendada. Windows carrega o WinPE + a imagem do Windows na RAM
// (≈ tamanho da ISO + folga); Ubuntu carrega a ISO baixada na RAM.
func neededRAMGB(kind string, isoSize int64) int {
	gb := int((isoSize + (1<<30 - 1)) / (1 << 30))
	if kind == "windows" {
		if n := gb + 3; n > 8 {
			return n
		}
		return 8
	}
	if n := gb + 2; n > 4 {
		return n
	}
	return 4
}

// panelISOURL: endereço da ISO no painel (o kexec baixa por ele durante a instalação).
func (a *app) panelISOURL() string {
	return strings.TrimRight(a.bundle.Server, "/") + "/t/" + a.bundle.Token + "/iso"
}
