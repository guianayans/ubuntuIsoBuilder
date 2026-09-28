package main

// Leitor mínimo de UDF (ECMA-167 / OSTA UDF 1.02–2.x), só leitura, em Go puro.
// A ISO do Windows tem o conteúdo de verdade em UDF: a parte ISO9660 só traz um
// README. Aqui basta listar a árvore e ler os arquivos (extensões contíguas).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf16"
)

const udfSector = 2048

// udfExtent: pedaço de um arquivo. Off < 0 = trecho não gravado (lê como zeros).
type udfExtent struct {
	Off int64 // posição na ISO (bytes)
	Len int64
}

type UDFFile struct {
	Path    string // "sources/install.wim" (sem barra inicial)
	Dir     bool
	Size    int64
	Extents []udfExtent
	Data    []byte // conteúdo embutido na própria entrada (arquivos minúsculos)
}

type UDF struct {
	r         io.ReaderAt
	partStart int64 // setor onde a partição começa
	Files     []*UDFFile
	byPath    map[string]*UDFFile
}

func udfTag(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }

func (u *UDF) sector(lba int64) ([]byte, error) {
	b := make([]byte, udfSector)
	if _, err := u.r.ReadAt(b, lba*udfSector); err != nil {
		return nil, err
	}
	return b, nil
}

// OpenUDF lê os descritores e a árvore inteira.
func OpenUDF(r io.ReaderAt) (*UDF, error) {
	u := &UDF{r: r, byPath: map[string]*UDFFile{}}
	avdp, err := u.sector(256)
	if err != nil {
		return nil, fmt.Errorf("não foi possível ler a ISO: %w", err)
	}
	if udfTag(avdp) != 2 {
		return nil, errors.New("a ISO não tem sistema de arquivos UDF (não é uma ISO do Windows?)")
	}
	le := binary.LittleEndian
	vdsLen, vdsLoc := le.Uint32(avdp[16:]), le.Uint32(avdp[20:])
	var fsdLBN uint32
	var fsdPart uint16
	gotLVD, gotPD := false, false
	for i := uint32(0); i < vdsLen/udfSector && i < 64; i++ {
		b, err := u.sector(int64(vdsLoc + i))
		if err != nil {
			return nil, err
		}
		switch udfTag(b) {
		case 5: // descritor de partição
			if !gotPD {
				u.partStart = int64(le.Uint32(b[188:]))
				gotPD = true
			}
		case 6: // volume lógico
			if le.Uint32(b[212:]) != udfSector {
				return nil, errors.New("UDF com bloco diferente de 2048 bytes não suportado")
			}
			fsdLBN = le.Uint32(b[252:])
			fsdPart = le.Uint16(b[256:])
			gotLVD = true
		case 8:
			i = vdsLen // terminador
		}
	}
	if !gotLVD || !gotPD {
		return nil, errors.New("UDF incompleto (sem descritor de volume/partição)")
	}
	_ = fsdPart
	fsd, err := u.sector(u.partStart + int64(fsdLBN))
	if err != nil {
		return nil, err
	}
	if udfTag(fsd) != 256 {
		return nil, errors.New("UDF: descritor do conjunto de arquivos não encontrado")
	}
	rootLBN := le.Uint32(fsd[400+4:])
	root, err := u.readEntry(rootLBN)
	if err != nil {
		return nil, err
	}
	root.Dir = true
	if err := u.walk(root, "", 0); err != nil {
		return nil, err
	}
	sort.Slice(u.Files, func(i, j int) bool { return u.Files[i].Path < u.Files[j].Path })
	return u, nil
}

// readEntry lê uma File Entry (261) ou Extended File Entry (266) no bloco lógico dado.
func (u *UDF) readEntry(lbn uint32) (*UDFFile, error) {
	b, err := u.sector(u.partStart + int64(lbn))
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	var eaLen, adLen uint32
	var base int
	switch udfTag(b) {
	case 261:
		eaLen, adLen, base = le.Uint32(b[168:]), le.Uint32(b[172:]), 176
	case 266:
		eaLen, adLen, base = le.Uint32(b[208:]), le.Uint32(b[212:]), 216
	default:
		return nil, fmt.Errorf("UDF: entrada inválida no bloco %d", lbn)
	}
	f := &UDFFile{Dir: b[16+11] == 4, Size: int64(le.Uint64(b[56:]))}
	flags := le.Uint16(b[16+18:])
	start := base + int(eaLen)
	if start+int(adLen) > udfSector {
		return nil, errors.New("UDF: entrada grande demais")
	}
	ads := b[start : start+int(adLen)]
	remaining := f.Size
	add := func(pos int64, n int64, typ uint32) {
		if n > remaining {
			n = remaining
		}
		if n <= 0 {
			return
		}
		if typ != 0 {
			pos = -1 // alocado mas não gravado: zeros
		}
		f.Extents = append(f.Extents, udfExtent{Off: pos, Len: n})
		remaining -= n
	}
	switch flags & 7 {
	case 0, 1: // short_ad (8 bytes) / long_ad (16 bytes)
		sz := 8
		if flags&7 == 1 {
			sz = 16
		}
		for guard := 0; guard < 10000; guard++ {
			cont := false
			for i := 0; i+sz <= len(ads); i += sz {
				raw := le.Uint32(ads[i:])
				n, typ := int64(raw&0x3FFFFFFF), raw>>30
				if n == 0 {
					break
				}
				pos := u.partStart + int64(le.Uint32(ads[i+4:]))
				if typ == 3 { // continuação: a lista segue em outro bloco
					nb, err := u.sector(pos)
					if err != nil {
						return nil, err
					}
					// Allocation Extent Descriptor (tag 258): lista começa em 24
					l := int(le.Uint32(nb[20:]))
					if udfTag(nb) != 258 || 24+l > udfSector {
						return nil, errors.New("UDF: lista de extensões inválida")
					}
					ads = nb[24 : 24+l]
					cont = true
					break
				}
				add(pos*udfSector, n, typ)
			}
			if !cont {
				break
			}
		}
	case 3: // dados embutidos
		if int64(len(ads)) < f.Size {
			return nil, errors.New("UDF: dados embutidos truncados")
		}
		f.Data = append([]byte(nil), ads[:f.Size]...)
		remaining = 0
	default:
		return nil, errors.New("UDF: tipo de alocação não suportado")
	}
	if remaining > 0 {
		return nil, errors.New("UDF: extensões não cobrem o tamanho do arquivo")
	}
	return f, nil
}

