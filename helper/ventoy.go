package main

// Pendrive com Ventoy (https://www.ventoy.net): partição 1 com as ISOs (exFAT,
// rótulo "Ventoy" por padrão) + partição 2 "VTOYEFI" (boot). Nesse caso o
// isoBuilder pode só COPIAR a ISO para a pasta escolhida, sem apagar nada:
// o Ventoy procura ISOs em todas as pastas e mostra a nossa no menu dele.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type VentoyInfo struct {
	Mount   string   `json:"mount"`   // onde a partição das ISOs está montada
	FS      string   `json:"fs"`      // exfat, vfat, ntfs...
	Free    int64    `json:"free"`    // bytes livres
	Folders []string `json:"folders"` // pastas do primeiro nível (para escolher o destino)
	release func()   // desmonta, se foi o isoBuilder que montou
}

func (v *VentoyInfo) Release() {
	if v != nil && v.release != nil {
		v.release()
		v.release = nil
	}
}

var ventoySkip = map[string]bool{"ventoy": true, "system volume information": true, "$recycle.bin": true,
	".trashes": true, ".spotlight-v100": true, ".fseventsd": true, "lost+found": true}

func ventoyFor(d Disk) (*VentoyInfo, error) {
	var v *VentoyInfo
	var err error
	if dir := os.Getenv("UAB_FAKE_VENTOY"); dir != "" && os.Getenv("UAB_FAKE_DISK") != "" { // testes
		v = &VentoyInfo{Mount: dir, FS: "exfat"}
	} else {
		v, err = osVentoy(d)
	}
	if v == nil || err != nil {
		return nil, err
	}
	v.Free = freeSpace(v.Mount)
	entries, _ := os.ReadDir(v.Mount)
	for _, e := range entries {
		if e.IsDir() && !ventoySkip[strings.ToLower(e.Name())] && !strings.HasPrefix(e.Name(), ".") {
			v.Folders = append(v.Folders, e.Name())
		}
	}
	sort.Strings(v.Folders)
	return v, nil
}

var badFolder = regexp.MustCompile(`[<>:"\\|?*\x00-\x1f]`)

// ventoyTarget valida a pasta escolhida (relativa, sem "..") e devolve o caminho final.
func ventoyTarget(mount, folder string) (string, error) {
	folder = strings.Trim(strings.ReplaceAll(folder, "\\", "/"), "/ ")
	if folder != "" {
		for _, part := range strings.Split(folder, "/") {
			if part == "" || part == "." || part == ".." || badFolder.MatchString(part) || len(part) > 100 {
				return "", errors.New("nome de pasta inválido")
			}
			if strings.EqualFold(part, "ventoy") {
				return "", errors.New(`a pasta "ventoy" é de configuração do Ventoy; escolha outra`)
			}
		}
	}
	return filepath.Join(mount, filepath.FromSlash(folder)), nil
}

