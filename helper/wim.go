package main

// Formato WIM (Windows Imaging File Format, especificação pública da Microsoft):
// leitura do cabeçalho, da tabela de recursos ("lookup table") e do XML, e a
// DIVISÃO em partes .swm (install.swm, install2.swm, …) para caber no FAT32.
//
// A divisão NÃO recomprime nada: os recursos (já comprimidos) são copiados
// byte a byte; só mudam o cabeçalho de cada parte, a tabela de recursos (posição
// e número da parte de cada recurso) e o total de bytes no XML. Mesmo esquema
// das partes geradas pelo DISM (/Split-Image) e pelo wimlib:
//   - todas as partes têm o mesmo GUID, o nº da parte e o total de partes;
//   - a parte 1 leva os recursos de metadados (um por imagem) e o de boot;
//   - cada parte tem a tabela só com os recursos que estão nela, e o XML inteiro.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	wimHeaderSize  = 208
	wimEntrySize   = 50
	wimResFree     = 0x01
	wimResMetadata = 0x02
	wimResCompr    = 0x04
	wimResSpanned  = 0x08
	wimResSolid    = 0x10
	wimHdrSpanned  = 0x00000008

	// limite por parte: abaixo dos 4 GiB do FAT32 com folga
	wimMaxPart = 4000 << 20
)

type wimResHdr struct {
	Size  int64 // tamanho no arquivo (comprimido)
	Flags byte
	Off   int64
	Orig  int64 // tamanho original
}

func parseResHdr(b []byte) wimResHdr {
	le := binary.LittleEndian
	v := le.Uint64(b)
	return wimResHdr{Size: int64(v & 0x00FFFFFFFFFFFFFF), Flags: byte(v >> 56), Off: int64(le.Uint64(b[8:])), Orig: int64(le.Uint64(b[16:]))}
}

func (r wimResHdr) put(b []byte) {
	le := binary.LittleEndian
	le.PutUint64(b, uint64(r.Size)|uint64(r.Flags)<<56)
	le.PutUint64(b[8:], uint64(r.Off))
	le.PutUint64(b[16:], uint64(r.Orig))
}

type wimHeader struct {
	raw        []byte
	Version    uint32
	Flags      uint32
	Part       uint16
	TotalParts uint16
	ImageCount uint32
	Lookup     wimResHdr
	XML        wimResHdr
	Boot       wimResHdr
	BootIndex  uint32
	Integrity  wimResHdr
}

func (h *wimHeader) GUID() []byte { return h.raw[24:40] }

func readWIMHeader(r io.ReaderAt) (*wimHeader, error) {
	b := make([]byte, wimHeaderSize)
	if _, err := r.ReadAt(b, 0); err != nil {
		return nil, fmt.Errorf("não foi possível ler o WIM: %w", err)
	}
	if string(b[:8]) != "MSWIM\x00\x00\x00" {
		return nil, errors.New("arquivo não é uma imagem WIM")
	}
	le := binary.LittleEndian
	if le.Uint32(b[8:]) != wimHeaderSize {
		return nil, errors.New("cabeçalho WIM de tamanho inesperado")
	}
	return &wimHeader{raw: b, Version: le.Uint32(b[12:]), Flags: le.Uint32(b[16:]),
		Part: le.Uint16(b[40:]), TotalParts: le.Uint16(b[42:]), ImageCount: le.Uint32(b[44:]),
		Lookup: parseResHdr(b[48:]), XML: parseResHdr(b[72:]), Boot: parseResHdr(b[96:]),
		BootIndex: le.Uint32(b[120:]), Integrity: parseResHdr(b[124:])}, nil
}

type wimEntry struct {
	Res      wimResHdr
	Part     uint16
	RefCount uint32
	Hash     [20]byte
}

