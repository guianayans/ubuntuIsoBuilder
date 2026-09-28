package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Download COMPLETO e real da ISO do Windows 11 (opcional): UAB_MS_FULL=/pasta/destino.
// Usa exatamente o fluxo do gravador (downloadWindowsISO): link da Microsoft, retomada,
// SHA256 da tabela publicada pela Microsoft e conferência da estrutura da ISO.
func TestMSFullDownload(t *testing.T) {
	dir := os.Getenv("UAB_MS_FULL")
	if dir == "" {
		t.Skip("UAB_MS_FULL não definido")
	}
	home := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(home, "Downloads")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	p := &progress{file: filepath.Join(dir, ".status.json")}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	iso, sum, err := downloadWindowsISO(ctx, &BundleWindows{Version: "11", Edition: "Pro", Lang: "pt-BR", Arch: "x64"}, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ISO %s sha256=%s", iso, sum)
}
