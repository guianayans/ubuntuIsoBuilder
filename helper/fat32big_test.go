package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// alignedDev falha se alguém gravar fora do alinhamento de 4 KiB (como o Windows/macOS exigem).
type alignedDev struct {
	*os.File
	t *testing.T
}

func (d alignedDev) WriteAt(b []byte, off int64) (int, error) {
	if off%alignSize != 0 || len(b)%alignSize != 0 {
		d.t.Errorf("gravação desalinhada: off=%d len=%d", off, len(b))
	}
	return d.File.WriteAt(b, off)
}

// leitor FAT32 mínimo (só para o teste): devolve caminho -> conteúdo e os nomes 8.3.
func readFAT32(t *testing.T, img *os.File) (map[string][]byte, map[string]string) {
	t.Helper()
	le := binary.LittleEndian
	mbr := make([]byte, 512)
	img.ReadAt(mbr, 0)
	if mbr[510] != 0x55 || mbr[511] != 0xAA || mbr[446] != 0x80 || mbr[450] != 0x0C {
		t.Fatalf("MBR inválido: % x", mbr[446:462])
	}
	pstart := int64(le.Uint32(mbr[454:])) * 512
	bs := make([]byte, 512)
	img.ReadAt(bs, pstart)
	if string(bs[82:90]) != "FAT32   " || bs[510] != 0x55 {
		t.Fatal("setor de boot FAT32 inválido")
	}
	spc := int64(bs[13])
	res := int64(le.Uint16(bs[14:]))
	fatSz := int64(le.Uint32(bs[36:]))
	rootC := le.Uint32(bs[44:])
	fat := make([]byte, fatSz*512)
	img.ReadAt(fat, pstart+res*512)
	fat2 := make([]byte, fatSz*512)
	img.ReadAt(fat2, pstart+(res+fatSz)*512)
	if !bytes.Equal(fat, fat2) {
		t.Fatal("as duas FATs diferem")
	}
	data := pstart + (res+2*fatSz)*512
	if data%(1<<20) != 0 {
		t.Errorf("área de dados não alinhada a 1 MiB: %d", data)
	}
	cs := spc * 512
	chain := func(c uint32, size int64) []byte {
		var out []byte
		for c >= 2 && c < 0x0FFFFFF8 {
			b := make([]byte, cs)
			img.ReadAt(b, data+int64(c-2)*cs)
			out = append(out, b...)
			c = le.Uint32(fat[c*4:]) & 0x0FFFFFFF
		}
		if size >= 0 {
			if int64(len(out)) < size {
				t.Fatalf("cadeia curta")
			}
			out = out[:size]
		}
		return out
	}
	files := map[string][]byte{}
	shorts := map[string]string{}
	var walk func(c uint32, prefix string)
	walk = func(c uint32, prefix string) {
		d := chain(c, -1)
		var lfn []uint16
		for i := 0; i+32 <= len(d); i += 32 {
			e := d[i : i+32]
			if e[0] == 0 {
				break
			}
			if e[0] == 0xE5 {
				continue
			}
			if e[11] == 0x0F {
				var part []uint16
				for _, p := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
					part = append(part, le.Uint16(e[p:]))
				}
				if e[0]&0x40 != 0 {
					lfn = nil
				}
				lfn = append(part, lfn...)
				continue
			}
			if e[11]&0x08 != 0 {
				continue // rótulo
			}
			short := strings.TrimSpace(string(e[0:8]))
			if ext := strings.TrimSpace(string(e[8:11])); ext != "" {
				short += "." + ext
			}
			if short == "." || short == ".." {
				lfn = nil
				continue
			}
			name := short
			if e[12]&0x08 != 0 {
				b, x, _ := strings.Cut(short, ".")
				name = strings.ToLower(b)
				if x != "" {
					name += "." + x
				}
			}
			if e[12]&0x10 != 0 {
				b, x, _ := strings.Cut(name, ".")
				name = b + "." + strings.ToLower(x)
			}
			if lfn != nil {
				n := 0
				for n < len(lfn) && lfn[n] != 0 {
					n++
				}
				name = string(utf16.Decode(lfn[:n]))
			}
			lfn = nil
			first := uint32(le.Uint16(e[20:]))<<16 | uint32(le.Uint16(e[26:]))
			p := prefix + name
			shorts[p] = short
			if e[11]&0x10 != 0 {
				files[p+"/"] = nil
				walk(first, p+"/")
			} else {
				files[p] = chain(first, int64(le.Uint32(e[28:])))
			}
		}
	}
	walk(rootC, "")
	return files, shorts
}