func readWIMLookup(r io.ReaderAt, h *wimHeader) ([]wimEntry, error) {
	if h.Lookup.Flags&wimResCompr != 0 {
		return nil, errors.New("tabela de recursos do WIM comprimida (formato não suportado)")
	}
	if h.Lookup.Size%wimEntrySize != 0 || h.Lookup.Size > 256<<20 {
		return nil, errors.New("tabela de recursos do WIM inválida")
	}
	b := make([]byte, h.Lookup.Size)
	if _, err := r.ReadAt(b, h.Lookup.Off); err != nil {
		return nil, fmt.Errorf("falha ao ler a tabela do WIM: %w", err)
	}
	le := binary.LittleEndian
	out := make([]wimEntry, 0, len(b)/wimEntrySize)
	for i := 0; i+wimEntrySize <= len(b); i += wimEntrySize {
		e := wimEntry{Res: parseResHdr(b[i:]), Part: le.Uint16(b[i+24:]), RefCount: le.Uint32(b[i+26:])}
		copy(e.Hash[:], b[i+30:i+50])
		out = append(out, e)
	}
	return out, nil
}

func encodeWIMLookup(es []wimEntry) []byte {
	b := make([]byte, len(es)*wimEntrySize)
	le := binary.LittleEndian
	for i, e := range es {
		o := b[i*wimEntrySize:]
		e.Res.put(o)
		le.PutUint16(o[24:], e.Part)
		le.PutUint32(o[26:], e.RefCount)
		copy(o[30:50], e.Hash[:])
	}
	return b
}

// readWIMXML devolve o XML (UTF-16LE no arquivo) como texto.
func readWIMXML(r io.ReaderAt, h *wimHeader) (string, error) {
	if h.XML.Size <= 0 || h.XML.Size > 64<<20 || h.XML.Flags&wimResCompr != 0 {
		return "", errors.New("XML do WIM ausente ou em formato não suportado")
	}
	b := make([]byte, h.XML.Size)
	if _, err := r.ReadAt(b, h.XML.Off); err != nil {
		return "", err
	}
	return decodeUTF16LE(b), nil
}

func decodeUTF16LE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	s := string(utf16.Decode(u))
	return strings.TrimPrefix(s, "\ufeff")
}

