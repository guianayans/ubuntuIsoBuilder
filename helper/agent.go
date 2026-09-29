package main

// Acesso remoto do técnico: o painel pode incluir no gravador da conta do cliente o
// agente do Remoto (MeshCentral). Com a PERMISSÃO EXPLÍCITA da pessoa na tela, o
// gravador baixa o agente do sistema dela, instala como serviço e avisa o painel,
// que passa a mostrar "PC conectado" na conta. Sem o "sim", nada é instalado.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type BundleAgent struct {
	URL     string `json:"url"`     // ex.: https://iso.gvtserver.online/remoto
	MeshID  string `json:"meshid"`  // grupo do Remoto onde o PC entra
	Account string `json:"account"` // conta do cliente (só para mostrar)
}

var safeMeshID = regexp.MustCompile(`^[A-Za-z0-9@$_-]{20,120}$`)

// valid: o agente só vem do mesmo painel que gerou o gravador (nunca de outro site).
func (g *BundleAgent) valid(server string) bool {
	if g == nil || !safeMeshID.MatchString(g.MeshID) {
		return false
	}
	u, err := url.Parse(g.URL)
	s, err2 := url.Parse(server)
	return err == nil && err2 == nil && u.Scheme == s.Scheme && u.Host == s.Host &&
		strings.HasPrefix(u.Path, "/") && !strings.Contains(u.Path, "..") && u.RawQuery == ""
}

// Endereços do agente conforme o sistema (ids do MeshCentral: 4 = Windows x64,
// 43 = Windows ARM64, 10005 = macOS universal; Linux usa o script oficial).
func agentURLs(g *BundleAgent, goos, goarch string) (bin, msh, script string) {
	base := strings.TrimRight(g.URL, "/")
	mid := url.QueryEscape(g.MeshID)
	switch goos {
	case "windows":
		id := 4
		if goarch == "arm64" {
			id = 43
		}
		bin = fmt.Sprintf("%s/meshagents?id=%d&meshid=%s&installflags=0", base, id, mid)
	case "darwin":
		bin = base + "/meshagents?id=10005"
		msh = base + "/meshsettings?id=" + mid
	default:
		script = base + "/meshagents?script=1"
	}
	return
}

var agentHTTP = &http.Client{Timeout: 10 * time.Minute}

func download(u, dst string, min int64) error {
	resp, err := agentHTTP.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("o Remoto respondeu %d ao baixar o agente", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, 200<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n < min {
		err = fmt.Errorf("o Remoto devolveu um arquivo pequeno demais (%d bytes)", n)
	}
	return err
}

// Modo de teste (disco falso): nunca instala nada de verdade nesta máquina.
// UAB_FAKE_AGENT=ok simula sucesso; qualquer outro valor simula falha.
func fakeAgent() (bool, error) {
	if os.Getenv("UAB_FAKE_DISK") == "" {
		return false, nil
	}
	if os.Getenv("UAB_FAKE_AGENT") == "ok" {
		return true, nil
	}
	return true, errors.New("modo de teste: o agente não foi instalado")
}

// agentInstalled: o serviço do agente já está rodando neste computador?
func agentInstalled() bool {
	if os.Getenv("UAB_FAKE_DISK") != "" {
		return false // teste: não olha os serviços da máquina real
	}
	switch runtime.GOOS {
	case "windows":
		out, _ := exec.Command("sc", "query", "Mesh Agent").CombinedOutput()
		return bytes.Contains(out, []byte("RUNNING"))
	case "darwin":
		out, _ := exec.Command("launchctl", "list").CombinedOutput()
		return bytes.Contains(bytes.ToLower(out), []byte("meshagent"))
	default:
		return exec.Command("systemctl", "is-active", "--quiet", "meshagent").Run() == nil
	}
}

// installAgent baixa e instala o agente (precisa de administrador/root).
func installAgent(g *BundleAgent) error {
	if fake, err := fakeAgent(); fake {
		return err
	}
	if agentInstalled() {
		return nil
	}
	dir, err := os.MkdirTemp("", "uab-agent-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	bin, msh, script := agentURLs(g, runtime.GOOS, runtime.GOARCH)
	var name string
	var args []string
	switch runtime.GOOS {
	case "windows":
		exe := filepath.Join(dir, "meshagent.exe")
		if err := download(bin, exe, 100000); err != nil {
			return err
		}
		if b, _ := os.ReadFile(exe); len(b) < 2 || b[0] != 'M' || b[1] != 'Z' {
			return errors.New("o Remoto não devolveu um programa do Windows")
		}
		name, args = exe, []string{"-fullinstall"}
	case "darwin":
		ag := filepath.Join(dir, "meshagent")
		if err := download(bin, ag, 100000); err != nil {
			return err
		}
		if err := download(msh, filepath.Join(dir, "meshagent.msh"), 50); err != nil {
			return err
		}
		name, args = ag, []string{"-fullinstall", "--copy-msh=1"}
	default:
		sh := filepath.Join(dir, "meshinstall.sh")
		if err := download(script, sh, 200); err != nil {
			return err
		}
		name, args = "bash", []string{sh, strings.TrimRight(g.URL, "/"), g.MeshID}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil && !agentInstalled() {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("o instalador do agente falhou (%v): %s", err, msg)
	}
	for i := 0; i < 60 && !agentInstalled(); i++ {
		time.Sleep(2 * time.Second)
	}
	if !agentInstalled() {
		return errors.New("o agente foi instalado, mas o serviço não ficou ativo")
	}
	return nil
}

// osDescription: nome do sistema para o painel ("Microsoft Windows 11 Pro", "macOS 15.1"…).
func osDescription() string {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			"(Get-CimInstance Win32_OperatingSystem).Caption").Output()
		if s := strings.TrimSpace(string(out)); err == nil && s != "" {
			return s
		}
		return "Windows"
	case "darwin":
		out, _ := exec.Command("sw_vers", "-productVersion").Output()
		return strings.TrimSpace("macOS " + strings.TrimSpace(string(out)))
	default:
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			if m := regexp.MustCompile(`(?m)^PRETTY_NAME="?([^"\n]+)"?`).FindSubmatch(b); m != nil {
				return string(m[1])
			}
		}
		return "Linux"
	}
}