func TestBigFAT32(t *testing.T) {
	devPath := filepath.Join(t.TempDir(), "dev.img")
	const devSize = 700 << 20
	f, err := os.Create(devPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// lixo de um pendrive antigo no começo e no fim
	junk := bytes.Repeat([]byte{0xAB}, 1<<20)
	f.WriteAt(junk, 0)
	f.WriteAt(junk, devSize-(1<<20))
	big := make([]byte, 5<<20+123)
	rand.Read(big)
	src := bytes.NewReader(big)
	want := map[string][]byte{
		"bootmgr.efi":                   []byte("bootmgr"),
		"efi/boot/bootx64.efi":          bytes.Repeat([]byte{1}, 9000),
		"sources/install.swm":           big[:3<<20],
		"sources/Arquivo Longo ção.txt": []byte("nome longo com acentos"),
		"sources/vazio.txt":             {},
		"SETUP.EXE":                     []byte("MZ"),
		"autounattend.xml":              []byte("<unattend/>"),
	}
	var files []fsFile
	for p, d := range want {
		files = append(files, fsFile{Path: p, Size: int64(len(d)), Segs: []segment{{Data: d}}})
	}
	// arquivo feito de vários trechos (como as partes .swm)
	files = append(files, fsFile{Path: "sources/install2.swm", Size: 208 + int64(len(big)) - 1000,
		Segs: []segment{{Data: big[:208]}, {R: src, Off: 1000, N: int64(len(big)) - 2000}, {Data: big[len(big)-1000:]}}})
	want["sources/install2.swm"] = append(append(append([]byte{}, big[:208]...), big[1000:len(big)-1000]...), big[len(big)-1000:]...)
	files = append(files, fsFile{Path: "support/vazia", Dir: true})
	for i := 0; i < 300; i++ { // pasta com muitas entradas (mais de um cluster)
		n := fmt.Sprintf("sources/pt-br/arquivo-numero-%03d.mui", i)
		d := []byte(n)
		want[n] = d
		files = append(files, fsFile{Path: n, Size: int64(len(d)), Segs: []segment{{Data: d}}})
	}
	plan, err := planBigFAT(devSize-2<<20, "WIN11", files, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := &progress{}
	if err := plan.write(alignedDev{f, t}, devSize, p); err != nil {
		t.Fatal(err)
	}
	got, shorts := readFAT32(t, f)
	for k, v := range want {
		if g, ok := got[k]; !ok || !bytes.Equal(g, v) {
			t.Errorf("%s: conteúdo difere/ausente (%d bytes lidos)", k, len(g))
		}
	}
	if _, ok := got["support/vazia/"]; !ok {
		t.Error("pasta vazia não criada")
	}
	if shorts["efi/boot/bootx64.efi"] != "BOOTX64.EFI" || shorts["efi"] != "EFI" || shorts["efi/boot"] != "BOOT" {
		t.Errorf("nomes 8.3 do boot UEFI errados: %q %q %q", shorts["efi"], shorts["efi/boot"], shorts["efi/boot/bootx64.efi"])
	}
	tail := make([]byte, 1<<20)
	f.ReadAt(tail, devSize-(1<<20))
	if !bytes.Equal(tail, make([]byte, 1<<20)) {
		t.Error("o fim do pendrive deveria ter sido limpo")
	}
	// corrupção depois da gravação: a verificação precisa acusar
	var n *bigNode
	for _, x := range plan.order {
		if x.clusters > 0 {
			n = x
		}
	}
	f.WriteAt([]byte{0xFF}, plan.clusterOff(n.first))
	if err := plan.verify(f, p); err == nil {
		t.Fatal("corrupção não detectada pela verificação")
	}
}

func TestBigFAT32TooSmall(t *testing.T) {
	files := []fsFile{{Path: "a.bin", Size: 400 << 20}}
	if _, err := planBigFAT(300<<20, "WIN11", files, time.Now()); err == nil || !strings.Contains(err.Error(), "não cabem") {
		t.Fatalf("esperava erro de espaço, veio %v", err)
	}
	if _, err := planBigFAT(100<<20, "WIN11", nil, time.Now()); err == nil {
		t.Fatal("volume pequeno demais para FAT32 deveria falhar")
	}
}

func TestShort83(t *testing.T) {
	used := map[string]bool{}
	s, low, lfn := short83("bootx64.efi", used)
	if string(s[:]) != "BOOTX64 EFI" || low != 0x18 || lfn {
		t.Fatalf("%q %x %v", s, low, lfn)
	}
	s, _, lfn = short83("Setup.exe", used)
	if string(s[:]) != "SETUP   EXE" || !lfn {
		t.Fatalf("%q %v", s, lfn)
	}
	s, _, lfn = short83("autounattend.xml", used)
	if !lfn || !strings.Contains(string(s[:]), "~") {
		t.Fatalf("%q %v", s, lfn)
	}
}