func encodeUTF16LE(s string) []byte {
	u := utf16.Encode([]rune("\ufeff" + s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	return b
}

type wimImage struct {
	Index     int
	Name      string
	EditionID string
	Arch      int // 0 = x86, 9 = x64, 12 = arm64
	Build     int
	Lang      string
}

var (
	reImage = regexp.MustCompile(`(?s)<IMAGE INDEX="(\d+)">(.*?)</IMAGE>`)
	reTag   = func(t string) *regexp.Regexp { return regexp.MustCompile(`(?s)<` + t + `>(.*?)</` + t + `>`) }
	reName  = reTag("NAME")
	reEd    = reTag("EDITIONID")
	reArch  = reTag("ARCH")
	reBuild = reTag("BUILD")
	reDef   = reTag("DEFAULT")
)

func wimImages(xml string) []wimImage {
	var out []wimImage
	first := func(re *regexp.Regexp, s string) string {
		if m := re.FindStringSubmatch(s); m != nil {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	for _, m := range reImage.FindAllStringSubmatch(xml, -1) {
		idx, _ := strconv.Atoi(m[1])
		body := m[2]
		// o NAME da imagem vem antes de qualquer NAME aninhado? Não há NAME aninhado
		// no XML do Windows, mas "DISPLAYNAME" contém "NAME": o regexp exige <NAME>.
		arch, _ := strconv.Atoi(first(reArch, body))
		build, _ := strconv.Atoi(first(reBuild, body))
		out = append(out, wimImage{Index: idx, Name: first(reName, body), EditionID: first(reEd, body),
			Arch: arch, Build: build, Lang: first(reDef, body)})
	}
	return out
}

// ------------------------------------------------------------------ divisão

// segment: pedaço do conteúdo de um arquivo de saída (bytes prontos ou trecho de um leitor).
type segment struct {
	Data []byte
	R    io.ReaderAt
	Off  int64
	N    int64
}

func (s segment) Len() int64 {
	if s.R == nil {
		return int64(len(s.Data))
	}
	return s.N
}

type wimPart struct {
	Name string
	Size int64
	Segs []segment
}

// swmName: install.swm, install2.swm, install3.swm… (o padrão do Setup/DISM).
func swmName(base string, n int) string {
	if n == 1 {
		return base + ".swm"
	}
	return fmt.Sprintf("%s%d.swm", base, n)
}

var reTotalBytes = regexp.MustCompile(`<TOTALBYTES>\d+</TOTALBYTES>`)

// planSplit calcula as partes .swm (conteúdo exato) sem gravar nada.
func planSplit(r io.ReaderAt, base string, maxPart int64) ([]wimPart, error) {
	h, err := readWIMHeader(r)
	if err != nil {
		return nil, err
	}
	if h.TotalParts != 1 || h.Part != 1 || h.Flags&wimHdrSpanned != 0 {
		return nil, errors.New("o WIM já está dividido")
	}
	entries, err := readWIMLookup(r, h)
	if err != nil {
		return nil, err
	}
	xml, err := readWIMXML(r, h)
	if err != nil {
		return nil, err
	}
	var meta, data []wimEntry
	for _, e := range entries {
		switch {
		case e.Res.Flags&wimResFree != 0:
			continue
		case e.Res.Flags&(wimResSolid|wimResSpanned) != 0 || e.Res.Size == 0 && e.Res.Orig != 0:
			return nil, errors.New("WIM com recursos sólidos (formato ESD) não pode ser dividido")
		case e.Res.Size > maxPart/2:
			return nil, errors.New("WIM com um recurso grande demais para dividir")
		case e.Res.Flags&wimResMetadata != 0:
			meta = append(meta, e)
		default:
			data = append(data, e)
		}
	}
	if uint32(len(meta)) != h.ImageCount {
		return nil, fmt.Errorf("WIM inconsistente: %d imagens e %d metadados", h.ImageCount, len(meta))
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Res.Off < data[j].Res.Off }) // leitura sequencial

	xmlLen := func(total int64) int64 {
		return int64(len(encodeUTF16LE(reTotalBytes.ReplaceAllString(xml, fmt.Sprintf("<TOTALBYTES>%d</TOTALBYTES>", total)))))
	}
	// Tamanho do XML muda com o nº de dígitos do total: reserva o maior (20 dígitos).
	xmlMax := xmlLen(math.MaxInt64)

	// distribui: parte 1 = metadados + dados que couberem; depois as outras.
	type bucket struct{ es []wimEntry }
	var parts []bucket
	cur := bucket{es: append([]wimEntry(nil), meta...)}
	curSize := int64(wimHeaderSize) + xmlMax
	for _, e := range meta {
		curSize += e.Res.Size + wimEntrySize
	}
	if curSize > maxPart {
		return nil, errors.New("metadados do WIM grandes demais")
	}
	for _, e := range data {
		if curSize+e.Res.Size+wimEntrySize > maxPart && len(cur.es) > 0 {
			parts = append(parts, cur)
			cur = bucket{}
			curSize = int64(wimHeaderSize) + xmlMax
		}
		cur.es = append(cur.es, e)
		curSize += e.Res.Size + wimEntrySize
	}
	parts = append(parts, cur)
	if len(parts) > 0xFFFF {
		return nil, errors.New("partes demais")
	}

	out := make([]wimPart, 0, len(parts))
	for i, pb := range parts {
		pn := uint16(i + 1)
		var segs []segment
		off := int64(wimHeaderSize)
		table := make([]wimEntry, 0, len(pb.es))
		var boot wimResHdr
		for _, e := range pb.es {
			ne := e
			ne.Part = pn
			ne.Res.Off = off
			ne.Res.Flags &^= wimResSpanned
			if e.Res.Flags&wimResMetadata != 0 && pn == 1 && e.Res.Off == h.Boot.Off && e.Res.Size == h.Boot.Size && h.Boot.Size > 0 {
				boot = ne.Res
			}
			table = append(table, ne)
			// junta trechos contíguos do WIM original (leitura em blocos grandes)
			if n := len(segs); n > 0 && segs[n-1].R != nil && segs[n-1].Off+segs[n-1].N == e.Res.Off {
				segs[n-1].N += e.Res.Size
			} else {
				segs = append(segs, segment{R: r, Off: e.Res.Off, N: e.Res.Size})
			}
			off += e.Res.Size
		}
		lt := encodeWIMLookup(table)
		lookup := wimResHdr{Size: int64(len(lt)), Flags: h.Lookup.Flags &^ wimResCompr, Off: off, Orig: int64(len(lt))}
		off += int64(len(lt))
		xb := encodeUTF16LE(reTotalBytes.ReplaceAllString(xml, fmt.Sprintf("<TOTALBYTES>%d</TOTALBYTES>", off)))
		xh := wimResHdr{Size: int64(len(xb)), Flags: h.XML.Flags &^ wimResCompr, Off: off, Orig: int64(len(xb))}
		off += int64(len(xb))

		hdr := append([]byte(nil), h.raw...)
		le := binary.LittleEndian
		le.PutUint32(hdr[16:], h.Flags|wimHdrSpanned)
		le.PutUint16(hdr[40:], pn)
		le.PutUint16(hdr[42:], uint16(len(parts)))
		lookup.put(hdr[48:])
		xh.put(hdr[72:])
		boot.put(hdr[96:]) // só a parte 1 tem o recurso de boot
		wimResHdr{}.put(hdr[124:])
		for k := 148; k < wimHeaderSize; k++ {
			hdr[k] = 0
		}
		all := append([]segment{{Data: hdr}}, segs...)
		all = append(all, segment{Data: lt}, segment{Data: xb})
		out = append(out, wimPart{Name: swmName(base, int(pn)), Size: off, Segs: all})
	}
	return out, nil
}

// segReader lê a concatenação de segmentos (para gravar em arquivo/testes).
type segReader struct {
	segs []segment
	i    int
	pos  int64
}

func newSegReader(s []segment) *segReader { return &segReader{segs: s} }

func (s *segReader) Read(p []byte) (int, error) {
	for s.i < len(s.segs) {
		sg := s.segs[s.i]
		if s.pos >= sg.Len() {
			s.i++
			s.pos = 0
			continue
		}
		n := int64(len(p))
		if rem := sg.Len() - s.pos; n > rem {
			n = rem
		}
		if sg.R == nil {
			copy(p, sg.Data[s.pos:s.pos+n])
		} else if m, err := sg.R.ReadAt(p[:n], sg.Off+s.pos); int64(m) < n {
			if err == nil || err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return m, err
		}
		s.pos += n
		return int(n), nil
	}
	return 0, io.EOF
}

// ------------------------------------------------------------------ conferência

// checkSplit relê as partes geradas (como o Setup faria: cabeçalhos, GUID,
// tabelas juntadas) e confere que cada recurso do WIM original está em alguma
// parte, com os mesmos bytes.
func checkSplit(orig io.ReaderAt, parts []io.ReaderAt) error {
	oh, err := readWIMHeader(orig)
	if err != nil {
		return err
	}
	oe, err := readWIMLookup(orig, oh)
	if err != nil {
		return err
	}
	type loc struct {
		part int
		res  wimResHdr
	}
	found := map[[20]byte]loc{}
	var metaOrder [][20]byte
	for i, pr := range parts {
		h, err := readWIMHeader(pr)
		if err != nil {
			return fmt.Errorf("parte %d: %w", i+1, err)
		}
		if int(h.Part) != i+1 || int(h.TotalParts) != len(parts) || h.Flags&wimHdrSpanned == 0 {
			return fmt.Errorf("parte %d: numeração errada (%d/%d)", i+1, h.Part, h.TotalParts)
		}
		if !bytes.Equal(h.GUID(), oh.GUID()) || h.ImageCount != oh.ImageCount {
			return fmt.Errorf("parte %d: GUID/imagens diferentes", i+1)
		}
		if _, err := readWIMXML(pr, h); err != nil {
			return fmt.Errorf("parte %d: %w", i+1, err)
		}
		es, err := readWIMLookup(pr, h)
		if err != nil {
			return fmt.Errorf("parte %d: %w", i+1, err)
		}
		for _, e := range es {
			if int(e.Part) != i+1 {
				return fmt.Errorf("parte %d: recurso marcado na parte %d", i+1, e.Part)
			}
			if e.Res.Flags&wimResMetadata != 0 {
				if i != 0 {
					return fmt.Errorf("metadados fora da parte 1")
				}
				metaOrder = append(metaOrder, e.Hash)
			}
			found[e.Hash] = loc{i, e.Res}
		}
	}
	buf1, buf2 := make([]byte, 1<<20), make([]byte, 1<<20)
	mi := 0
	for _, e := range oe {
		if e.Res.Flags&wimResFree != 0 {
			continue
		}
		if e.Res.Flags&wimResMetadata != 0 {
			if mi >= len(metaOrder) || metaOrder[mi] != e.Hash {
				return errors.New("ordem dos metadados alterada")
			}
			mi++
		}
		l, ok := found[e.Hash]
		if !ok {
			return fmt.Errorf("recurso %x não está em nenhuma parte", e.Hash[:6])
		}
		if l.res.Size != e.Res.Size || l.res.Orig != e.Res.Orig || l.res.Flags != e.Res.Flags {
			return fmt.Errorf("recurso %x com tamanho/flags diferentes", e.Hash[:6])
		}
		for done := int64(0); done < e.Res.Size; {
			n := int64(len(buf1))
			if e.Res.Size-done < n {
				n = e.Res.Size - done
			}
			if _, err := orig.ReadAt(buf1[:n], e.Res.Off+done); err != nil {
				return err
			}
			if _, err := parts[l.part].ReadAt(buf2[:n], l.res.Off+done); err != nil {
				return err
			}
			if !bytes.Equal(buf1[:n], buf2[:n]) {
				return fmt.Errorf("recurso %x com conteúdo diferente", e.Hash[:6])
			}
			done += n
		}
	}
	if mi != len(metaOrder) {
		return errors.New("metadados a mais nas partes")
	}
	return nil
}

// segReaderAt: acesso aleatório à concatenação de segmentos (conferência/testes).
type segReaderAt struct {
	segs  []segment
	start []int64
	size  int64
}

func newSegReaderAt(s []segment) *segReaderAt {
	r := &segReaderAt{segs: s}
	for _, sg := range s {
		r.start = append(r.start, r.size)
		r.size += sg.Len()
	}
	return r
}

func (r *segReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	i := sort.Search(len(r.start), func(i int) bool { return r.start[i] > off }) - 1
	total := 0
	for ; i < len(r.segs) && len(p) > 0; i++ {
		sg := r.segs[i]
		in := off - r.start[i]
		n := sg.Len() - in
		if n > int64(len(p)) {
			n = int64(len(p))
		}
		if sg.R == nil {
			copy(p, sg.Data[in:in+n])
		} else if m, err := sg.R.ReadAt(p[:n], sg.Off+in); int64(m) < n {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return total + m, err
		}
		p = p[n:]
		off += n
		total += int(n)
	}
	if len(p) > 0 {
		return total, io.EOF
	}
	return total, nil
}
