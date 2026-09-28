package main

// Testes com a ISO real do Windows (opcionais): UAB_WIN_ISO=/caminho/Win11.iso
// UAB_SWM_OUT=/pasta grava também as partes .swm (para conferir com outra ferramenta).

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRealWindowsISO(t *testing.T) {
	iso := os.Getenv("UAB_WIN_ISO")
	if iso == "" {
		t.Skip("UAB_WIN_ISO não definido")
	}
	f, err := os.Open(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	u, err := OpenUDF(f)
	if err != nil {
		t.Fatal(err)
	}
	nf, nd := 0, 0
	for _, x := range u.Files {
		if x.Dir {
			nd++
		} else {
			nf++
		}
	}
	t.Logf("UDF: %d arquivos, %d pastas", nf, nd)
	info, err := inspectUDF(u, "x64", "*", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectUDF(u, "x64", "Pro", ""); err == nil {
		t.Log("a ISO tem a edição Pro")
	} else {
		t.Logf("sem Pro (esperado na ISO de avaliação): %v", err)
	}
	if _, err := inspectUDF(u, "x64", "*", "en-US"); err == nil {
		t.Error("idioma diferente deveria ser recusado")
	}
	if _, err := inspectUDF(u, "arm64", "*", ""); err == nil {
		t.Error("arquitetura diferente deveria ser recusada")
	}
	for _, im := range info.Images {
		t.Logf("imagem %d: %q edição=%s arch=%d build=%d idioma=%s", im.Index, im.Name, im.EditionID, im.Arch, im.Build, im.Lang)
	}
	// um arquivo pequeno lido pelo UDF deve bater com o 7z (hash impresso para conferência)
	if bm := u.Lookup("efi/boot/bootx64.efi"); bm != nil {
		h := sha256.New()
		io.Copy(h, u.Open(bm))
		t.Logf("bootx64.efi %d bytes sha256=%s", bm.Size, hex.EncodeToString(h.Sum(nil)))
	}
	inst := u.Lookup("sources/install.wim")
	r := u.Open(inst)
	parts, err := planSplit(r, "install", wimMaxPart)
	if err != nil {
		t.Fatal(err)
	}
	var ras []io.ReaderAt
	var total int64
	for _, p := range parts {
		t.Logf("%s: %d bytes, %d trechos", p.Name, p.Size, len(p.Segs))
		total += p.Size
		ras = append(ras, newSegReaderAt(p.Segs))
	}
	t.Logf("install.wim %d -> partes somam %d", inst.Size, total)
	if err := checkSplit(r, ras); err != nil {
		t.Fatal(err)
	}
	// recursos sem compressão: o SHA1 do conteúdo na parte tem de bater com a tabela
	checked := 0
	for i, pr := range ras {
		ph, _ := readWIMHeader(pr)
		es, _ := readWIMLookup(pr, ph)
		for _, e := range es {
			if e.Res.Flags&wimResCompr != 0 {
				continue
			}
			h := sha1.New()
			if _, err := io.Copy(h, io.NewSectionReader(pr, e.Res.Off, e.Res.Size)); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(h.Sum(nil), e.Hash[:]) {
				t.Fatalf("parte %d: SHA1 de recurso não confere", i+1)
			}
			checked++
		}
	}
	t.Logf("%d recursos sem compressão conferidos por SHA1", checked)
	if out := os.Getenv("UAB_SWM_OUT"); out != "" {
		for _, p := range parts {
			w, err := os.Create(filepath.Join(out, p.Name))
			if err != nil {
				t.Fatal(err)
			}
			n, err := io.Copy(w, newSegReader(p.Segs))
			w.Close()
			if err != nil || n != p.Size {
				t.Fatalf("%s: %v (%d/%d)", p.Name, err, n, p.Size)
			}
		}
	}
}
