//go:build !linux && !windows

package main

// macOS: instalação sem pendrive não disponível (usa o pendrive).
func osTotalRAMGB() int { return 0 }

func osLocalInstallReason(kind string) string {
	return "indisponível neste sistema"
}

func osInstallLocal(a *app, isoPath, isoURL, unattendPath string, p *progress) {
	p.finish("error", "instalação sem pendrive indisponível neste sistema", "")
}
