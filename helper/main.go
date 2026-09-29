// isoBuilder (gravador de pendrive) — ubuntu-autoinstall-builder.
//
// Abre uma interface local (http://127.0.0.1:<porta>/<token>/) no navegador,
// encontra a ISO baixada, lista SOMENTE pendrives/discos USB e grava a ISO
// com verificação por releitura.
//
// Modos:
//
//	gravador                                  interface gráfica (padrão)
//	gravador --list                           lista pendrives em JSON
//	gravador --write --iso X --disk ID [--status arquivo.json]   grava (uso interno/avançado)
//
// Teste sem hardware: UAB_FAKE_DISK=/caminho/arquivo.img UAB_FAKE_DISK_SIZE=8000000000
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ui/index.html assets/icon.ico assets/icon.png
var uiFS embed.FS

var version = "dev"

type Disk struct {
	ID     string `json:"id"`
	Path   string `json:"-"`
	Name   string `json:"name"`
	Serial string `json:"serial,omitempty"`
	Size   int64  `json:"size"`
	Bus    string `json:"bus"`
	// Key identifica o MESMO pendrive mesmo que o sistema troque o nome dele
	// (ex.: disk4 -> disk5 no macOS depois de uma falha ou de reconectar).
	Key string `json:"key"`
}

func diskKey(d Disk) string {
	if d.Serial != "" {
		return "s:" + d.Serial + "|" + strconv.FormatInt(d.Size, 10)
	}
	return "m:" + d.Name + "|" + strconv.FormatInt(d.Size, 10) + "|" + d.Bus
}

type ISO struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Expected string `json:"expected,omitempty"` // SHA256 do arquivo .sha256 ao lado, se houver
}

func listDisks() ([]Disk, error) {
	if f := os.Getenv("UAB_FAKE_DISK"); f != "" {
		size, _ := strconv.ParseInt(os.Getenv("UAB_FAKE_DISK_SIZE"), 10, 64)
		if size == 0 {
			size = 8_000_000_000
		}
		id := "fake0"
		if v := os.Getenv("UAB_FAKE_DISK_ID"); v != "" { // testes: simula o sistema renomeando o pendrive
			id = v
		}
		d := Disk{ID: id, Path: f, Name: "Pendrive de teste (arquivo)", Size: size, Bus: "TESTE"}
		d.Key = diskKey(d)
		return []Disk{d}, nil
	}
	disks, err := osListDisks()
	for i := range disks {
		disks[i].Key = diskKey(disks[i])
	}
	return disks, err
}

// findDisk acha o pendrive pelo nome atual; se o sistema o renomeou, acha pela
// identidade (nº de série/modelo + tamanho), desde que só UM pendrive combine.
func findDisk(id, key string) (Disk, error) {
	disks, err := listDisks()
	if err != nil {
		return Disk{}, err
	}
	for _, d := range disks {
		if d.ID == id && (key == "" || d.Key == key) {
			return d, nil
		}
	}
	if key != "" {
		var m []Disk
		for _, d := range disks {
			if d.Key == key {
				m = append(m, d)
			}
		}
		if len(m) == 1 {
			return m[0], nil
		}
		if len(m) > 1 {
			return Disk{}, errors.New("há mais de um pendrive igual conectado; escolha na lista de novo")
		}
	}
	return Disk{}, errors.New("pendrive não encontrado (foi removido?). Reconecte e atualize a lista")
}

func prepareDisk(d Disk) error {
	if os.Getenv("UAB_FAKE_DISK") != "" {
		return nil
	}
	return osPrepareDisk(d)
}

func openDevice(d Disk) (*os.File, error) {
	if os.Getenv("UAB_FAKE_DISK") != "" {
		return os.OpenFile(d.Path, os.O_RDWR|os.O_CREATE, 0o600)
	}
	return osOpenDevice(d)
}

