package main

// Conteúdo da partição INSTALAR e o "modo pendrive" (isoBuilder aberto a partir dela).

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// StickInfo vai em uab-stick.json na partição INSTALAR.
type StickInfo struct {
	Hostname string `json:"hostname"`
	ISOName  string `json:"iso_name"`
	Created  string `json:"created"`
	Ventoy   bool   `json:"ventoy,omitempty"` // no pendrive com Ventoy: escolher a ISO no menu dele
}

const toolsReadme = `PENDRIVE DE INSTALAÇÃO DO UBUNTU SERVER — servidor %s

ATENÇÃO: instalar APAGA TODO O DISCO do computador (arquivos, programas e o
Windows/macOS atual), sem volta.

Para instalar NESTE computador, com ele ligado e o pendrive conectado:
  Windows: abra "Instalar-no-Windows.exe" (aceite o pedido de administrador).
  Mac Intel: abra "Instalar-no-Mac.app" (Macs com chip Apple M1–M4 não são compatíveis).
  Linux:   no terminal, dentro desta pasta: sudo ./instalar-linux
Confirme digitando FORMATAR: o computador reinicia pelo pendrive e a instalação
é automática (não é preciso responder nada).

Sem os programas: desligue, ligue de novo apertando a tecla do menu de boot
(F12, F11, F8 ou Esc; no Mac, segure Option) e escolha o pendrive.
`

// gatherTools monta os arquivos da partição INSTALAR numa pasta temporária.
// Com o isoBuilder personalizado, baixa do painel as versões dos três sistemas;
// sem ele, leva só a do sistema atual.
func (a *app) gatherTools(isoPath string) (string, []string, error) {
	dir, err := os.MkdirTemp("", "isobuilder-tools")
	if err != nil {
		return "", nil, err
	}
	var notes []string
	put := func(rel string, data []byte) error {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, data, 0o644)
	}
	host := strings.TrimSuffix(filepath.Base(isoPath), "-autoinstall.iso")
	a.mu.Lock()
	b := a.bundle
	a.mu.Unlock()
	if b != nil && b.Hostname != "" {
		host = b.Hostname
	}
	info, _ := json.MarshalIndent(StickInfo{Hostname: host, ISOName: filepath.Base(isoPath), Created: time.Now().Format(time.RFC3339)}, "", "  ")
	if err := put(stickMarker, info); err != nil {
		return dir, nil, err
	}
	if err := put("LEIA-ME.txt", []byte(strings.ReplaceAll(fmt.Sprintf(toolsReadme, host), "\n", "\r\n"))); err != nil {
		return dir, nil, err
	}
	got := map[string]bool{}
	if b != nil {
		client := &http.Client{Timeout: 3 * time.Minute}
		for _, plat := range []string{"windows", "macos", "linux"} {
			data, err := httpGet(client, strings.TrimRight(b.Server, "/")+"/t/"+b.Token+"/tool/"+plat)
			if err == nil {
				err = placeTool(put, plat, data)
			}
			if err != nil {
				notes = append(notes, fmt.Sprintf("isoBuilder para %s não incluído (%v)", plat, err))
				continue
			}
			got[plat] = true
		}
	}
	// o do próprio sistema sempre vai (mesmo sem o painel)
	own := map[string]string{"windows": "windows", "darwin": "macos", "linux": "linux"}[runtime.GOOS]
	if own != "" && !got[own] {
		if err := placeOwn(put); err != nil {
			notes = append(notes, "isoBuilder deste sistema não incluído ("+err.Error()+")")
		}
	}
	return dir, notes, nil
}

func httpGet(c *http.Client, url string) ([]byte, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("servidor respondeu %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 40<<20))
}

func placeTool(put func(string, []byte) error, plat string, data []byte) error {
	switch plat {
	case "windows":
		return put("Instalar-no-Windows.exe", data)
	case "linux":
		return put("instalar-linux", data)
	case "macos":
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !strings.HasPrefix(f.Name, "isoBuilder.app/") || strings.HasSuffix(f.Name, "uab-download.json") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			b, err := io.ReadAll(io.LimitReader(rc, 40<<20))
			rc.Close()
			if err != nil {
				return err
			}
			if err := put("Instalar-no-Mac.app/"+strings.TrimPrefix(f.Name, "isoBuilder.app/"), b); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("sistema desconhecido")
}

// placeOwn copia o isoBuilder em execução (sem link/token: só o genérico chega aqui).
func placeOwn(put func(string, []byte) error) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		i := strings.Index(exe, ".app/")
		if i < 0 {
			return errors.New("fora de um .app")
		}
		app := exe[:i+4]
		return filepath.WalkDir(app, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(p, "uab-download.json") {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(app, p)
			return put("Instalar-no-Mac.app/"+filepath.ToSlash(rel), b)
		})
	case "windows":
		b, err := os.ReadFile(exe)
		if err != nil {
			return err
		}
		return put("Instalar-no-Windows.exe", stripOverlay(b))
	default:
		b, err := os.ReadFile(exe)
		if err != nil {
			return err
		}
		return put("instalar-linux", stripOverlay(b))
	}
}

// stripOverlay remove o link/token anexado ao executável personalizado.
func stripOverlay(b []byte) []byte {
	m := len(bundleMagic)
	if len(b) > m+8 && string(b[len(b)-m:]) == bundleMagic {
		n := int(binary.BigEndian.Uint64(b[len(b)-m-8 : len(b)-m])) // mesmo formato de readOverlay
		if n > 0 && n < len(b)-m-8 {
			return b[:len(b)-m-8-n]
		}
	}
	return b
}

func readToolsDir(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	return files, err
}

// detectStick: o isoBuilder foi aberto a partir da partição INSTALAR?
func detectStick() *StickInfo {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	d := filepath.Dir(exe)
	for i := 0; i < 5; i++ {
		if b, err := os.ReadFile(filepath.Join(d, stickMarker)); err == nil {
			var s StickInfo
			if json.Unmarshal(b, &s) == nil && s.ISOName != "" {
				return &s
			}
		}
		up := filepath.Dir(d)
		if up == d {
			break
		}
		d = up
	}
	return nil
}
