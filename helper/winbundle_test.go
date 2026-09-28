package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func winBundle(server string, cfg []byte) *Bundle {
	return &Bundle{Kind: "windows", Server: server, Token: strings.Repeat("w", 40), ConfigName: "recepcao-01-autounattend.xml",
		SHA256: sha256Hex(cfg), Size: int64(len(cfg)), Hostname: "recepcao-01", ExpiresAt: 1234567890,
		Windows: &BundleWindows{Version: "11", Edition: "Pro", Lang: "pt-BR", Arch: "x64"}}
}

func TestWindowsBundle(t *testing.T) {
	// JSON exatamente como no contrato (§3), anexado ao executável
	raw := `{"kind": "windows", "server": "https://iso.exemplo", "token": "` + strings.Repeat("a", 40) + `",
	 "config_name": "recepcao-01-autounattend.xml", "sha256": "` + strings.Repeat("0", 64) + `", "size": 12345,
	 "hostname": "recepcao-01", "expires_at": 1234567890,
	 "windows": {"version": "11", "edition": "Pro", "lang": "pt-BR", "arch": "x64"}}`
	var b Bundle
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.WriteString("MZ binário")
	if err := writeOverlay(&buf, &b); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "isoBuilder.exe")
	os.WriteFile(exe, buf.Bytes(), 0o755)
	got := readOverlay(exe)
	if got == nil || !got.valid() || !got.isWindows() || got.Windows.Lang != "pt-BR" || got.ConfigName != b.ConfigName {
		t.Fatalf("bundle Windows não lido: %#v", got)
	}
	// Ubuntu sem "kind" e com "kind":"ubuntu" continuam valendo
	u := Bundle{Server: "https://x", Token: strings.Repeat("a", 40), ISOName: "a-autoinstall.iso", SHA256: strings.Repeat("0", 64), Size: 1}
	if !u.valid() || u.isWindows() {
		t.Fatal("bundle Ubuntu antigo deveria valer")
	}
	u.Kind = "ubuntu"
	if !u.valid() {
		t.Fatal(`bundle "ubuntu" deveria valer`)
	}
	bad := []func(b *Bundle){
		func(b *Bundle) { b.ConfigName = "../x-autounattend.xml" },
		func(b *Bundle) { b.Windows = nil },
		func(b *Bundle) { b.Windows.Version = "10" },
		func(b *Bundle) { b.Windows.Arch = "x86" },
		func(b *Bundle) { b.Windows.Lang = "pt-BR;rm" },
		func(b *Bundle) { b.Size = 50 << 20 },
		func(b *Bundle) { b.Kind = "macos" },
		func(b *Bundle) { b.Server = "http://iso.exemplo" },
	}
	for i, f := range bad {
		c := b
		w := *b.Windows
		c.Windows = &w
		f(&c)
		if c.valid() {
			t.Fatalf("caso inseguro %d aceito", i)
		}
	}
}

func TestFetchConfig(t *testing.T) {
	cfg := []byte(`<?xml version="1.0"?><unattend xmlns="urn:schemas-microsoft-com:unattend"/>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/config") && strings.Contains(r.URL.Path, "/t/"+strings.Repeat("w", 40)+"/"):
			w.Write(cfg)
		case strings.HasSuffix(r.URL.Path, "/config"):
			http.Error(w, "expirado", http.StatusGone)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	b := winBundle(srv.URL, cfg)
	got, err := fetchConfig(b)
	if err != nil || !bytes.Equal(got, cfg) {
		t.Fatalf("configuração: %v", err)
	}
	b2 := winBundle(srv.URL, cfg)
	b2.SHA256 = strings.Repeat("1", 64)
	if _, err := fetchConfig(b2); err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("esperava erro de SHA256, veio %v", err)
	}
	b3 := winBundle(srv.URL, cfg)
	b3.Token = strings.Repeat("x", 40)
	if _, err := fetchConfig(b3); err == nil || !strings.Contains(err.Error(), "expirou") {
		t.Fatalf("esperava erro de expirado, veio %v", err)
	}
}

func TestVentoyAutoInstall(t *testing.T) {
	mnt := t.TempDir()
	os.MkdirAll(filepath.Join(mnt, "ventoy"), 0o755)
	// configuração já existente do usuário: precisa ser preservada
	old := `{"control":[{"VTOY_DEFAULT_MENU_MODE":"1"}],"auto_install":[{"image":"/outra.iso","template":"/ventoy/script/x.cfg"},{"image":"/ISOs/recepcao-01-Win11.iso","template":"/velho.xml"}]}`
	os.WriteFile(filepath.Join(mnt, "ventoy", "ventoy.json"), []byte(old), 0o644)
	w := &ventoyWin{Host: "recepcao-01", ConfigName: "recepcao-01-autounattend.xml", Unattend: []byte("<unattend/>")}
	if err := ventoyAutoInstall(mnt, "/ISOs/recepcao-01-Win11.iso", w); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(mnt, "ventoy", "ventoy.json"))
	var cfg struct {
		Control     []map[string]string `json:"control"`
		AutoInstall []struct {
			Image    string `json:"image"`
			Template any    `json:"template"`
			Autosel  int    `json:"autosel"`
		} `json:"auto_install"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Control) != 1 || len(cfg.AutoInstall) != 2 || cfg.AutoInstall[0].Image != "/outra.iso" {
		t.Fatalf("configuração do usuário não preservada: %s", b)
	}
	e := cfg.AutoInstall[1]
	if e.Image != "/ISOs/recepcao-01-Win11.iso" || e.Autosel != 1 || !strings.Contains(string(b), "/ventoy/script/recepcao-01-autounattend.xml") {
		t.Fatalf("entrada errada: %s", b)
	}
	if x, _ := os.ReadFile(filepath.Join(mnt, "ventoy", "script", "recepcao-01-autounattend.xml")); string(x) != "<unattend/>" {
		t.Fatal("modelo não gravado")
	}
	// ventoy.json inválido: não mexe
	os.WriteFile(filepath.Join(mnt, "ventoy", "ventoy.json"), []byte("{quebrado"), 0o644)
	if err := ventoyAutoInstall(mnt, "/a.iso", w); err == nil {
		t.Fatal("ventoy.json inválido deveria ser recusado")
	}
	if b, _ := os.ReadFile(filepath.Join(mnt, "ventoy", "ventoy.json")); string(b) != "{quebrado" {
		t.Fatal("ventoy.json inválido foi alterado")
	}
	w.Unattend = []byte("<x>$$USERNAME$$</x>")
	if err := ventoyAutoInstall(t.TempDir(), "/a.iso", w); err == nil {
		t.Fatal("variável do Ventoy na configuração deveria ser recusada")
	}
}