func finalizeDisk(d Disk) {
	if os.Getenv("UAB_FAKE_DISK") == "" {
		osFinalize(d)
	}
}

// runWrite executa a gravação completa em um disco já validado.
// toolsDir: arquivos da partição INSTALAR (vazio = não cria a partição).
func runWrite(isoPath, diskID, toolsDir string, p *progress) {
	d, err := findDisk(diskID, "") // revalida: o disco ainda existe e ainda é USB/externo
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	p.set("preparing", "Preparando o pendrive…", 0)
	if err := prepareDisk(d); err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	f, err := openDevice(d)
	if err != nil {
		p.finish("error", "não foi possível abrir o pendrive: "+err.Error(), "")
		return
	}
	sum, err := writeImage(isoPath, f, d.Size, p)
	if err != nil {
		f.Close()
		p.finish("error", err.Error(), "")
		return
	}
	// ISO gravada e verificada; agora a partição INSTALAR (opcional: se falhar,
	// o pendrive continua bom para instalar pelo menu de boot)
	msg := "Pendrive pronto! Pode removê-lo e usar para instalar o servidor."
	if toolsDir != "" {
		p.set("tools", "Criando a partição INSTALAR (programas para instalar no computador de destino)…", 0)
		files, rerr := readToolsDir(toolsDir)
		if rerr == nil {
			_, rerr = addToolsPartition(f, d.Size, files)
		}
		if rerr == nil {
			rerr = syncDevice(f)
		}
		if rerr != nil {
			msg += " (Sem a partição INSTALAR: " + rerr.Error() + ".)"
		} else {
			msg = "Pendrive pronto! Além do instalador, ele tem a unidade INSTALAR com os programas para reiniciar e instalar no computador de destino."
		}
	}
	f.Close()
	finalizeDisk(d)
	p.finish("done", msg, sum)
}

// ------------------------------------------------------------------ ISOs
func searchDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		dirs = append(dirs, d)
		// dentro de um .app no macOS, procurar também ao lado do .app
		if i := strings.Index(d, ".app/"); i > 0 {
			dirs = append(dirs, filepath.Dir(d[:i+4]))
		}
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	if h, err := os.UserHomeDir(); err == nil {
		for _, s := range []string{"Downloads", "Desktop", "Área de Trabalho", "OneDrive/Desktop", "OneDrive/Área de Trabalho", "Documents", "Documentos"} {
			dirs = append(dirs, filepath.Join(h, s))
		}
	}
	return dirs
}

func readExpected(iso string) string {
	b, err := os.ReadFile(iso + ".sha256")
	if err != nil {
		return ""
	}
	f := strings.Fields(string(b))
	if len(f) > 0 && len(f[0]) == 64 {
		return strings.ToLower(f[0])
	}
	return ""
}

func findISOs() []ISO {
	seen := map[string]bool{}
	out := []ISO{}
	for _, d := range searchDirs() {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".iso") {
				continue
			}
			p := filepath.Join(d, e.Name())
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			fi, err := os.Stat(p) // segue atalhos/links simbólicos
			if err != nil || fi.IsDir() || fi.Size() < 100<<20 {
				continue
			}
			out = append(out, ISO{Path: p, Name: e.Name(), Size: fi.Size(),
				Modified: fi.ModTime().Format("02/01/2006 15:04"), Expected: readExpected(p)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai := strings.Contains(out[i].Name, "autoinstall")
		aj := strings.Contains(out[j].Name, "autoinstall")
		if ai != aj {
			return ai
		}
		return out[i].Modified > out[j].Modified
	})
	return out
}

