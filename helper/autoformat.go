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
// Saídas: 0 reiniciando/ok, 1 erro, 5 cancelado pelo painel (nada foi reiniciado).
func runAutoFormat(a *app, reportURL string, dry bool) int {
	rep := &formatReporter{url: reportURL, c: &http.Client{Timeout: 20 * time.Second}}
	if a.bundle == nil {
		rep.send("erro", "gravador sem configuração embutida.", nil)
		return 1
	}
	p := &progress{}
	// ctx cancelável: quando o painel cancela, abortamos o download em andamento na hora
	// (downloadWindowsISO/Fallback honram ctx) e marcamos p.cancel() para o motor parar
	// ANTES de reiniciar.
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	stop := make(chan struct{})
	go func() { // ponte progresso -> painel
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
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
				msg := st.Message
				if msg == "" {
					msg = "Trabalhando…"
				}
				// Consulta o painel a cada tick (mesmo sem mudança de mensagem) para
				// detectar o cancelamento em até ~3s em QUALQUER fase — inclusive na cópia
				// longa da imagem, que não muda a mensagem. Sem isto, um cancelamento
				// durante a cópia só seria notado tarde demais (após o ponto de não-retorno).
				if rep.sendProgress(state, msg, pct) { // painel cancelou
					p.cancel()
					cancelCtx()
					return
				}
			}
		}
	}()
	defer close(stop)

	if !a.bundle.isWindows() { // build de Ubuntu num PC Windows: UEFI (EFISTUB) ou BIOS (grub4dos)
		b := a.bundle
		base := strings.TrimRight(b.Server, "/") + "/t/" + b.Token
		// a URL da ISO precisa terminar em .iso: o casper só faz netboot automático com url=*.iso
		osInstallUbuntuLocal(a, base+"/kernel", base+"/initrd", base+"/image.iso", base+"/grldr", base+"/grldr.mbr", dry, p)
		if p.cancelled() { // painel cancelou antes do reboot: o motor não reiniciou
			return 5
		}
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

	if rep.send("baixando", "Baixando a configuração do painel…", nil) {
		return 5
	}
	if err := a.loadConfig(); err != nil {
		rep.send("erro", "não consegui baixar a configuração: "+err.Error(), nil)
		return 1
	}
	if p.cancelled() {
		return 5
	}
	w := a.bundle.Windows
	path, sum, err := downloadWindowsISO(ctx, w, p)
	if err != nil && a.bundle.WinFallback != "" {
		p.set("downloading", "A Microsoft recusou; baixando a ISO de reserva do painel…", 0)
		if fp, fs, ferr := downloadWinFallback(ctx, a.bundle, p); ferr == nil {
			path, sum, err = fp, fs, nil
		}
	}
	if p.cancelled() { // cancelado durante o download: o ctx abortou o download
		return 5
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
	if p.cancelled() { // painel cancelou antes do reboot: o motor não reiniciou
		return 5
	}
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
