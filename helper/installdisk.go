package main

// "Formatar sem pendrive" (fase 2, só Linux): o PC baixa o kernel e o initrd da ISO
// (servidos pelo painel), faz kexec para o instalador com url= apontando para a ISO no
// painel — o casper baixa a ISO para a RAM e roda o autoinstall, apagando o disco. Não
// depende de pendrive nem da firmware (kexec troca o kernel em execução), então serve em
// UEFI e em BIOS antiga. Precisa de RAM suficiente para a ISO (~4 GB) e de kexec-tools.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// downloadSHA baixa a URL para dst e confere o SHA256 (hex minúsculo). max = teto de bytes.
func downloadSHA(u, dst, want string, max int64) error {
	resp, err := agentHTTP.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("o painel respondeu %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, max))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n < 1000 {
		return fmt.Errorf("arquivo pequeno demais (%d bytes)", n)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("o arquivo baixado não confere (%s)", filepath.Base(dst))
	}
	return nil
}

// runInstallDisk: baixa kernel/initrd, carrega o kexec e reinicia para o instalador.
// Saídas: 0 reiniciando, 1 erro, 3 sem kexec, 5 cancelado.
func runInstallDisk(baseURL, kernelSHA, initrdSHA string, wait time.Duration, reportURL string) int {
	rep := &formatReporter{url: reportURL, c: &http.Client{Timeout: 20 * time.Second}}
	defer osBootUSBCleanup()
	if !diskInstallSupported() {
		rep.send("erro", "este PC não tem o kexec (apt install kexec-tools) ou não tem RAM suficiente para a instalação sem pendrive. Use a opção com pendrive.", nil)
		return 3
	}
	deadline := time.Now().Add(wait)
	dir, err := os.MkdirTemp("", "uab-inst-")
	if err != nil {
		rep.send("erro", err.Error(), nil)
		return 1
	}
	defer os.RemoveAll(dir)
	kern, init := filepath.Join(dir, "vmlinuz"), filepath.Join(dir, "initrd")

	if rep.send("baixando", "Baixando o instalador…", nil) {
		return 5
	}
	for _, d := range []struct{ url, dst, sha string }{
		{baseURL + "/kernel", kern, kernelSHA}, {baseURL + "/initrd", init, initrdSHA},
	} {
		if err := downloadSHA(d.url, d.dst, d.sha, 600<<20); err != nil {
			if time.Now().After(deadline) {
				rep.send("expirou", "O PC não ficou pronto a tempo.", nil)
				return 1
			}
			rep.send("erro", "não consegui baixar o instalador: "+err.Error(), nil)
			return 1
		}
	}

	if rep.send("preparando", "Preparando a instalação…", nil) {
		return 5
	}
	if err := osStageKexec(kern, init, baseURL+"/iso"); err != nil {
		rep.send("erro", "não consegui preparar o kexec: "+err.Error(), nil)
		return 1
	}

	total := time.Duration(bootCountdownN) * bootCountdownStep
	for i := 0; i <= bootCountdownN; i++ { // o técnico ainda pode cancelar
		if i > 0 {
			time.Sleep(bootCountdownStep)
		}
		left := (total - time.Duration(i)*bootCountdownStep).Round(time.Second)
		if rep.send("preparando", fmt.Sprintf("Instalador pronto. Reiniciando em %d segundos.", int(left.Seconds())), nil) {
			return 5
		}
	}
	osBeforeInstallReboot() // BitLocker não se aplica ao Linux; mantém o padrão
	rep.send("reiniciando", "Reiniciando para instalar. A instalação começa sozinha; o PC volta a aparecer depois de pronta.", nil)
	if os.Getenv("UAB_FAKE_DISK") != "" {
		return 0 // teste: nunca reinicia a máquina de verdade
	}
	if err := osKexecReboot(); err != nil {
		rep.send("erro", "não consegui reiniciar pelo kexec: "+err.Error(), nil)
		return 1
	}
	return 0
}

// safeHex64: um SHA256 em hex (64 caracteres).
func safeHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