// ------------------------------------------------------------------ servidor local
type app struct {
	rebootErr string     // falha ao pedir o reinício (mostrada na tela)
	stick     *StickInfo // aberto a partir do pendrive (partição INSTALAR)
	bundle    *Bundle
	fetch     *progress
	token     string
	mu        sync.Mutex
	isoPath   string
	isoHash   string
	hashing   *progress
	job       *progress
	install   *progress // "formatar este computador (sem pendrive)"
	busy      bool
	lastSeen  time.Time
	port      int
	// Windows
	unattend     []byte             // autounattend.xml do painel (conferido)
	unattendPath string             // cópia 0600 numa pasta temporária (para o gravador privilegiado)
	cfgErr       string             // falha ao baixar a configuração
	winInfo      *winISOInfo        // ISO do Windows conferida
	fetchCancel  context.CancelFunc // interrompe o download da Microsoft (ex.: "Já tenho a ISO")
	agent        agentStatus        // acesso remoto do técnico (quando o painel oferece)
}

func (a *app) state() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	disks, derr := listDisks()
	st := map[string]any{
		"os": runtime.GOOS, "version": version, "isos": findISOs(), "disks": disks,
		"iso": a.isoPath, "iso_sha256": a.isoHash, "busy": a.busy,
	}
	if derr != nil {
		st["disk_error"] = derr.Error()
	}
	if a.hashing != nil {
		st["hash_status"] = a.hashing.get()
	}
	if a.job != nil {
		st["job"] = a.job.get()
	}
	st["reboot"] = osRebootInfo()
	st["local_install"] = localInstallInfo(a.bundle, a.isoPath, a.unattend != nil, a.winInfo != nil)
	if a.install != nil {
		st["install"] = a.install.get()
	}
	if a.stick != nil {
		st["stick"] = a.stick
	}
	if a.rebootErr != "" {
		st["reboot_error"] = a.rebootErr
	}
	if a.bundle != nil && a.bundle.Agent != nil {
		ag := a.agent.get()
		st["agent"] = map[string]any{"account": a.bundle.Agent.Account, "state": ag["state"], "message": ag["message"]}
	}
	if a.bundle != nil {
		st["bundle"] = map[string]any{"hostname": a.bundle.Hostname, "iso_name": a.bundle.ISOName,
			"size": a.bundle.Size, "server": a.bundle.Server, "kind": a.bundle.Kind,
			"config_name": a.bundle.ConfigName, "windows": a.bundle.Windows}
		if a.fetch != nil {
			st["fetch"] = a.fetch.get()
		}
		if a.bundle.isWindows() {
			w := map[string]any{"config_ok": a.unattend != nil, "config_error": a.cfgErr}
			if a.winInfo != nil {
				w["iso_info"] = a.winInfo
			}
			if a.isoPath != "" {
				if fi, err := os.Stat(a.isoPath); err == nil {
					w["min_size"] = winStickSize(fi.Size())
				}
			}
			st["win"] = w
		}
	}
	return st
}

// loadConfig baixa o autounattend.xml (build Windows) e guarda uma cópia 0600.
func (a *app) loadConfig() error {
	a.mu.Lock()
	if a.unattend != nil {
		a.mu.Unlock()
		return nil
	}
	b := a.bundle
	a.mu.Unlock()
	data, err := fetchConfig(b)
	if err == nil {
		var dir string
		if dir, err = os.MkdirTemp("", "isobuilder-cfg"); err == nil {
			path := filepath.Join(dir, "autounattend.xml")
			if err = os.WriteFile(path, data, 0o600); err == nil {
				addCleanup(func() { os.RemoveAll(dir) })
				a.mu.Lock()
				a.unattend, a.unattendPath, a.cfgErr = data, path, ""
				a.mu.Unlock()
				return nil
			}
			os.RemoveAll(dir)
		}
	}
	a.mu.Lock()
	a.cfgErr = err.Error()
	a.mu.Unlock()
	return err
}

