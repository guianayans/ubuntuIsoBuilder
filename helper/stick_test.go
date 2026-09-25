package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// O volume FAT32 gerado passa no fsck.fat (quando disponível) e tem os arquivos.
func TestBuildFAT32(t *testing.T) {
	files := map[string][]byte{
		"LEIA-ME.txt":                                        []byte("teste\n"),
		"Instalar-no-Windows.exe":                            make([]byte, 700_000),
		"Instalar-no-Mac.app/Contents/MacOS/isoBuilder":      []byte("#!/bin/sh\n"),
		"Instalar-no-Mac.app/Contents/Info.plist":            []byte("<plist/>"),
		"Instalar-no-Mac.app/Contents/Resources/icone.icns":  make([]byte, 3000),
		"um nome bem comprido com espaços e acentuação.txt": []byte("ok"),
	}
	img, err := buildFAT32(64<<20, "INSTALAR", files, 2048, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "fat.img")
	if o := os.Getenv("UAB_FAT_OUT"); o != "" {
		p = o
	}
	if err := os.WriteFile(p, img, 0o600); err != nil {
		t.Fatal(err)
	}
	fsck, err := exec.LookPath("fsck.fat")
	if err != nil {
		t.Skip("fsck.fat indisponível")
	}
	out, err := exec.Command(fsck, "-n", "-v", p).CombinedOutput()
	if err != nil {
		t.Fatalf("fsck.fat reprovou:\n%s", out)
	}
}

// Diagnóstico com um pendrive/imagem Ventoy real: UAB_TEST_VENTOY_DEV=/dev/loopN
func TestVentoyReal(t *testing.T) {
	dev := os.Getenv("UAB_TEST_VENTOY_DEV")
	if dev == "" {
		t.Skip("sem dispositivo Ventoy")
	}
	v, err := ventoyFor(Disk{ID: dev, Path: dev})
	if err != nil || v == nil {
		t.Fatalf("Ventoy não detectado: %v", err)
	}
	t.Logf("montado em %s (%s), livre %d bytes, pastas %v", v.Mount, v.FS, v.Free, v.Folders)
	v.Release()
}

// Tabela GPT montada pelo sgdisk (como a da ISO): UAB_TEST_GPT_IMG=/caminho/img
func TestAddToolsPartitionImage(t *testing.T) {
	p := os.Getenv("UAB_TEST_GPT_IMG")
	if p == "" {
		t.Skip("sem imagem")
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	if _, err := addToolsPartition(f, fi.Size(), map[string][]byte{"a.txt": []byte("x")}); err != nil {
		t.Fatal(err)
	}
}