// copyToVentoy copia a ISO (e os programas "Instalar-no-...") e confere o SHA256 relendo do pendrive.
func copyToVentoy(isoPath, wantHash string, v *VentoyInfo, folder, toolsDir string, p *progress) {
	dst, err := ventoyTarget(v.Mount, folder)
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	fi, err := os.Stat(isoPath)
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	if strings.Contains(strings.ToLower(v.FS), "fat") && !strings.Contains(strings.ToLower(v.FS), "exfat") && fi.Size() >= 4<<30 {
		p.finish("error", "a partição do Ventoy é FAT32 (limite de 4 GB por arquivo); use exFAT", "")
		return
	}
	if v.Free > 0 && v.Free < fi.Size()+(64<<20) {
		p.finish("error", fmt.Sprintf("espaço insuficiente no Ventoy: livres %s, a ISO tem %s", human(v.Free), human(fi.Size())), "")
		return
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		p.finish("error", "não foi possível criar a pasta: "+err.Error(), "")
		return
	}
	target := filepath.Join(dst, filepath.Base(isoPath))
	tmp := target + ".copiando"
	src, err := os.Open(isoPath)
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	defer src.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		p.finish("error", "não foi possível gravar no pendrive: "+err.Error(), "")
		return
	}
	p.set("writing", "Copiando a ISO para o Ventoy…", fi.Size())
	buf := make([]byte, chunkSize)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				p.finish("error", "falha ao copiar: "+werr.Error(), "")
				return
			}
			p.add(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			p.finish("error", rerr.Error(), "")
			return
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		p.finish("error", "falha ao gravar no pendrive: "+err.Error(), "")
		return
	}
	out.Close()
	_ = os.Remove(target)
	if err := os.Rename(tmp, target); err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	// conferência: relê do pendrive
	p.set("verifying", "Conferindo a cópia (SHA256)…", fi.Size())
	f, err := os.Open(target)
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(h, progressWriter{p}), f); err != nil {
		f.Close()
		p.finish("error", "falha ao reler a cópia: "+err.Error(), "")
		return
	}
	f.Close()
	got := hex.EncodeToString(h.Sum(nil))
	if wantHash != "" && got != wantHash {
		_ = os.Remove(target)
		p.finish("error", "a cópia no pendrive não confere com a ISO (pendrive com defeito?)", "")
		return
	}
	// programas para reiniciar e instalar (abertos a partir do Ventoy, como na unidade INSTALAR)
	extra := ""
	if toolsDir != "" {
		host := strings.TrimSuffix(filepath.Base(isoPath), "-autoinstall.iso")
		td := filepath.Join(dst, "Instalar-"+host)
		if files, err := readToolsDir(toolsDir); err == nil {
			ok := true
			if b, ok := files[stickMarker]; ok { // marca: pendrive com Ventoy
				var si StickInfo
				if json.Unmarshal(b, &si) == nil {
					si.Ventoy = true
					files[stickMarker], _ = json.MarshalIndent(si, "", "  ")
				}
			}
			for rel, data := range files {
				pp := filepath.Join(td, filepath.FromSlash(rel))
				if os.MkdirAll(filepath.Dir(pp), 0o755) != nil || os.WriteFile(pp, data, 0o755) != nil {
					ok = false
				}
			}
			if ok {
				extra = fmt.Sprintf(` A pasta "Instalar-%s" tem os programas para reiniciar e instalar no computador de destino.`, host)
			}
		}
	}
	where := "na raiz"
	if f := strings.Trim(folder, "/ "); f != "" {
		where = `na pasta "` + f + `"`
	}
	p.finish("done", fmt.Sprintf("ISO copiada para o Ventoy %s e conferida. No boot pelo pendrive, escolha %s no menu do Ventoy.%s",
		where, filepath.Base(isoPath), extra), got)
}

type progressWriter struct{ p *progress }

func (w progressWriter) Write(b []byte) (int, error) { w.p.add(int64(len(b))); return len(b), nil }

// ---- endpoints
func (a *app) ventoyInfoHandler(w http.ResponseWriter, r *http.Request) {
	d, err := findDisk(r.URL.Query().Get("disk"), r.URL.Query().Get("key"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	v, err := ventoyFor(d)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ventoy": nil, "note": err.Error()})
		return
	}
	v.Release() // só consulta: a cópia monta de novo
	writeJSON(w, 200, map[string]any{"ventoy": v})
}

func (a *app) ventoyCopyHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disk, Key, Folder string
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, 400, map[string]string{"error": "requisição inválida"})
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
	iso, hash := a.isoPath, a.isoHash
	a.mu.Unlock()
	d, err := findDisk(req.Disk, req.Key)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	v, err := ventoyFor(d)
	if err != nil || v == nil {
		writeJSON(w, 400, map[string]string{"error": "Ventoy não encontrado neste pendrive"})
		return
	}
	if _, err := ventoyTarget(v.Mount, req.Folder); err != nil {
		v.Release()
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	p := &progress{}
	a.mu.Lock()
	a.job, a.busy = p, true
	a.mu.Unlock()
	go func() {
		defer func() { a.mu.Lock(); a.busy = false; a.mu.Unlock() }()
		defer v.Release() // desmonta (Linux) só depois de gravar e conferir
		p.set("preparing", "Preparando…", 0)
		tools, _, terr := a.gatherTools(iso)
		if terr != nil {
			tools = ""
		}
		if tools != "" {
			defer os.RemoveAll(tools)
		}
		copyToVentoy(iso, hash, v, req.Folder, tools, p)
	}()
	writeJSON(w, 200, map[string]string{"ok": "1"})
}