// startWindowsFetch: configuração do painel + ISO do Windows direto da Microsoft.
func (a *app) startWindowsFetch(p *progress) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.fetchCancel = cancel
	a.mu.Unlock()
	go func() {
		defer cancel()
		p.set("config", "Baixando a configuração do painel…", 0)
		if err := a.loadConfig(); err != nil {
			p.finish("error", err.Error(), "")
			return
		}
		a.mu.Lock()
		manual := a.isoPath != ""
		a.mu.Unlock()
		if manual { // o usuário já escolheu uma ISO própria
			p.finish("done", "Configuração baixada.", "")
			return
		}
		w := a.bundle.Windows
		path, sum, err := downloadWindowsISO(ctx, w, p)
		if err != nil {
			if ctx.Err() != nil {
				p.finish("cancelled", "Download da Microsoft interrompido: usando a ISO escolhida.", "")
				return
			}
			p.finish("error", "Não foi possível baixar o Windows da Microsoft: "+err.Error()+".", "")
			return
		}
		info, err := inspectWindowsISO(path, w.Arch, w.Edition, w.Lang)
		if err != nil {
			p.finish("error", err.Error(), "")
			return
		}
		a.mu.Lock()
		if !a.busy && a.isoPath == "" {
			a.isoPath, a.isoHash, a.winInfo = path, sum, info
			hp := &progress{}
			hp.finish("done", "Windows 11 baixado da Microsoft e conferido ("+info.Edition+", "+info.Lang+").", sum)
			a.hashing = hp
		}
		a.mu.Unlock()
		p.finish("done", "Windows 11 baixado e conferido: "+path, sum)
	}()
}

// startFetch baixa a ISO do servidor (gravador personalizado) e já a seleciona.
func (a *app) startFetch() {
	p := &progress{}
	a.mu.Lock()
	if a.fetch != nil && a.fetch.get().Phase != "error" && a.fetch.get().Phase != "done" && a.fetch.get().Phase != "cancelled" {
		a.mu.Unlock()
		return
	}
	a.fetch = p
	a.mu.Unlock()
	if a.bundle.isWindows() {
		a.startWindowsFetch(p)
		return
	}
	go func() {
		path, err := fetchISO(a.bundle, p)
		if err != nil {
			p.finish("error", err.Error(), "")
			return
		}
		a.mu.Lock()
		if !a.busy {
			a.isoPath, a.isoHash = path, a.bundle.SHA256
			hp := &progress{}
			hp.finish("done", "ISO baixada e conferida (SHA256 confere).", a.bundle.SHA256)
			a.hashing = hp
		}
		a.mu.Unlock()
		p.finish("done", "ISO baixada e conferida: "+path, a.bundle.SHA256)
	}()
}

