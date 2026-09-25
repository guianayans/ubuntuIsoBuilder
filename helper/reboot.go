package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// "Reiniciar agora": para quem gravou o pendrive NO PRÓPRIO computador que vai
// virar servidor. O instalador apaga o disco interno, então só é aceito com a
// palavra FORMATAR digitada e depois de uma gravação concluída e verificada.
type rebootInfo struct {
	Available bool   `json:"available"`
	How       string `json:"how"`  // o que acontece ao reiniciar
	Note      string `json:"note"` // o que a pessoa precisa fazer
}

func (a *app) rebootHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || req.Confirm != "FORMATAR" {
		writeJSON(w, 400, map[string]string{"error": "digite FORMATAR para confirmar"})
		return
	}
	a.mu.Lock()
	done := (a.job != nil && a.job.get().Phase == "done" && !a.busy) || a.stick != nil
	a.mu.Unlock()
	if !done {
		writeJSON(w, 409, map[string]string{"error": "grave e verifique o pendrive antes de reiniciar"})
		return
	}
	info := osRebootInfo()
	if !info.Available {
		writeJSON(w, 400, map[string]string{"error": info.Note})
		return
	}
	if os.Getenv("UAB_FAKE_DISK") != "" { // testes: nunca reinicia a máquina de verdade
		writeJSON(w, 200, map[string]any{"ok": "1", "dry_run": true, "how": info.How})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": "1", "how": info.How})
	go func() {
		time.Sleep(800 * time.Millisecond) // deixa a resposta chegar ao navegador
		if err := osRebootToUSB(); err != nil {
			a.mu.Lock()
			a.rebootErr = err.Error()
			a.mu.Unlock()
		}
	}()
}
