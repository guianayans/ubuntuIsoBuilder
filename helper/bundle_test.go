package main

import (
	"bytes"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOverlayRoundTrip(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "gravador.exe")
	b := &Bundle{Server: "https://iso.exemplo", Token: strings.Repeat("a", 43), ISOName: "srv-teste-autoinstall.iso",
		SHA256: strings.Repeat("0", 64), Size: 123, Hostname: "srv-teste"}
	var buf bytes.Buffer
	buf.Write([]byte("MZ...binário falso..."))
	if err := writeOverlay(&buf, b); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(exe, buf.Bytes(), 0o755)
	got := readOverlay(exe)
	if got == nil || !got.valid() || got.Token != b.Token || got.ISOName != b.ISOName {
		t.Fatalf("anexo não lido: %#v", got)
	}
	os.WriteFile(exe, []byte("sem anexo"), 0o755)
	if readOverlay(exe) != nil {
		t.Fatal("não deveria achar anexo")
	}
}

func TestBundleRejectsUnsafe(t *testing.T) {
	bad := []Bundle{
		{Server: "http://x", Token: strings.Repeat("a", 40), ISOName: "a-autoinstall.iso", SHA256: strings.Repeat("0", 64), Size: 1},
		{Server: "https://x", Token: strings.Repeat("a", 40), ISOName: "../../etc/passwd", SHA256: strings.Repeat("0", 64), Size: 1},
		{Server: "https://x", Token: "curto", ISOName: "a-autoinstall.iso", SHA256: strings.Repeat("0", 64), Size: 1},
	}
	for i := range bad {
		if bad[i].valid() {
			t.Fatalf("configuração insegura aceita: %#v", bad[i])
		}
	}
}

// Servidor que derruba a conexão no meio do primeiro download: o gravador
// precisa retomar com Range e conferir o SHA256.
func TestFetchResumesAndVerifies(t *testing.T) {
	data := make([]byte, 6<<20+777)
	rand.Read(data)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/iso") {
			http.NotFound(w, r)
			return
		}
		n := atomic.AddInt32(&calls, 1)
		if n == 1 { // primeira tentativa: manda só 1 MiB e corta
			w.Header().Set("Content-Length", "99999999")
			w.WriteHeader(200)
			w.Write(data[:1<<20])
			if hj, ok := w.(http.Hijacker); ok {
				c, _, _ := hj.Hijack()
				c.Close()
			}
			return
		}
		http.ServeContent(w, r, "x.iso", time.Now(), bytes.NewReader(data))
	}))
	defer srv.Close()

	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "Downloads"), 0o755)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	b := &Bundle{Server: srv.URL, Token: strings.Repeat("t", 43), ISOName: "srv-teste-autoinstall.iso",
		SHA256: sha256Hex(data), Size: int64(len(data)), Hostname: "srv-teste"}
	path, err := fetchISO(b, &progress{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, data) {
		t.Fatal("ISO baixada difere")
	}
	if calls < 2 {
		t.Fatal("deveria ter retomado o download")
	}
	if _, err := os.Stat(path + ".sha256"); err != nil {
		t.Fatal("arquivo .sha256 não criado")
	}
	// segunda execução reaproveita o arquivo já baixado, sem rede
	srv.Close()
	if p2, err := fetchISO(b, &progress{}); err != nil || p2 != path {
		t.Fatalf("deveria reaproveitar a ISO: %v", err)
	}
}

func TestFetchExpiredAndCorrupt(t *testing.T) {
	data := []byte(strings.Repeat("x", 4096))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/expirado/") {
			http.Error(w, "expirado", http.StatusGone)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "Downloads"), 0o755)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	b := &Bundle{Server: srv.URL, Token: "expirado", ISOName: "a-autoinstall.iso", SHA256: sha256Hex(data), Size: 4096}
	if _, err := fetchISO(b, &progress{}); err == nil || !strings.Contains(err.Error(), "expirou") {
		t.Fatalf("esperava erro de link expirado, veio %v", err)
	}
	b = &Bundle{Server: srv.URL, Token: "ok", ISOName: "b-autoinstall.iso", SHA256: strings.Repeat("0", 64), Size: 4096}
	if _, err := fetchISO(b, &progress{}); err == nil || !strings.Contains(err.Error(), "corrompida") {
		t.Fatalf("esperava erro de ISO corrompida, veio %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "Downloads", "b-autoinstall.iso")); err == nil {
		t.Fatal("ISO corrompida não deveria ficar em Downloads")
	}
}
