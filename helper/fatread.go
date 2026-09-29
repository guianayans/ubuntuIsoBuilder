package main

// Leitor mínimo de FAT32 (só leitura): acha um arquivo pelo caminho direto no
// dispositivo, sem montar nada. Usado para reconhecer o pendrive gravado pelo
// isoBuilder (uab-stick.json) antes de mandar a placa-mãe ligar por ele.

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode/utf16"
)

// alignedReader: discos "crus" do Windows (\\.\PhysicalDriveN) só aceitam leituras
// alinhadas ao setor; lê sempre em blocos de 4 KiB (vale para setores de 512 e 4096).
type alignedReader struct{ r io.ReaderAt }

func (a alignedReader) ReadAt(p []byte, off int64) (int, error) {
	const blk = 4096
	start := off &^ (blk - 1)
	end := (off + int64(len(p)) + blk - 1) &^ (blk - 1)
	buf := make([]byte, end-start)
	n, err := a.r.ReadAt(buf, start)
	if n < int(off-start) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	c := copy(p, buf[off-start:n])
	if c < len(p) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return c, err
	}
	return c, nil
}

type fatVol struct {
	r         io.ReaderAt
	fatStart  int64 // bytes (absolutos no dispositivo)
	dataStart int64
	clusBytes int64
	root      uint32
	clusters  uint32
}

// openFAT32 lê o setor de boot da partição que começa em base (bytes).
func openFAT32(r io.ReaderAt, base int64) (*fatVol, error) {
	bs := make([]byte, 512)
	if _, err := r.ReadAt(bs, base); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	bps, spc := int64(le.Uint16(bs[11:])), int64(bs[13])
	reserved, nfats := int64(le.Uint16(bs[14:])), int64(bs[16])
	fatSz, total := int64(le.Uint32(bs[36:])), int64(le.Uint32(bs[32:]))
	if bs[510] != 0x55 || bs[511] != 0xAA || le.Uint16(bs[22:]) != 0 || fatSz == 0 ||
		(bps != 512 && bps != 1024 && bps != 2048 && bps != 4096) || spc == 0 || spc&(spc-1) != 0 || nfats == 0 {
		return nil, errors.New("não é FAT32")
	}
	v := &fatVol{r: r, fatStart: base + reserved*bps, clusBytes: bps * spc, root: le.Uint32(bs[44:])}
	v.dataStart = v.fatStart + nfats*fatSz*bps
	if total > 0 {
		v.clusters = uint32((total - reserved - nfats*fatSz) / spc)
	}
	return v, nil
}

func (v *fatVol) next(c uint32) (uint32, error) {
	b := make([]byte, 4)
	if _, err := v.r.ReadAt(b, v.fatStart+int64(c)*4); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b) & 0x0FFFFFFF, nil
}

// chain lê a cadeia de clusters a partir de first (no máximo max bytes).
func (v *fatVol) chain(first uint32, max int64) ([]byte, error) {
	var out []byte
	for c, n := first, 0; c >= 2 && c < 0x0FFFFFF8 && int64(len(out)) < max; n++ {
		if v.clusters > 0 && c >= v.clusters+2 || n > 1<<20 {
			return nil, errors.New("cadeia FAT inválida")
		}
		b := make([]byte, v.clusBytes)
		if _, err := v.r.ReadAt(b, v.dataStart+int64(c-2)*v.clusBytes); err != nil {
			return nil, err
		}
		out = append(out, b...)
		var err error
		if c, err = v.next(c); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type fatEntry struct {
	name  string
	dir   bool
	first uint32
	size  uint32
}

func fatEntries(dir []byte) []fatEntry {
	le := binary.LittleEndian
	var out []fatEntry
	var lfn []uint16
	for i := 0; i+32 <= len(dir); i += 32 {
		e := dir[i : i+32]
		switch {
		case e[0] == 0x00:
			return out
		case e[0] == 0xE5:
			lfn = nil
			continue
		case e[11] == 0x0F: // pedaço do nome longo (vêm do último para o primeiro)
			seq := int(e[0] & 0x1F)
			if seq == 0 {
				lfn = nil
				continue
			}
			var part []uint16
			for _, o := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
				part = append(part, le.Uint16(e[o:]))
			}
			if e[0]&0x40 != 0 || len(lfn) < seq*13 {
				n := make([]uint16, seq*13)
				copy(n, lfn)
				lfn = n
			}
			copy(lfn[(seq-1)*13:], part)
			continue
		case e[11]&0x08 != 0: // rótulo do volume
			lfn = nil
			continue
		}
		name := ""
		if lfn != nil {
			for j, c := range lfn {
				if c == 0 || c == 0xFFFF {
					lfn = lfn[:j]
					break
				}
			}
			name = string(utf16.Decode(lfn))
		} else {
			base, ext := strings.TrimRight(string(e[0:8]), " "), strings.TrimRight(string(e[8:11]), " ")
			name = base
			if ext != "" {
				name += "." + ext
			}
		}
		lfn = nil
		out = append(out, fatEntry{name: name, dir: e[11]&0x10 != 0,
			first: uint32(le.Uint16(e[20:]))<<16 | uint32(le.Uint16(e[26:])), size: le.Uint32(e[28:])})
	}
	return out
}

// readFile devolve o conteúdo de "PASTA/arquivo" (sem diferenciar maiúsculas).
func (v *fatVol) readFile(p string, max int64) ([]byte, error) {
	cur := v.root
	parts := strings.Split(strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/"), "/")
	for i, name := range parts {
		dir, err := v.chain(cur, 4<<20)
		if err != nil {
			return nil, err
		}
		var hit *fatEntry
		for _, e := range fatEntries(dir) {
			if strings.EqualFold(e.name, name) {
				e := e
				hit = &e
				break
			}
		}
		if hit == nil {
			return nil, errors.New("arquivo não encontrado: " + p)
		}
		last := i == len(parts)-1
		if last == hit.dir {
			return nil, errors.New("caminho inválido: " + p)
		}
		if !last {
			cur = hit.first
			continue
		}
		if int64(hit.size) > max {
			return nil, errors.New("arquivo grande demais: " + p)
		}
		if hit.size == 0 {
			return []byte{}, nil
		}
		data, err := v.chain(hit.first, int64(hit.size))
		if err != nil {
			return nil, err
		}
		if len(data) < int(hit.size) {
			return nil, errors.New("arquivo truncado: " + p)
		}
		return data[:hit.size], nil
	}
	return nil, errors.New("caminho vazio")
}