func serveAsset(w http.ResponseWriter, name, ctype string) {
	b, _ := uiFS.ReadFile(name)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = w.Write(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	prefix := "/" + a.token
	mux.HandleFunc(prefix+"/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := uiFS.ReadFile("ui/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:")
		_, _ = w.Write(b)
	})
	mux.HandleFunc(prefix+"/icon.png", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, "assets/icon.png", "image/png")
	})
	mux.HandleFunc(prefix+"/api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.state())
	})
	mux.HandleFunc(prefix+"/api/select", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Path string }
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil {
			writeJSON(w, 400, map[string]string{"error": "requisição inválida"})
			return
		}
		fi, err := os.Stat(req.Path)
		if err != nil || fi.IsDir() || !strings.HasSuffix(strings.ToLower(req.Path), ".iso") {
			writeJSON(w, 400, map[string]string{"error": "arquivo .iso não encontrado"})
			return
		}
		a.mu.Lock()
		if a.busy {
			a.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "gravação em andamento"})
			return
		}
		a.isoPath, a.isoHash, a.winInfo = req.Path, "", nil
		p := &progress{}
		a.hashing = p
		win := a.bundle.isWindows()
		if win && a.fetchCancel != nil && a.fetch != nil && a.fetch.get().Phase != "done" {
			a.fetchCancel() // "Já tenho a ISO": para o download da Microsoft
		}
		a.mu.Unlock()
		if win {
			go a.selectWindowsISO(req.Path, p)
			writeJSON(w, 200, map[string]string{"ok": "1"})
			return
		}
		go func(path string) {
			sum, err := hashFile(path, p)
			if err != nil {
				p.finish("error", err.Error(), "")
				return
			}
			exp := readExpected(path)
			if exp != "" && exp != sum {
				p.finish("error", "O SHA256 da ISO não confere com o arquivo .sha256: o download está corrompido. Baixe a ISO novamente.", sum)
				return
			}
			msg := "ISO conferida."
			if exp != "" {
				msg = "ISO íntegra: SHA256 confere com o arquivo .sha256."
			}
			a.mu.Lock()
			if a.isoPath == path {
				a.isoHash = sum
			}
			a.mu.Unlock()
			p.finish("done", msg, sum)
		}(req.Path)
		writeJSON(w, 200, map[string]string{"ok": "1"})
	})
	mux.HandleFunc(prefix+"/api/write", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Disk    string `json:"disk"`
			Key     string `json:"key"`
			Confirm bool   `json:"confirm"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || !req.Confirm {
			writeJSON(w, 400, map[string]string{"error": "confirmação necessária"})
			return
		}
		a.mu.Lock()
		if a.busy {
			a.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "gravação em andamento"})
			return
		}
		if a.isoPath == "" || a.isoHash == "" {
			a.mu.Unlock()
			writeJSON(w, 400, map[string]string{"error": "selecione a ISO e aguarde a conferência"})
			return
		}
		d, err := findDisk(req.Disk, req.Key)
		if err != nil {
			a.mu.Unlock()
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		iso := a.isoPath
		win := a.bundle.isWindows()
		unattend := a.unattendPath
		if win && (a.unattend == nil || a.winInfo == nil) {
			a.mu.Unlock()
			writeJSON(w, 400, map[string]string{"error": "aguarde a configuração e a conferência da ISO do Windows"})
			return
		}
		if fi, err := os.Stat(iso); err != nil || (!win && padLen(fi.Size()) > d.Size) || (win && winStickSize(fi.Size()) > d.Size) {
			a.mu.Unlock()
			writeJSON(w, 400, map[string]string{"error": "o pendrive é pequeno demais para esta ISO"})
			return
		}
		p := &progress{}
		a.job, a.busy = p, true
		a.mu.Unlock()
		go func() {
			defer func() { a.mu.Lock(); a.busy = false; a.mu.Unlock() }()
			p.set("preparing", "Preparando os programas para o pendrive…", 0)
			tools, notes, terr := a.gatherTools(iso)
			if terr != nil {
				tools = ""
			}
			if tools != "" {
				defer os.RemoveAll(tools)
			}
			if !win {
				unattend = ""
			}
			switch {
			case osNeedsPrivilegedWorker():
				runViaPrivilegedWorker(iso, d.ID, tools, unattend, p)
			case win:
				runWriteWindows(iso, d.ID, unattend, tools, p)
			default:
				runWrite(iso, d.ID, tools, p)
			}
			if len(notes) > 0 && p.get().Phase == "done" {
				p.setMessage(p.get().Message + " Observação: " + strings.Join(notes, "; ") + ".")
			}
		}()
		writeJSON(w, 200, map[string]string{"ok": "1"})
	})
	// "Tentar de novo": descarta o resultado da gravação anterior (erro ou concluída)
	mux.HandleFunc(prefix+"/api/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "use POST"})
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.busy {
			writeJSON(w, 409, map[string]string{"error": "gravação em andamento"})
			return
		}
		a.job, a.install = nil, nil
		writeJSON(w, 200, map[string]string{"ok": "1"})
	})
	mux.HandleFunc(prefix+"/api/fetch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || a.bundle == nil {
			writeJSON(w, 400, map[string]string{"error": "nada para baixar"})
			return
		}
		a.startFetch()
		writeJSON(w, 200, map[string]string{"ok": "1"})
	})
	mux.HandleFunc(prefix+"/api/agent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || a.bundle == nil || a.bundle.Agent == nil {
			http.Error(w, "indisponível", http.StatusNotFound)
			return
		}
		var req struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
			http.Error(w, "pedido inválido", http.StatusBadRequest)
			return
		}
		if err := a.agentAction(req.Action); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, a.state())
	})
	mux.HandleFunc(prefix+"/api/install", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Confirm string `json:"confirm"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || req.Confirm != "FORMATAR" {
			writeJSON(w, 400, map[string]string{"error": "digite FORMATAR para confirmar"})
			return
		}
		a.mu.Lock()
		li := localInstallInfo(a.bundle, a.isoPath, a.unattend != nil, a.winInfo != nil)
		if !li.Available {
			a.mu.Unlock()
			writeJSON(w, 400, map[string]string{"error": li.Reason})
			return
		}
		if a.busy {
			a.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "já há uma operação em andamento"})
			return
		}
		p := &progress{}
		a.install, a.busy = p, true
		isoPath, unattend := a.isoPath, a.unattendPath
		isoURL := a.panelISOURL()
		a.mu.Unlock()
		go func() {
			defer func() { a.mu.Lock(); a.busy = false; a.mu.Unlock() }()
			osInstallLocal(a, isoPath, isoURL, unattend, p)
		}()
		writeJSON(w, 200, map[string]string{"ok": "1"})
	})
	mux.HandleFunc(prefix+"/api/reboot", a.rebootHandler)
	mux.HandleFunc(prefix+"/api/ventoy", a.ventoyInfoHandler)
	mux.HandleFunc(prefix+"/api/ventoy/copy", a.ventoyCopyHandler)
	mux.HandleFunc(prefix+"/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, nil)
			return
		}
		a.mu.Lock()
		busy := a.busy
		a.mu.Unlock()
		if busy {
			writeJSON(w, 409, map[string]string{"error": "aguarde o fim da gravação"})
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
		go func() { time.Sleep(300 * time.Millisecond); clearInstance(); os.Exit(0) }()
	})

	// Proteções: só aceita Host 127.0.0.1:<porta> (contra DNS rebinding) e
	// POST só com Content-Type JSON vindo da própria origem.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != fmt.Sprintf("127.0.0.1:%d", a.port) {
			http.Error(w, "host inválido", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") ||
				(r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+r.Host) {
				http.Error(w, "origem inválida", http.StatusForbidden)
				return
			}
		}
		if r.URL.Path == "/favicon.ico" { // pedido automático do navegador (fora do token; só o ícone)
			serveAsset(w, "assets/icon.ico", "image/x-icon")
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			http.NotFound(w, r)
			return
		}
		a.mu.Lock()
		a.lastSeen = time.Now()
		a.mu.Unlock()
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}

