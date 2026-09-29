//go:build !linux

package main

// A instalação sem pendrive a partir do gravador, por enquanto, só está pronta no Linux
// (kexec). No Windows o caminho é o WinPE, que ainda não está disponível por aqui.
func osLocalInstallReason(kind string) string {
	if kind == "windows" {
		return "a instalação do Windows sem pendrive ainda não está pronta; use o pendrive por enquanto"
	}
	return "indisponível neste sistema"
}

func osInstallLocal(a *app, isoPath, isoURL, unattendPath string, p *progress) {
	p.finish("error", "instalação sem pendrive indisponível neste sistema", "")
}