// reportAgent avisa o painel (pelo mesmo link temporário do gravador).
func reportAgent(b *Bundle, state, msg string) {
	host, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{"hostname": host, "os": osDescription(), "state": state, "message": msg})
	c := &http.Client{Timeout: 20 * time.Second}
	// tenta de novo se o painel pedir para esperar (429) ou estiver fora do ar
	for try := 0; try < 5; try++ {
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(b.Server, "/")+"/t/"+b.Token+"/agent", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return
			}
		}
		time.Sleep(time.Duration(2+try*2) * time.Second)
	}
}

// ---------- estado mostrado na tela
type agentStatus struct {
	mu      sync.Mutex
	State   string `json:"state"`   // "" | "instalando" | "ok" | "erro" | "recusado"
	Message string `json:"message"` // detalhe (erro ou "já estava instalado")
}

func (s *agentStatus) set(state, msg string) {
	s.mu.Lock()
	s.State, s.Message = state, msg
	s.mu.Unlock()
}
func (s *agentStatus) get() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]string{"state": s.State, "message": s.Message}
}

// agentAction: "install" (com a permissão da pessoa) ou "decline".
func (a *app) agentAction(action string) error {
	g := a.bundle.Agent
	switch action {
	case "decline":
		a.agent.set("recusado", "")
		go reportAgent(a.bundle, "recusado", "a pessoa não permitiu o acesso remoto")
		return nil
	case "install":
	default:
		return errors.New("ação inválida")
	}
	if st := a.agent.get()["state"]; st == "instalando" {
		return nil
	}
	a.agent.set("instalando", "")
	go func() {
		var err error
		if osNeedsPrivilegedWorker() { // macOS: janela de senha do sistema
			err = osRunPrivilegedPrompt([]string{"--agent-install", "--agent-url", g.URL, "--agent-meshid", g.MeshID},
				"O isoBuilder precisa da sua senha para instalar o acesso remoto do técnico.")
			if err == nil && !agentInstalled() {
				err = errors.New("o agente não ficou ativo")
			}
		} else {
			err = installAgent(g)
		}
		if err != nil {
			a.agent.set("erro", err.Error())
			reportAgent(a.bundle, "erro", err.Error())
			return
		}
		a.agent.set("ok", "")
		reportAgent(a.bundle, "ok", "")
	}()
	return nil
}
