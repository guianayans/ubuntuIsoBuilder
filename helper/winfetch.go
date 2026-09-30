package main

// Modos usados pelo PAINEL (não pela interface): conferir uma ISO do Windows enviada pelo
// admin e baixar a ISO da Microsoft no servidor, para o painel guardar como reserva.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// runInspectWindows confere a ISO e imprime um JSON (ou sai != 0 com o motivo).
func runInspectWindows(iso, arch, edition, lang string) {
	info, err := inspectWindowsISO(iso, arch, edition, lang)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	b, _ := json.Marshal(map[string]any{
		"build": info.Build, "lang": info.Lang, "arch": info.Arch,
		"edition": info.Edition, "name": filepath.Base(iso), "install_size": info.InstallSz,
	})
	fmt.Println(string(b))
}

// runFetchWindows baixa a ISO da Microsoft e salva em out (imprime "PROGRESS <pct>").
func runFetchWindows(lang, arch, edition, out string) {
	w := &BundleWindows{Version: "11", Edition: edition, Lang: lang, Arch: arch}
	p := &progress{}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		last := -1
		for {
			select {
			case <-done:
				return
			case <-t.C:
				st := p.get()
				if pct := int(st.Percent); pct != last {
					last = pct
					fmt.Printf("PROGRESS %d\n", pct)
				}
				if st.Message != "" {
					fmt.Println(st.Message)
				}
			}
		}
	}()
	path, _, err := downloadWindowsISO(context.Background(), w, p)
	close(done)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Rename(path, out); err != nil { // pode ser outro sistema de arquivos: copia
		if cerr := copyBig(path, out); cerr != nil {
			fmt.Fprintln(os.Stderr, cerr)
			os.Exit(1)
		}
		os.Remove(path)
	}
	fmt.Println("PROGRESS 100")
	fmt.Println("OK")
}

func copyBig(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err = io.Copy(f, in); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