// macOS: roda a gravação como root via janela de senha nativa e acompanha
// o progresso por um arquivo de status.
func runViaPrivilegedWorker(iso, diskID, toolsDir, unattend string, p *progress) {
	dir, err := os.MkdirTemp("", "isobuilder")
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	defer os.RemoveAll(dir)
	status := filepath.Join(dir, "status.json")
	p.set("preparing", "Aguardando a sua senha do computador…", 0)
	done := make(chan error, 1)
	args := []string{"--write", "--iso", iso, "--disk", diskID, "--status", status}
	if toolsDir != "" {
		args = append(args, "--tools", toolsDir)
	}
	if unattend != "" {
		args = append(args, "--unattend", unattend)
	}
	go func() { done <- osRunPrivileged(args) }()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if b, rerr := os.ReadFile(status); rerr == nil {
				var st Status
				if json.Unmarshal(b, &st) == nil {
					p.mu.Lock()
					p.st = st
					p.mu.Unlock()
				}
			}
			if err != nil && p.get().Phase != "error" {
				p.finish("error", "Gravação não concluída: "+err.Error(), "")
			} else if err == nil && p.get().Phase != "done" && p.get().Phase != "error" {
				p.finish("error", "O processo de gravação terminou sem confirmar o resultado.", "")
			}
			return
		case <-tick.C:
			if b, err := os.ReadFile(status); err == nil {
				var st Status
				if json.Unmarshal(b, &st) == nil {
					p.mu.Lock()
					p.st = st
					p.mu.Unlock()
				}
			}
		}
	}
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	write := flag.Bool("write", false, "grava a ISO no disco (modo trabalhador)")
	list := flag.Bool("list", false, "lista pendrives em JSON")
	iso := flag.String("iso", "", "caminho da ISO")
	disk := flag.String("disk", "", "ID do disco (de --list)")
	status := flag.String("status", "", "arquivo de status JSON")
	toolsDir := flag.String("tools", "", "pasta com os arquivos da partição INSTALAR (modo trabalhador)")
	unattendF := flag.String("unattend", "", "autounattend.xml: grava o pendrive do Windows (modo trabalhador)")
	noBrowser := flag.Bool("no-browser", false, "não abre o navegador")
	port := flag.Int("port", 0, "porta local (0 = automática)")
	showVer := flag.Bool("version", false, "mostra a versão")
	agentInstall := flag.Bool("agent-install", false, "instala o agente do Remoto (modo trabalhador privilegiado)")
	agentURL := flag.String("agent-url", "", "endereço do Remoto (com --agent-install)")
	agentMesh := flag.String("agent-meshid", "", "grupo do Remoto (com --agent-install)")
	bootUSB := flag.Bool("boot-usb", false, "espera o pendrive do isoBuilder e reinicia por ele (botão \"Formatar esta máquina\" do painel)")
	installDisk := flag.Bool("install-disk", false, "formata sem pendrive: kexec no instalador servido pelo painel (Linux)")
	instURL := flag.String("url", "", "com --install-disk: base do pedido no painel (.../f/<token>)")
	instKernelSHA := flag.String("kernel-sha", "", "com --install-disk: SHA256 do kernel")
	instInitrdSHA := flag.String("initrd-sha", "", "com --install-disk: SHA256 do initrd")
	bootWait := flag.Duration("wait", 24*time.Hour, "com --boot-usb/--install-disk: por quanto tempo tentar")
	bootReport := flag.String("report", "", "com --boot-usb/--install-disk: endereço do painel para avisar o andamento")
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}
	if *list {
		d, err := listDisks()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(d)
		return
	}
	if *agentInstall { // trabalhador privilegiado do macOS (a interface pede a senha e chama isto)
		g := &BundleAgent{URL: *agentURL, MeshID: *agentMesh}
		if !safeMeshID.MatchString(g.MeshID) || !strings.HasPrefix(g.URL, "https://") && !(os.Getenv("UAB_BUNDLE_INSECURE") == "1" && strings.HasPrefix(g.URL, "http://127.0.0.1:")) {
			fmt.Fprintln(os.Stderr, "agente: endereço ou grupo inválido")
			os.Exit(2)
		}
		if err := installAgent(g); err != nil {
			fmt.Fprintln(os.Stderr, "agente:", err)
			os.Exit(1)
		}
		return
	}
	if *bootUSB {
		if !validReportURL(*bootReport) {
			fmt.Fprintln(os.Stderr, "--report precisa ser https://")
			os.Exit(2)
		}
		os.Exit(runBootUSB(*bootWait, *bootReport))
	}
	if *installDisk {
		if !validReportURL(*bootReport) || !validReportURL(*instURL) {
			fmt.Fprintln(os.Stderr, "--url e --report precisam ser https://")
			os.Exit(2)
		}
		if !safeHex64(*instKernelSHA) || !safeHex64(*instInitrdSHA) {
			fmt.Fprintln(os.Stderr, "--kernel-sha e --initrd-sha precisam ser SHA256")
			os.Exit(2)
		}
		os.Exit(runInstallDisk(strings.TrimRight(*instURL, "/"), *instKernelSHA, *instInitrdSHA, *bootWait, *bootReport))
	}
	if *write {
		p := &progress{file: *status}
		if *iso == "" || *disk == "" {
			p.finish("error", "--iso e --disk são obrigatórios", "")
			os.Exit(2)
		}
		if *unattendF != "" {
			runWriteWindows(*iso, *disk, *unattendF, *toolsDir, p)
		} else {
			runWrite(*iso, *disk, *toolsDir, p)
		}
		st := p.get()
		fmt.Println(st.Message)
		if st.Phase != "done" {
			os.Exit(1)
		}
		return
	}

	// Já existe um isoBuilder aberto (ex.: a aba foi fechada no meio da gravação)?
	// Reabre a página dele em vez de iniciar outro, que não saberia da gravação.
	if url := runningInstance(); url != "" {
		fmt.Println("isoBuilder já está aberto em:", url)
		if !*noBrowser {
			osOpenBrowser(url)
		}
		return
	}

	relaunched, err := osEnsureElevated()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		os.Exit(1)
	}
	if relaunched {
		return
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		os.Exit(1)
	}
	a := &app{token: randomToken(), lastSeen: time.Now(), port: ln.Addr().(*net.TCPAddr).Port, bundle: loadBundle()}
	// aberto a partir da partição INSTALAR de um pendrive já gravado: só reiniciar e instalar
	if a.stick = detectStick(); a.stick != nil {
		a.bundle = nil
	}
	if a.bundle != nil {
		a.startFetch()
		// acesso remoto do técnico: só do mesmo painel; já instalado = avisa o painel
		if g := a.bundle.Agent; g != nil && !g.valid(a.bundle.Server) {
			a.bundle.Agent = nil
		} else if g != nil {
			go func() {
				if agentInstalled() {
					a.agent.set("ok", "já estava instalado neste computador")
					reportAgent(a.bundle, "ok", "já estava instalado")
				}
			}()
		}
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/%s/", a.port, a.token)
	saveInstance(url)
	fmt.Println("isoBuilder aberto em:", url)
	fmt.Println("(feche esta janela para encerrar)")
	if !*noBrowser {
		osOpenBrowser(url)
	}
	// Encerra sozinho após 30 min sem a página aberta (e sem gravação em curso).
	go func() {
		for range time.Tick(time.Minute) {
			a.mu.Lock()
			idle := time.Since(a.lastSeen) > 30*time.Minute && !a.busy
			a.mu.Unlock()
			if idle {
				clearInstance()
				os.Exit(0)
			}
		}
	}()
	srv := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		os.Exit(1)
	}
}

// ------------------------------------------------------------------ instância única
// Endereço da página do isoBuilder em execução (só do próprio usuário, 0600).
func instanceFile() string {
	if p := os.Getenv("UAB_INSTANCE_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "isoBuilder", "instance")
}

func saveInstance(url string) {
	p := instanceFile()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(url), 0o600)
}

func clearInstance() {
	_ = os.Remove(instanceFile())
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	for _, f := range cleanups {
		f()
	}
	cleanups = nil
}

var (
	cleanupMu sync.Mutex
	cleanups  []func() // arquivos temporários a apagar ao fechar (ex.: a configuração com senha)
)

func addCleanup(f func()) {
	cleanupMu.Lock()
	cleanups = append(cleanups, f)
	cleanupMu.Unlock()
}

// runningInstance devolve o endereço de um isoBuilder que ainda responde, ou "".
func runningInstance() string {
	b, err := os.ReadFile(instanceFile())
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(b))
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		return ""
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url + "api/state")
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	return url
}
