package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func makeISO(t *testing.T, size int) (string, []byte) {
	t.Helper()
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "teste-autoinstall.iso")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, b
}

func TestWriteAndVerify(t *testing.T) {
	// tamanho não múltiplo do chunk e múltiplo de 2048 (como uma ISO real)
	iso, data := makeISO(t, 9*chunkSize+2048*7)
	devPath := filepath.Join(t.TempDir(), "dev.img")
	// "pendrive" com lixo prévio para garantir que tudo é sobrescrito
	junk := bytes.Repeat([]byte{0xAB}, 12*chunkSize)
	if err := os.WriteFile(devPath, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	dev, err := os.OpenFile(devPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &progress{}
	sum, err := writeImage(iso, dev, int64(len(junk)), p)
	dev.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("sha retornado difere")
	}
	got, _ := os.ReadFile(devPath)
	if !bytes.Equal(got[:len(data)], data) {
		t.Fatal("conteúdo gravado difere da ISO")
	}
	pad := padLen(int64(len(data)))
	for _, c := range got[len(data):pad] {
		if c != 0 {
			t.Fatal("preenchimento de alinhamento deveria ser zero")
		}
	}
}

func TestRejectsSmallDevice(t *testing.T) {
	iso, _ := makeISO(t, 3*chunkSize)
	devPath := filepath.Join(t.TempDir(), "dev.img")
	dev, _ := os.OpenFile(devPath, os.O_RDWR|os.O_CREATE, 0o600)
	defer dev.Close()
	if _, err := writeImage(iso, dev, 2*chunkSize, &progress{}); err == nil {
		t.Fatal("deveria recusar dispositivo menor que a ISO")
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	iso, _ := makeISO(t, 3*chunkSize)
	devPath := filepath.Join(t.TempDir(), "dev.img")
	dev, _ := os.OpenFile(devPath, os.O_RDWR|os.O_CREATE, 0o600)
	defer dev.Close()
	if _, err := writeImage(iso, dev, 0, &progress{}); err != nil {
		t.Fatal(err)
	}
	// corrompe e verifica manualmente a detecção via hash
	_, _ = dev.WriteAt([]byte{1, 2, 3}, 2*chunkSize)
	sumIso, _ := hashFile(iso, &progress{})
	sumDev, _ := hashFile(devPath, &progress{})
	if sumIso == sumDev {
		t.Fatal("corrupção não detectada")
	}
}

func TestPlist(t *testing.T) {
	x := []byte(`<?xml version="1.0"?><plist version="1.0"><dict>
<key>WholeDisks</key><array><string>disk4</string></array>
<key>TotalSize</key><integer>31914983424</integer>
<key>Internal</key><false/><key>MediaName</key><string>SanDisk Ultra</string>
</dict></plist>`)
	m, err := parsePlist(x)
	if err != nil {
		t.Fatal(err)
	}
	if m["TotalSize"].(int64) != 31914983424 || m["Internal"].(bool) || m["MediaName"] != "SanDisk Ultra" {
		t.Fatalf("plist mal interpretado: %#v", m)
	}
	if w := m["WholeDisks"].([]any); len(w) != 1 || w[0] != "disk4" {
		t.Fatalf("array mal interpretado: %#v", w)
	}
}
