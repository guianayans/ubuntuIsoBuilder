package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"io"
	"strconv"
	"strings"
	"testing"
)

// makeWIM monta um WIM mínimo (recursos sem compressão, hash = SHA1 do conteúdo).
func makeWIM(t *testing.T, nData, nMeta int, size func(i int) int) ([]byte, [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(make([]byte, wimHeaderSize))
	var entries []wimEntry
	var blobs [][]byte
	add := func(data []byte, flags byte) {
		e := wimEntry{Res: wimResHdr{Size: int64(len(data)), Flags: flags, Off: int64(buf.Len()), Orig: int64(len(data))}, Part: 1, RefCount: 1}
		e.Hash = sha1.Sum(data)
		entries = append(entries, e)
		blobs = append(blobs, data)
		buf.Write(data)
	}
	for i := 0; i < nMeta; i++ {
		d := make([]byte, 3000+i)
		rand.Read(d)
		add(d, wimResMetadata)
	}
	for i := 0; i < nData; i++ {
		d := make([]byte, size(i))
		rand.Read(d)
		add(d, 0)
	}
	// embaralha a ordem da tabela (o WIM real não está em ordem de posição)
	entries[len(entries)-1], entries[nMeta] = entries[nMeta], entries[len(entries)-1]
	lt := encodeWIMLookup(entries)
	ltOff := int64(buf.Len())
	buf.Write(lt)
	xml := `<WIM><TOTALBYTES>123</TOTALBYTES>`
	for i := 1; i <= nMeta; i++ {
		xml += `<IMAGE INDEX="` + string(rune('0'+i)) + `"><NAME>Windows 11 Pro</NAME><WINDOWS><ARCH>9</ARCH><EDITIONID>Professional</EDITIONID><LANGUAGES><DEFAULT>pt-BR</DEFAULT></LANGUAGES><VERSION><BUILD>26200</BUILD></VERSION></WINDOWS></IMAGE>`
	}
	xml += `</WIM>`
	xb := encodeUTF16LE(xml)
	xOff := int64(buf.Len())
	buf.Write(xb)
	b := buf.Bytes()
	copy(b, "MSWIM\x00\x00\x00")
	le := binary.LittleEndian
	le.PutUint32(b[8:], wimHeaderSize)
	le.PutUint32(b[12:], 0x10d00)
	le.PutUint32(b[16:], 0x40082)
	le.PutUint32(b[20:], 32768)
	rand.Read(b[24:40])
	le.PutUint16(b[40:], 1)
	le.PutUint16(b[42:], 1)
	le.PutUint32(b[44:], uint32(nMeta))
	wimResHdr{Size: int64(len(lt)), Flags: wimResMetadata, Off: ltOff, Orig: int64(len(lt))}.put(b[48:])
	wimResHdr{Size: int64(len(xb)), Off: xOff, Orig: int64(len(xb))}.put(b[72:])
	wimResHdr{Size: 9999, Off: 1, Orig: 9999}.put(b[124:]) // "integridade": deve sumir nas partes
	return b, blobs
}

func TestWIMSplit(t *testing.T) {
	orig, _ := makeWIM(t, 60, 2, func(i int) int { return 1000 + i*997 })
	r := bytes.NewReader(orig)
	h, err := readWIMHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := readWIMXML(r, h)
	ims := wimImages(x)
	if len(ims) != 2 || ims[0].EditionID != "Professional" || ims[0].Arch != 9 || ims[0].Build != 26200 || ims[0].Lang != "pt-BR" {
		t.Fatalf("XML mal lido: %+v", ims)
	}
	parts, err := planSplit(r, "install", 400_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 3 {
		t.Fatalf("esperava várias partes, veio %d", len(parts))
	}
	var ras []io.ReaderAt
	for i, p := range parts {
		var b bytes.Buffer
		n, err := io.Copy(&b, newSegReader(p.Segs))
		if err != nil || n != p.Size {
			t.Fatalf("parte %d: %v %d/%d", i+1, err, n, p.Size)
		}
		if p.Size > 400_000 {
			t.Fatalf("parte %d passou do limite: %d", i+1, p.Size)
		}
		if want := swmName("install", i+1); p.Name != want {
			t.Fatalf("nome %s, esperado %s", p.Name, want)
		}
		pb := b.Bytes()
		ph, err := readWIMHeader(bytes.NewReader(pb))
		if err != nil {
			t.Fatal(err)
		}
		if ph.Integrity.Size != 0 || (i > 0 && ph.Boot.Size != 0) {
			t.Fatal("integridade/boot deveriam estar zerados")
		}
		px, _ := readWIMXML(bytes.NewReader(pb), ph)
		if !strings.Contains(px, "<TOTALBYTES>"+strconv.FormatInt(ph.XML.Off, 10)+"</TOTALBYTES>") {
			t.Fatalf("TOTALBYTES não atualizado na parte %d", i+1)
		}
		// cada recurso da parte: SHA1 do conteúdo = hash da tabela
		es, _ := readWIMLookup(bytes.NewReader(pb), ph)
		for _, e := range es {
			if sha1.Sum(pb[e.Res.Off:e.Res.Off+e.Res.Size]) != e.Hash {
				t.Fatalf("parte %d: recurso com conteúdo errado", i+1)
			}
		}
		ras = append(ras, bytes.NewReader(pb))
	}
	if err := checkSplit(r, ras); err != nil {
		t.Fatal(err)
	}
	// corrupção em uma parte precisa ser detectada
	var b bytes.Buffer
	io.Copy(&b, newSegReader(parts[1].Segs))
	bad := b.Bytes()
	bad[wimHeaderSize+10] ^= 0xFF
	ras[1] = bytes.NewReader(bad)
	if err := checkSplit(r, ras); err == nil {
		t.Fatal("corrupção não detectada")
	}
	// leitor aleatório = leitor sequencial
	ra := newSegReaderAt(parts[0].Segs)
	var seq bytes.Buffer
	io.Copy(&seq, newSegReader(parts[0].Segs))
	chunk := make([]byte, 5000)
	for _, off := range []int64{0, 100, 207, 208, 12345, int64(seq.Len()) - 5000} {
		if _, err := ra.ReadAt(chunk, off); err != nil || !bytes.Equal(chunk, seq.Bytes()[off:off+5000]) {
			t.Fatalf("ReadAt em %d difere (%v)", off, err)
		}
	}
}

func TestWIMSplitRejectsSolid(t *testing.T) {
	orig, _ := makeWIM(t, 3, 1, func(i int) int { return 100 })
	r := bytes.NewReader(orig)
	h, _ := readWIMHeader(r)
	es, _ := readWIMLookup(r, h)
	es[1].Res.Flags |= wimResSolid
	copy(orig[h.Lookup.Off:], encodeWIMLookup(es))
	if _, err := planSplit(bytes.NewReader(orig), "install", 1<<20); err == nil {
		t.Fatal("WIM sólido (ESD) deveria ser recusado")
	}
}
