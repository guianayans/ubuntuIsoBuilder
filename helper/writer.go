package main

// Gravação e verificação de imagem em dispositivo de bloco, comum a todos
// os sistemas operacionais.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	chunkSize = 4 << 20 // 4 MiB
	headSize  = 1 << 20 // primeiro 1 MiB é gravado por último
	alignSize = 4096    // cobre discos com setor de 512 e 4096 bytes
)

// Status é compartilhado entre o processo de interface e o de gravação.
type Status struct {
	Phase   string  `json:"phase"` // idle|hashing|preparing|writing|verifying|done|error
	Percent float64 `json:"percent"`
	Done    int64   `json:"done"`
	Total   int64   `json:"total"`
	Speed   float64 `json:"speed"` // bytes/s
	Message string  `json:"message"`
	SHA256  string  `json:"sha256,omitempty"`
}

type progress struct {
	mu     sync.Mutex
	st     Status
	start  time.Time
	file   string // se definido, o status também é gravado em arquivo (worker privilegiado)
	lastFS time.Time
}

func (p *progress) set(phase, msg string, total int64) {
	p.mu.Lock()
	p.st = Status{Phase: phase, Message: msg, Total: total}
	p.start = time.Now()
	p.mu.Unlock()
	p.flush(true)
}

func (p *progress) add(n int64) {
	p.mu.Lock()
	p.st.Done += n
	if p.st.Total > 0 {
		p.st.Percent = float64(p.st.Done) * 100 / float64(p.st.Total)
	}
	if el := time.Since(p.start).Seconds(); el > 0 {
		p.st.Speed = float64(p.st.Done) / el
	}
	p.mu.Unlock()
	p.flush(false)
}

func (p *progress) finish(phase, msg, sum string) {
	p.mu.Lock()
	p.st.Phase, p.st.Message, p.st.SHA256 = phase, msg, sum
	if phase == "done" {
		p.st.Percent = 100
	}
	p.mu.Unlock()
	p.flush(true)
}

func (p *progress) setMessage(msg string) {
	p.mu.Lock()
	p.st.Message = msg
	p.mu.Unlock()
	p.flush(true)
}

func (p *progress) get() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

func (p *progress) flush(force bool) {
	if p.file == "" {
		return
	}
	p.mu.Lock()
	if !force && time.Since(p.lastFS) < 300*time.Millisecond {
		p.mu.Unlock()
		return
	}
	p.lastFS = time.Now()
	b, _ := json.Marshal(p.st)
	p.mu.Unlock()
	tmp := p.file + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, p.file)
	}
}

type counter struct {
	w io.Writer
	p *progress
}

func (c counter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.p.add(int64(n))
	return n, err
}

// hashFile calcula o SHA256 de um arquivo reportando progresso.
func hashFile(path string, p *progress) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	p.set("hashing", "Conferindo a ISO (SHA256)…", fi.Size())
	h := sha256.New()
	buf := make([]byte, chunkSize)
	if _, err := io.CopyBuffer(counter{h, p}, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func padLen(n int64) int64 {
	if r := n % alignSize; r != 0 {
		return n + alignSize - r
	}
	return n
}

// writeImage grava a imagem no dispositivo já aberto e verifica por releitura.
// O primeiro 1 MiB (tabelas de partição) é gravado por último para que o
// sistema operacional não detecte/monte partições no meio da gravação.
func writeImage(isoPath string, dev *os.File, devSize int64, p *progress) (string, error) {
	iso, err := os.Open(isoPath)
	if err != nil {
		return "", err
	}
	defer iso.Close()
	fi, err := iso.Stat()
	if err != nil {
		return "", err
	}
	size := fi.Size()
	if size < headSize {
		return "", errors.New("arquivo pequeno demais para ser uma ISO")
	}
	if devSize > 0 && padLen(size) > devSize {
		return "", fmt.Errorf("o pendrive (%s) é menor que a ISO (%s)", human(devSize), human(size))
	}

	buf := make([]byte, chunkSize)
	h := sha256.New()
	head := make([]byte, headSize)
	if _, err := io.ReadFull(iso, head); err != nil {
		return "", err
	}
	h.Write(head)

	p.set("writing", "Gravando no pendrive…", size)
	p.add(headSize)
	off := int64(headSize)
	for off < size {
		n, rerr := iso.ReadAt(buf, off)
		if n == 0 && rerr != nil {
			return "", rerr
		}
		h.Write(buf[:n])
		w := int64(n)
		if off+w == size { // último bloco: completa até o alinhamento com zeros
			pl := padLen(w)
			for i := w; i < pl; i++ {
				buf[i] = 0
			}
			w = pl
		}
		if _, err := dev.WriteAt(buf[:w], off); err != nil {
			return "", fmt.Errorf("falha ao gravar em %s: %w", human(off), err)
		}
		off += int64(n)
		p.add(int64(n))
	}
	if _, err := dev.WriteAt(head, 0); err != nil {
		return "", fmt.Errorf("falha ao gravar o início do pendrive: %w", err)
	}
	if err := dev.Sync(); err != nil {
		return "", fmt.Errorf("falha ao descarregar o cache: %w", err)
	}
	want := hex.EncodeToString(h.Sum(nil))

	// Verificação: relê exatamente o tamanho da ISO do dispositivo.
	p.set("verifying", "Verificando o que foi gravado…", size)
	vh := sha256.New()
	off = 0
	for off < size {
		rd := int64(chunkSize)
		if size-off < rd {
			rd = padLen(size - off)
		}
		n, rerr := dev.ReadAt(buf[:rd], off)
		if int64(n) < rd && rerr != nil && !(errors.Is(rerr, io.EOF) && off+int64(n) >= size) {
			return "", fmt.Errorf("falha ao reler o pendrive: %w", rerr)
		}
		use := int64(n)
		if off+use > size {
			use = size - off
		}
		vh.Write(buf[:use])
		off += use
		p.add(use)
	}
	got := hex.EncodeToString(vh.Sum(nil))
	if got != want {
		return "", fmt.Errorf("verificação falhou: o conteúdo do pendrive difere da ISO (pendrive com defeito?)")
	}
	return want, nil
}

func human(n int64) string {
	const u = 1000
	f := float64(n)
	for _, s := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < u || s == "TB" {
			if s == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, s)
		}
		f /= u
	}
	return ""
}
