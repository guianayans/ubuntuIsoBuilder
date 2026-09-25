package main

// Gravador personalizado: o servidor anexa ao executável (Windows/Linux) ou
// coloca em Contents/Resources (macOS) um JSON com o link temporário da ISO.
// Assim o usuário baixa um único arquivo e o gravador busca a ISO sozinho.
//
// Formato do anexo no fim do executável:
//   <json> <8 bytes: tamanho do json, big-endian> "UABCFG01"

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const bundleMagic = "UABCFG01"

type Bundle struct {
	Server    string `json:"server"`
	Token     string `json:"token"`
	ISOName   string `json:"iso_name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Hostname  string `json:"hostname"`
	ExpiresAt int64  `json:"expires_at"`
}

var safeName = regexp.MustCompile(`^[a-z0-9-]+-autoinstall\.iso$`)

func (b *Bundle) valid() bool {
	if b == nil {
		return false
	}
	secure := strings.HasPrefix(b.Server, "https://") ||
		(os.Getenv("UAB_BUNDLE_INSECURE") == "1" && strings.HasPrefix(b.Server, "http://127.0.0.1:")) // só testes locais
	return secure && len(b.Token) >= 32 &&
		safeName.MatchString(b.ISOName) && len(b.SHA256) == 64 && b.Size > 0
}

// loadBundle procura a configuração embutida (anexo ou arquivo do .app).
func loadBundle() *Bundle {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if b := readOverlay(exe); b.valid() {
		return b
	}
	// macOS: Gravador de Pendrive.app/Contents/Resources/uab-download.json
	p := filepath.Join(filepath.Dir(exe), "..", "Resources", "uab-download.json")
	if data, err := os.ReadFile(p); err == nil {
		var b Bundle
		if json.Unmarshal(data, &b) == nil && b.valid() {
			return &b
		}
	}
	if p := os.Getenv("UAB_BUNDLE_JSON"); p != "" { // testes
		if data, err := os.ReadFile(p); err == nil {
			var b Bundle
			if json.Unmarshal(data, &b) == nil && b.valid() {
				return &b
			}
		}
	}
	return nil
}

func readOverlay(path string) *Bundle {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < 16 {
		return nil
	}
	tail := make([]byte, 16)
	if _, err := f.ReadAt(tail, fi.Size()-16); err != nil || string(tail[8:]) != bundleMagic {
		return nil
	}
	n := int64(binary.BigEndian.Uint64(tail[:8]))
	if n <= 0 || n > 64<<10 || n > fi.Size()-16 {
		return nil
	}
	data := make([]byte, n)
	if _, err := f.ReadAt(data, fi.Size()-16-n); err != nil {
		return nil
	}
	var b Bundle
	if json.Unmarshal(data, &b) != nil {
		return nil
	}
	return &b
}

// downloadDir: pasta Downloads do usuário (ou a pasta do programa).
func downloadDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(h, "Downloads")
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && writable(d) {
			return d
		}
	}
	if exe, err := os.Executable(); err == nil && writable(filepath.Dir(exe)) {
		return filepath.Dir(exe)
	}
	d, _ := os.Getwd()
	return d
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".gravador-teste-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// fetchISO baixa a ISO (com retomada) e confere o SHA256. Devolve o caminho.
func fetchISO(b *Bundle, p *progress) (string, error) {
	dir := downloadDir()
	dest := filepath.Join(dir, b.ISOName)

	// Já baixada antes? Confere e reaproveita.
	if fi, err := os.Stat(dest); err == nil && fi.Size() == b.Size {
		if sum, err := hashFile(dest, p); err == nil && sum == b.SHA256 {
			_ = os.WriteFile(dest+".sha256", []byte(b.SHA256+"  "+b.ISOName+"\n"), 0o644)
			return dest, nil
		}
	}

	part := dest + ".part"
	url := strings.TrimRight(b.Server, "/") + "/t/" + b.Token + "/iso"
	client := &http.Client{Timeout: 0}
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		have := int64(0)
		if fi, err := os.Stat(part); err == nil {
			have = fi.Size()
		}
		if have >= b.Size {
			break
		}
		p.set("downloading", fmt.Sprintf("Baixando %s…", b.ISOName), b.Size)
		p.add(have)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			p.setMessage(fmt.Sprintf("Sem conexão, tentando de novo (%d)…", attempt))
			time.Sleep(time.Duration(min(attempt, 10)) * 3 * time.Second)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			have = 0
		case resp.StatusCode == http.StatusPartialContent && have > 0:
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			resp.Body.Close()
			return "", errors.New("o link desta ISO expirou ou foi removido. Baixe o gravador de novo no site")
		default:
			resp.Body.Close()
			lastErr = fmt.Errorf("servidor respondeu %s", resp.Status)
			time.Sleep(5 * time.Second)
			continue
		}
		flags := os.O_CREATE | os.O_WRONLY
		if have > 0 {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
			p.set("downloading", fmt.Sprintf("Baixando %s…", b.ISOName), b.Size)
		}
		f, err := os.OpenFile(part, flags, 0o644)
		if err != nil {
			resp.Body.Close()
			return "", fmt.Errorf("não foi possível salvar em %s: %w", dir, err)
		}
		_, cerr := io.CopyBuffer(counter{f, p}, resp.Body, make([]byte, 1<<20))
		resp.Body.Close()
		f.Close()
		if cerr == nil {
			if fi, err := os.Stat(part); err == nil && fi.Size() >= b.Size {
				break
			}
		}
		lastErr = cerr
		p.setMessage("Conexão interrompida; retomando…")
		time.Sleep(3 * time.Second)
	}
	if fi, err := os.Stat(part); err != nil || fi.Size() != b.Size {
		if lastErr == nil {
			lastErr = errors.New("download incompleto")
		}
		return "", fmt.Errorf("não foi possível baixar a ISO: %v", lastErr)
	}
	sum, err := hashFile(part, p)
	if err != nil {
		return "", err
	}
	if sum != b.SHA256 {
		os.Remove(part)
		return "", errors.New("a ISO baixada veio corrompida (SHA256 não confere). Abra o gravador de novo para baixar outra vez")
	}
	if err := os.Rename(part, dest); err != nil {
		return "", err
	}
	_ = os.WriteFile(dest+".sha256", []byte(b.SHA256+"  "+b.ISOName+"\n"), 0o644)
	return dest, nil
}

// writeOverlay anexa a configuração a um executável (usado em testes; o
// servidor faz o mesmo em Python).
func writeOverlay(w io.Writer, b *Bundle) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(data)))
	if _, err := w.Write(data); err != nil {
		return err
	}
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	_, err = w.Write([]byte(bundleMagic))
	return err
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
