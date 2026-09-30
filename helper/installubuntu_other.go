//go:build !windows

package main

// Instalar Ubuntu sem pendrive num PC Windows só existe no gravador do Windows (EFISTUB via
// UEFI). Em Linux, o "sem pendrive" do Ubuntu é o kexec (osInstallLocal). Este é só o stub
// para o código compilar nas outras plataformas.
func osInstallUbuntuLocal(a *app, kernelURL, initrdURL, isoURL, grldrURL, grldrMbrURL string, dry bool, p *progress) {
	p.finish("error", "instalar Ubuntu sem pendrive só é suportado pelo gravador do Windows.", "")
}
