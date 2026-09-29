//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func ps(script string) ([]byte, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.CombinedOutput()
}

type winDisk struct {
	Number       int    `json:"Number"`
	FriendlyName string `json:"FriendlyName"`
	SerialNumber string `json:"SerialNumber"`
	Size         int64  `json:"Size"`
	BusType      string `json:"BusType"`
	IsBoot       bool   `json:"IsBoot"`
	IsSystem     bool   `json:"IsSystem"`
}

func osListDisks() ([]Disk, error) {
	out, err := ps(`@(Get-Disk | Select-Object Number,FriendlyName,SerialNumber,Size,@{n='BusType';e={[string]$_.BusType}},IsBoot,IsSystem) | ConvertTo-Json -Compress`)
	if err != nil {
		return nil, fmt.Errorf("Get-Disk falhou: %s", strings.TrimSpace(string(out)))
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil, nil
	}
	var list []winDisk
	if strings.HasPrefix(s, "{") {
		var one winDisk
		if err := json.Unmarshal([]byte(s), &one); err != nil {
			return nil, err
		}
		list = []winDisk{one}
	} else if err := json.Unmarshal([]byte(s), &list); err != nil {
		return nil, err
	}
	var disks []Disk
	for _, d := range list {
		bus := strings.ToUpper(d.BusType)
		if d.IsBoot || d.IsSystem || d.Number == 0 {
			continue
		}
		if bus != "USB" && bus != "SD" && bus != "MMC" {
			continue
		}
		disks = append(disks, Disk{
			ID: fmt.Sprint(d.Number), Path: fmt.Sprintf(`\\.\PhysicalDrive%d`, d.Number),
			Size: d.Size, Bus: bus, Name: strings.TrimSpace(d.FriendlyName), Serial: strings.TrimSpace(d.SerialNumber),
		})
	}
	return disks, nil
}

// Apaga a tabela de partições (remove volumes montados), permitindo escrita bruta.
func osPrepareDisk(d Disk) error {
	// 1. Checagem obrigatória e separada: nunca prosseguir num disco do sistema.
	out, err := ps(fmt.Sprintf(`$ErrorActionPreference='Stop'; $d=Get-Disk -Number %s; if ($d.IsBoot -or $d.IsSystem -or $d.Number -eq 0) { 'SISTEMA' } elseif ([string]$d.BusType -notin 'USB','SD','MMC') { 'NAO-USB' } else { 'OK' }`, d.ID))
	verdict := strings.TrimSpace(string(out))
	if err != nil || verdict != "OK" {
		return fmt.Errorf("recusado: o disco %s não é um pendrive/USB seguro para gravar (%s)", d.ID, verdict)
	}
	// 2. Limpa a tabela de partições.
	out, err = ps(fmt.Sprintf(`$ErrorActionPreference='Stop'; Set-Disk -Number %s -IsReadOnly $false -ErrorAction SilentlyContinue; if ((Get-Disk -Number %s).PartitionStyle -ne 'RAW') { Clear-Disk -Number %s -RemoveData -RemoveOEM -Confirm:$false }`, d.ID, d.ID, d.ID))
	if err != nil {
		// Alternativa para mídias removíveis em que Clear-Disk não é suportado.
		out2, err2 := ps(fmt.Sprintf(`$f=New-TemporaryFile; Set-Content -Path $f -Value "select disk %s`+"`r`n"+`clean"; diskpart /s $f; $c=$LASTEXITCODE; Remove-Item $f; exit $c`, d.ID))
		if err2 != nil {
			return fmt.Errorf("não foi possível preparar o pendrive: %s / %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}
	return nil
}

func osOpenDevice(d Disk) (*os.File, error) { return os.OpenFile(d.Path, os.O_RDWR, 0) }

func osFinalize(d Disk) { _, _ = ps(fmt.Sprintf(`Update-Disk -Number %s`, d.ID)) }

func isAdmin() bool {
	out, err := ps(`([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)`)
	return err == nil && strings.Contains(strings.ToLower(string(out)), "true")
}

// Reabre o programa como administrador (janela do UAC). Retorna true se
// relançou (o processo atual deve encerrar).
func osEnsureElevated() (bool, error) {
	if os.Getenv("UAB_FAKE_DISK") != "" || isAdmin() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	args := ""
	if len(os.Args) > 1 {
		q := make([]string, 0, len(os.Args)-1)
		for _, a := range os.Args[1:] {
			q = append(q, "'"+strings.ReplaceAll(a, "'", "''")+"'")
		}
		args = " -ArgumentList " + strings.Join(q, ",")
	}
	out, err := ps(fmt.Sprintf(`Start-Process -FilePath '%s' -Verb RunAs%s`, strings.ReplaceAll(exe, "'", "''"), args))
	if err != nil {
		return false, errors.New("é preciso permitir a execução como administrador: " + strings.TrimSpace(string(out)))
	}
	return true, nil
}

func osOpenBrowser(url string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	_ = cmd.Start()
}

func osNeedsPrivilegedWorker() bool { return false }

func osRunPrivileged(args []string) error { return errors.New("não suportado") }

func osRunPrivilegedPrompt(args []string, prompt string) error { return errors.New("não suportado") }
