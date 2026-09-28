package main

// Partes do fluxo do Windows ligadas à interface: "Já tenho a ISO" e o plugin
// auto_install do Ventoy.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const toolsReadmeWindows = `PENDRIVE DE INSTALAÇÃO DO WINDOWS 11 — computador %s

ATENÇÃO: instalar APAGA TODO O MAIOR DISCO INTERNO do computador (arquivos,
programas e o sistema atual), sem volta. O pendrive nunca é apagado.

Para instalar NESTE computador, com ele ligado e o pendrive conectado:
  Windows: abra "Instalar-no-Windows.exe" (aceite o pedido de administrador).
  Linux:   no terminal, dentro desta pasta: sudo ./instalar-linux
Confirme digitando FORMATAR: o computador reinicia pelo pendrive e a instalação
é automática (não é preciso responder nada).

Sem os programas: desligue, ligue de novo apertando a tecla do menu de boot
(F12, F11, F8 ou Esc) e escolha o pendrive. Ele funciona em UEFI e em BIOS antiga
(Legacy/CSM); se o pendrive aparecer duas vezes, prefira a opção "UEFI".
Em PCs sem UEFI o Windows 11 funciona, mas não é suportado oficialmente pela Microsoft.
`

// selectWindowsISO: o usuário escolheu uma ISO própria (ou o download da Microsoft falhou).
func (a *app) selectWindowsISO(path string, p *progress) {
	p.set("checking", "Conferindo se a ISO é do Windows 11…", 0)
	w := a.bundle.Windows
	info, err := inspectWindowsISO(path, w.Arch, w.Edition, w.Lang)
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	sum, err := hashFile(path, p)
	if err != nil {
		p.finish("error", err.Error(), "")
		return
	}
	if exp := readExpected(path); exp != "" && exp != sum {
		p.finish("error", "O SHA256 da ISO não confere com o arquivo .sha256: o download está corrompido. Baixe a ISO novamente.", sum)
		return
	}
	a.mu.Lock()
	if a.isoPath == path {
		a.isoHash, a.winInfo = sum, info
	}
	a.mu.Unlock()
	p.finish("done", fmt.Sprintf("ISO do Windows 11 conferida: %s, %s, build %d.", info.Edition, info.Lang, info.Build), sum)
}

type ventoyWin struct {
	Host       string
	ConfigName string
	Unattend   []byte
}

var reVentoyVar = regexp.MustCompile(`\$\$[A-Za-z0-9_]{1,62}\$\$`)

// ventoyAutoInstall grava o autounattend.xml em /ventoy/script/ e acrescenta (ou
// troca) a entrada da ISO no plugin auto_install do /ventoy/ventoy.json,
// preservando o resto da configuração do Ventoy. imagePath: "/pasta/arquivo.iso".
func ventoyAutoInstall(mount, imagePath string, w *ventoyWin) error {
	if reVentoyVar.Match(w.Unattend) {
		// o Ventoy trocaria $$NOME$$ por um valor pedido na tela
		return errors.New("a configuração contém $$…$$, que o Ventoy interpretaria como variável")
	}
	vdir := filepath.Join(mount, "ventoy")
	if err := os.MkdirAll(filepath.Join(vdir, "script"), 0o755); err != nil {
		return err
	}
	name := w.ConfigName
	if name == "" {
		name = w.Host + "-autounattend.xml"
	}
	tmpl := "/ventoy/script/" + name
	if err := writeFileSync(filepath.Join(vdir, "script", name), w.Unattend); err != nil {
		return err
	}
	cfgPath := filepath.Join(vdir, "ventoy.json")
	cfg := map[string]json.RawMessage{}
	if b, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return errors.New("o ventoy/ventoy.json que já está no pendrive é inválido; corrija ou apague esse arquivo e tente de novo")
		}
		_ = os.WriteFile(cfgPath+".bak", b, 0o644)
	} else if !os.IsNotExist(err) {
		return err
	}
	var list []map[string]any
	if raw, ok := cfg["auto_install"]; ok {
		if err := json.Unmarshal(raw, &list); err != nil {
			return errors.New(`"auto_install" do ventoy.json em formato inesperado`)
		}
	}
	out := make([]map[string]any, 0, len(list)+1)
	for _, e := range list {
		if img, _ := e["image"].(string); img == imagePath {
			continue
		}
		out = append(out, e)
	}
	// autosel 1 sem timeout: usa este modelo direto, sem outro menu (a escolha da
	// ISO no menu do Ventoy já é o ato consciente de instalar)
	out = append(out, map[string]any{"image": imagePath, "template": []string{tmpl}, "autosel": 1})
	raw, _ := json.Marshal(out)
	cfg["auto_install"] = raw
	b, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return err
	}
	return writeFileSync(cfgPath, b)
}

func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
