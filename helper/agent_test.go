package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testMesh = "tzK@Eg$abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOP"

func TestAgentURLs(t *testing.T) {
	g := &BundleAgent{URL: "https://iso.exemplo.com/remoto/", MeshID: testMesh}
	bin, _, _ := agentURLs(g, "windows", "amd64")
	if !strings.HasPrefix(bin, "https://iso.exemplo.com/remoto/meshagents?id=4&meshid=") || !strings.Contains(bin, "installflags=0") {
		t.Fatalf("windows x64: %s", bin)
	}
	if !strings.Contains(bin, "tzK%40Eg%24abc") { // @ e $ escapados na query
		t.Fatalf("meshid não escapado: %s", bin)
	}
	if bin, _, _ = agentURLs(g, "windows", "arm64"); !strings.Contains(bin, "id=43&") {
		t.Fatalf("windows arm64: %s", bin)
	}
	bin, msh, _ := agentURLs(g, "darwin", "arm64")
	if bin != "https://iso.exemplo.com/remoto/meshagents?id=10005" || !strings.HasPrefix(msh, "https://iso.exemplo.com/remoto/meshsettings?id=") {
		t.Fatalf("macOS: %s %s", bin, msh)
	}
	if _, _, sc := agentURLs(g, "linux", "amd64"); sc != "https://iso.exemplo.com/remoto/meshagents?script=1" {
		t.Fatalf("linux: %s", sc)
	}
}

func TestAgentValidSameOrigin(t *testing.T) {
	srv := "https://iso.exemplo.com"
	cases := []struct {
		g  BundleAgent
		ok bool
	}{
		{BundleAgent{URL: "https://iso.exemplo.com/remoto", MeshID: testMesh}, true},
		{BundleAgent{URL: "https://outro-site.com/remoto", MeshID: testMesh}, false}, // outro site: nunca
		{BundleAgent{URL: "http://iso.exemplo.com/remoto", MeshID: testMesh}, false}, // sem TLS
		{BundleAgent{URL: "https://iso.exemplo.com/remoto?x=1", MeshID: testMesh}, false},
		{BundleAgent{URL: "https://iso.exemplo.com/../x", MeshID: testMesh}, false},
		{BundleAgent{URL: "https://iso.exemplo.com/remoto", MeshID: "curto"}, false},
		{BundleAgent{URL: "https://iso.exemplo.com/remoto", MeshID: testMesh + "\"; rm -rf /"}, false},
	}
	for i, c := range cases {
		if got := c.g.valid(srv); got != c.ok {
			t.Errorf("caso %d (%s): valid=%v, esperado %v", i, c.g.URL, got, c.ok)
		}
	}
	var nilAgent *BundleAgent
	if nilAgent.valid(srv) {
		t.Error("agente nulo não pode ser válido")
	}
}

func TestBundleWithAgentJSON(t *testing.T) {
	raw := `{"kind":"windows","server":"https://iso.exemplo.com","token":"` + strings.Repeat("a", 40) + `",
	  "config_name":"pc-autounattend.xml","sha256":"` + strings.Repeat("b", 64) + `","size":1000,"hostname":"pc",
	  "expires_at":1,"windows":{"version":"11","edition":"Pro","lang":"pt-BR","arch":"x64"},
	  "agent":{"url":"https://iso.exemplo.com/remoto","meshid":"` + testMesh + `","account":"cliente@exemplo.com"}}`
	var b Bundle
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	if !b.valid() || b.Agent == nil || !b.Agent.valid(b.Server) || b.Agent.Account != "cliente@exemplo.com" {
		t.Fatalf("bundle com agente não reconhecido: %+v", b.Agent)
	}
}

func TestReportAgent(t *testing.T) {
	var got map[string]string
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(200)
	}))
	defer ts.Close()
	b := &Bundle{Server: ts.URL, Token: strings.Repeat("t", 40)}
	reportAgent(b, "ok", "")
	if path != "/t/"+strings.Repeat("t", 40)+"/agent" || got["state"] != "ok" || got["hostname"] == "" || got["os"] == "" {
		t.Fatalf("relatório: %s %v", path, got)
	}
}
