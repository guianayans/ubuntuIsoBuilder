package main

// Modo headless usado pelo PAINEL (via agente do Remoto): "Formatar esta máquina (Windows,
// sem pendrive)". O painel manda o agente baixar o gravador personalizado (com o bundle do
// build embutido) e rodar `--auto-format --report <.../f/token/report>`. Aqui o gravador
// baixa a configuração e a ISO (da Microsoft ou da reserva do painel) e instala no disco,
// reportando o andamento ao painel — o mesmo motor da tela "Formatar este computador".

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// mapeia as fases internas do progresso para os estados que o painel entende (format_jobs)
var autoFormatState = map[string]string{
	"config": "baixando", "downloading": "baixando", "hashing": "baixando", "checking": "baixando",
	"staging": "preparando", "rebooting": "reiniciando", "done": "reiniciando", "error": "erro",
}

// runAutoFormat: instala o Windows neste computador sem pendrive, reportando ao painel.
// Saídas: 0 reiniciando/ok, 1 erro.
func runAutoFormat(a *app, reportURL string, dry bool) int {
	rep := &formatReporter{url: reportURL, c: &http.Client{Timeout: 20 * time.Second}}
	if a.bundle == nil {
		rep.send("erro", "gravador sem configuração embutida.", nil)
		return 1
	}
	p := &progress{}
	stop := make(chan struct{})
	go func() { // ponte progresso -> painel
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		lastMsg, lastPct := "", -1
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				st := p.get()
				state := autoFormatState[st.Phase]
				if state == "" {
					state = "preparando"
				}
				pct := -1
				if state == "baixando" && st.Percent > 0 { // só o download tem porcentagem confiável
					pct = int(st.Percent + 0.5)
				}
				// avisa quando a mensagem muda ou a porcentagem anda ao menos 1 ponto
				if st.Message != "" && (st.Message != lastMsg || pct != lastPct) {
					lastMsg, lastPct = st.Message, pct
					if rep.sendProgress(state, st.Message, pct) { // painel cancelou
						return
					}
				}
			}
		}
	}()
	defer close(stop)

	ctx := context.Background()

	if !a.bundle.isWindows() { // build de Ubuntu num PC Windows: UEFI (EFISTUB) ou BIOS (grub4dos)
		b := a.bundle
		base := strings.TrimRight(b.Server, "/") + "/t/" + b.Token
		// a URL da ISO precisa terminar em .iso: o casper só faz netboot automático com url=*.iso
		osInstallUbuntuLocal(a, base+"/kernel", base+"/initrd", base+"/image.iso", base+"/grldr", base+"/grldr.mbr", dry, p)
		st := p.get()
		if st.Phase == "error" {
			rep.send("erro", st.Message, nil)
			return 1
		}
		if dry { // modo teste: usa a mensagem do próprio motor (…não reiniciei; plano em …)
			rep.send("reiniciando", st.Message, nil)
			return 0
		}
		rep.send("reiniciando", "Tudo pronto. Reiniciando para instalar o Ubuntu; o computador volta a aparecer no painel quando terminar.", nil)
		return 0
	}

	rep.send("baixando", "Baixando a configuração do painel…", nil)
	if err := a.loadConfig(); err != nil {
		rep.send("erro", "não consegui baixar a configuração: "+err.Error(), nil)
		return 1
	}
	w := a.bundle.Windows
	path, sum, err := downloadWindowsISO(ctx, w, p)
	if err != nil && a.bundle.WinFallback != "" {
		p.set("downloading", "A Microsoft recusou; baixando a ISO de reserva do painel…", 0)
		if fp, fs, ferr := downloadWinFallback(ctx, a.bundle, p); ferr == nil {
			path, sum, err = fp, fs, nil
		}
	}
	if err != nil {
		rep.send("erro", "não consegui baixar o Windows (Microsoft e reserva do painel): "+err.Error(), nil)
		return 1
	}
	info, err := inspectWindowsISO(path, w.Arch, w.Edition, w.Lang)
	if err != nil {
		rep.send("erro", "a ISO não passou na conferência: "+err.Error(), nil)
		return 1
	}
	a.mu.Lock()
	a.isoPath, a.isoHash, a.winInfo = path, sum, info
	a.mu.Unlock()

	// instala no disco (o osInstallLocal reinicia sozinho no fim; em dry, escreve o plano e volta)
	osInstallLocal(a, path, "", a.unattendPath, dry, p)
	st := p.get()
	if st.Phase == "error" {
		rep.send("erro", st.Message, nil)
		return 1
	}
	if dry { // modo teste: mensagem do próprio motor
		rep.send("reiniciando", st.Message, nil)
		return 0
	}
	rep.send("reiniciando", "Tudo pronto. Reiniciando para instalar o Windows; o computador volta a aparecer no painel quando terminar.", nil)
	return 0
}