func (u *UDF) walk(dir *UDFFile, prefix string, depth int) error {
	if depth > 32 {
		return errors.New("UDF: pastas aninhadas demais")
	}
	if dir.Size > 64<<20 {
		return errors.New("UDF: pasta grande demais")
	}
	data := make([]byte, dir.Size)
	if _, err := io.ReadFull(u.Open(dir), data); err != nil {
		return err
	}
	le := binary.LittleEndian
	for i := 0; i+38 <= len(data); {
		if udfTag(data[i:]) != 257 { // File Identifier Descriptor
			return errors.New("UDF: pasta corrompida")
		}
		chars := data[i+18]
		lIdent := int(data[i+19])
		icbLBN := le.Uint32(data[i+20+4:])
		lIU := int(le.Uint16(data[i+36:]))
		nameStart := i + 38 + lIU
		if nameStart+lIdent > len(data) {
			return errors.New("UDF: pasta corrompida")
		}
		name := udfName(data[nameStart : nameStart+lIdent])
		recLen := (38 + lIU + lIdent + 3) &^ 3
		i += recLen
		if chars&8 != 0 || chars&4 != 0 || name == "" { // pai / apagado
			continue
		}
		f, err := u.readEntry(icbLBN)
		if err != nil {
			return err
		}
		if chars&2 != 0 {
			f.Dir = true
		}
		if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
			return fmt.Errorf("UDF: nome inválido %q", name)
		}
		f.Path = path.Join(prefix, name)
		u.Files = append(u.Files, f)
		u.byPath[strings.ToLower(f.Path)] = f
		if f.Dir {
			if err := u.walk(f, f.Path, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// udfName decodifica o "dstring" OSTA CS0 (8 ou 16 bits por caractere).
func udfName(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	switch b[0] {
	case 8:
		r := make([]rune, 0, len(b)-1)
		for _, c := range b[1:] {
			r = append(r, rune(c))
		}
		return string(r)
	case 16:
		u := make([]uint16, 0, (len(b)-1)/2)
		for i := 1; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u))
	}
	return ""
}

// Lookup acha um arquivo pelo caminho (sem diferenciar maiúsculas).
func (u *UDF) Lookup(p string) *UDFFile {
	return u.byPath[strings.ToLower(strings.Trim(p, "/"))]
}

// Open devolve um leitor do conteúdo do arquivo (também serve de ReaderAt).
func (u *UDF) Open(f *UDFFile) *udfReader { return &udfReader{r: u.r, f: f} }

type udfReader struct {
	r   io.ReaderAt
	f   *UDFFile
	pos int64
}

func (x *udfReader) Read(p []byte) (int, error) {
	n, err := x.ReadAt(p, x.pos)
	x.pos += int64(n)
	return n, err
}

func (x *udfReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= x.f.Size {
		return 0, io.EOF
	}
	if x.f.Data != nil {
		n := copy(p, x.f.Data[off:])
		if n < len(p) {
			return n, io.EOF
		}
		return n, nil
	}
	total := 0
	var base int64
	for _, e := range x.f.Extents {
		if len(p) == 0 {
			break
		}
		if off >= base+e.Len {
			base += e.Len
			continue
		}
		in := off - base
		n := int64(len(p))
		if n > e.Len-in {
			n = e.Len - in
		}
		if e.Off < 0 {
			for i := int64(0); i < n; i++ {
				p[i] = 0
			}
		} else if m, err := x.r.ReadAt(p[:n], e.Off+in); int64(m) < n {
			if err == nil || err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return total + m, err
		}
		total += int(n)
		p = p[n:]
		off += n
		base += e.Len
	}
	if len(p) > 0 {
		return total, io.EOF
	}
	return total, nil
}
