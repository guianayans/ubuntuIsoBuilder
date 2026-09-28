package main

// FAT32 grande, gravado direto no pendrive em fluxo (sem montar nada e sem
// carregar os arquivos na memória): usado no pendrive do Windows, que tem ~8 GB
// de arquivos. Layout: tabela MBR com UMA partição FAT32 (tipo 0x0C, ativa)
// começando em 1 MiB; o UEFI de qualquer PC inicializa por \EFI\BOOT\BOOTX64.EFI.
// Os arquivos ficam contíguos; tudo é gravado em blocos alinhados a 4 KiB (a
// escrita bruta em disco no Windows/macOS exige alinhamento).

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	bigPartStart = 1 << 20 // partição começa em 1 MiB
	bigAlign     = 1 << 20
)

// fsFile: arquivo (ou pasta, se Dir) a gravar no FAT32.
type fsFile struct {
	Path string // "sources/boot.wim"
	Dir  bool
	Size int64
	Segs []segment
}

type bigNode struct {
	name     string
	dir      bool
	file     *fsFile
	children []*bigNode
	short    [11]byte
	lower    byte // bits NT: 0x08 nome em minúsculas, 0x10 extensão em minúsculas
	lfn      bool
	first    uint32
	clusters uint32
	sum      [32]byte
}

// bigClusterSize: tamanho de cluster padrão do Windows para FAT32.
func bigClusterSize(part int64) uint32 {
	switch {
	case part <= 8<<30:
		return 4096
	case part <= 16<<30:
		return 8192
	case part <= 32<<30:
		return 16384
	}
	return 32768
}

// short83 usa o próprio nome quando ele já é um 8.3 válido (o firmware UEFI mais
// simples procura por EFI/BOOT/BOOTX64.EFI em 8.3); senão gera um NOME~N.
func short83(name string, used map[string]bool) (s [11]byte, lower byte, needLFN bool) {
	valid := func(p string, max int) bool {
		if p == "" || len(p) > max {
			return p == "" && max == 3
		}
		for _, r := range p {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("_-~!#$%&'(){}^@`", r)) {
				return false
			}
		}
		return true
	}
	base, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		base, ext = name[:i], name[i+1:]
	}
	caseOK := func(p string) (byte, bool) { // tudo maiúsculo (0), tudo minúsculo (1) ou misto
		if strings.ToUpper(p) == p {
			return 0, true
		}
		if strings.ToLower(p) == p {
			return 1, true
		}
		return 0, false
	}
	if valid(base, 8) && valid(ext, 3) && !strings.Contains(base, ".") {
		key := strings.ToUpper(base) + "." + strings.ToUpper(ext)
		if !used[key] {
			used[key] = true
			copy(s[:], fmt.Sprintf("%-8s%-3s", strings.ToUpper(base), strings.ToUpper(ext)))
			bl, ok1 := caseOK(base)
			el, ok2 := caseOK(ext)
			if ok1 && ok2 {
				lower = bl*0x08 | el*0x10
				return s, lower, false
			}
			return s, 0, true
		}
	}
	return shortName(name, used), 0, true
}

func bigLFNCount(n *bigNode) int {
	if !n.lfn {
		return 0
	}
	return lfnCount(n.name)
}

type bigFAT struct {
	dev       rwAt
	partOff   int64 // bytes
	partSecs  uint32
	cs        uint32 // bytes por cluster
	reserved  uint32
	fatSz     uint32 // setores por FAT
	nClusters uint32
	dataOff   int64 // bytes (relativo ao início da partição)
	root      *bigNode
	next      uint32
	chainEnd  map[uint32]bool
	metaSum   [32]byte
	usedBytes int64
	fileBytes int64
	label     string
	now       time.Time
	order     []*bigNode // arquivos na ordem de gravação
	dirs      []*bigNode
}

// planBigFAT monta a árvore e o layout; não grava nada.
func planBigFAT(partSize int64, label string, files []fsFile, now time.Time) (*bigFAT, error) {
	if partSize > int64(0xFFFFFFFF)*512 {
		partSize = int64(0xFFFFFFFF) * 512 &^ (bigAlign - 1)
	}
	f := &bigFAT{partSecs: uint32(partSize / 512), cs: bigClusterSize(partSize), label: label, now: now,
		chainEnd: map[uint32]bool{}}
	clusSecs := f.cs / 512
	// tamanho da FAT pela fórmula da especificação; depois as áreas reservadas
	// crescem para os dados começarem alinhados a 1 MiB
	tmp1 := f.partSecs - 32
	tmp2 := (256*clusSecs + 2) / 2
	f.fatSz = (tmp1 + tmp2 - 1) / tmp2
	meta := uint32(32) + 2*f.fatSz
	alignSecs := uint32(bigAlign / 512)
	meta = (meta + alignSecs - 1) / alignSecs * alignSecs
	f.reserved = meta - 2*f.fatSz
	f.nClusters = (f.partSecs - meta) / clusSecs
	if f.nClusters < 65525 {
		return nil, errors.New("pendrive pequeno demais para FAT32")
	}
	if f.nClusters > 0x0FFFFFF5-2 {
		f.nClusters = 0x0FFFFFF5 - 2
	}
	if uint64(f.fatSz)*512/4 < uint64(f.nClusters)+2 {
		return nil, errors.New("erro interno no cálculo da FAT")
	}
	f.dataOff = int64(meta) * 512

	// árvore
	f.root = &bigNode{dir: true}
	find := func(cur *bigNode, name string) *bigNode {
		for _, c := range cur.children {
			if strings.EqualFold(c.name, name) {
				return c
			}
		}
		return nil
	}
	for i := range files {
		fl := &files[i]
		parts := strings.Split(strings.Trim(path.Clean("/"+fl.Path), "/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		cur := f.root
		for k, p := range parts {
			last := k == len(parts)-1
			nx := find(cur, p)
			if nx == nil {
				nx = &bigNode{name: p, dir: !last || fl.Dir}
				cur.children = append(cur.children, nx)
			}
			if last && !fl.Dir {
				if nx.dir {
					return nil, fmt.Errorf("conflito de nome: %s", fl.Path)
				}
				if fl.Size > 0xFFFFFFFF {
					return nil, fmt.Errorf("%s tem mais de 4 GB (limite do FAT32)", fl.Path)
				}
				nx.file = fl
			} else if !nx.dir {
				return nil, fmt.Errorf("conflito de nome: %s", fl.Path)
			}
			cur = nx
		}
	}
	var assign func(n *bigNode)
	assign = func(n *bigNode) {
		sort.SliceStable(n.children, func(i, j int) bool { return strings.ToLower(n.children[i].name) < strings.ToLower(n.children[j].name) })
		used := map[string]bool{}
		for _, c := range n.children {
			c.short, c.lower, c.lfn = short83(c.name, used)
			if c.dir {
				f.dirs = append(f.dirs, c)
				assign(c)
			} else {
				f.order = append(f.order, c)
			}
		}
	}
	f.dirs = append(f.dirs, f.root)
	assign(f.root)

	// alocação contígua: todas as pastas primeiro, depois os arquivos na ordem
	f.next = 2
	allocN := func(n *bigNode, bytes int64) error {
		n.clusters = uint32((bytes + int64(f.cs) - 1) / int64(f.cs))
		if n.clusters == 0 {
			return nil
		}
		if uint64(f.next-2)+uint64(n.clusters) > uint64(f.nClusters) {
			return errors.New("os arquivos não cabem no pendrive")
		}
		n.first = f.next
		f.next += n.clusters
		f.chainEnd[n.first+n.clusters-1] = true
		return nil
	}
	for _, d := range f.dirs {
		entries := 2
		if d == f.root {
			entries = 1 // rótulo
		}
		for _, c := range d.children {
			entries += bigLFNCount(c) + 1
		}
		entries++
		if entries > 65536 {
			return nil, errors.New("pasta com arquivos demais para FAT32")
		}
		if err := allocN(d, int64(entries)*32); err != nil {
			return nil, err
		}
	}
	for _, n := range f.order {
		if err := allocN(n, n.file.Size); err != nil {
			return nil, err
		}
		f.fileBytes += n.file.Size
	}
	f.usedBytes = int64(f.next-2) * int64(f.cs)
	return f, nil
}

func (f *bigFAT) clusterOff(c uint32) int64 {
	return f.partOff + f.dataOff + int64(c-2)*int64(f.cs)
}

// bootMessageCode: código x86 mínimo para BIOS antiga (modo legado): mostra um
// aviso e para, em vez de travar numa tela preta.
func bootMessageCode(base uint16, msg string) []byte {
	si := 0x7C00 + base + 31
	code := []byte{0xFA, 0x31, 0xC0, 0x8E, 0xD8, 0x8E, 0xD0, 0xBC, 0x00, 0x7C, 0xFB,
		0xBE, byte(si), byte(si >> 8), 0xAC, 0x08, 0xC0, 0x74, 0x09, 0xB4, 0x0E, 0xBB, 0x07, 0x00,
		0xCD, 0x10, 0xEB, 0xF2, 0xF4, 0xEB, 0xFD}
	return append(code, append([]byte(msg), 0)...)
}

const legacyBootMsg = "\r\nEste pendrive inicia somente em modo UEFI.\r\nNo setup da placa-mae, ative UEFI e escolha o pendrive (UEFI).\r\n"

func (f *bigFAT) bootSector() []byte {
	le := binary.LittleEndian
	bs := make([]byte, 512)
	copy(bs[0:], []byte{0xEB, 0x58, 0x90})
	copy(bs[3:], "MSWIN4.1")
	le.PutUint16(bs[11:], 512)
	bs[13] = byte(f.cs / 512)
	le.PutUint16(bs[14:], uint16(f.reserved))
	bs[16] = 2
	bs[21] = 0xF8
	le.PutUint16(bs[24:], 63)
	le.PutUint16(bs[26:], 255)
	le.PutUint32(bs[28:], uint32(f.partOff/512))
	le.PutUint32(bs[32:], f.partSecs)
	le.PutUint32(bs[36:], f.fatSz)
	le.PutUint32(bs[44:], f.root.first)
	le.PutUint16(bs[48:], 1)
	le.PutUint16(bs[50:], 6)
	bs[64] = 0x80
	bs[66] = 0x29
	le.PutUint32(bs[67:], uint32(f.now.Unix()))
	copy(bs[71:82], padLabel(f.label))
	copy(bs[82:90], "FAT32   ")
	copy(bs[90:], bootMessageCode(90, legacyBootMsg))
	bs[510], bs[511] = 0x55, 0xAA
	return bs
}

// metaChunk gera o trecho [off, off+len(b)) da área reservada + FATs.
func (f *bigFAT) metaChunk(b []byte, off int64) {
	for i := range b {
		b[i] = 0
	}
	le := binary.LittleEndian
	resBytes := int64(f.reserved) * 512
	fatBytes := int64(f.fatSz) * 512
	// área reservada: boot (0), FSInfo (1), cópias (6, 7)
	put := func(at int64, data []byte) {
		lo, hi := at, at+int64(len(data))
		if hi <= off || lo >= off+int64(len(b)) {
			return
		}
		for i := lo; i < hi; i++ {
			if i >= off && i < off+int64(len(b)) {
				b[i-off] = data[i-lo]
			}
		}
	}
	bs := f.bootSector()
	fi := make([]byte, 512)
	le.PutUint32(fi[0:], 0x41615252)
	le.PutUint32(fi[484:], 0x61417272)
	le.PutUint32(fi[488:], f.nClusters-(f.next-2))
	le.PutUint32(fi[492:], f.next)
	le.PutUint32(fi[508:], 0xAA550000)
	put(0, bs)
	put(512, fi)
	put(6*512, bs)
	put(7*512, fi)
	// FATs (duas cópias iguais)
	for copyN := int64(0); copyN < 2; copyN++ {
		start := resBytes + copyN*fatBytes
		lo, hi := max(off, start), min(off+int64(len(b)), start+fatBytes)
		for p := lo &^ 3; p < hi; p += 4 {
			if p < lo {
				continue
			}
			c := uint32((p - start) / 4)
			var v uint32
			switch {
			case c == 0:
				v = 0x0FFFFFF8
			case c == 1:
				v = 0x0FFFFFFF
			case c < f.next:
				if f.chainEnd[c] {
					v = 0x0FFFFFFF
				} else {
					v = c + 1
				}
			}
			if p+4 <= hi {
				le.PutUint32(b[p-off:], v)
			}
		}
	}
}

func (f *bigFAT) dirData(n *bigNode, parent uint32) []byte {
	le := binary.LittleEndian
	dt, tm := dosTime(f.now)
	ent := func(short [11]byte, attr, lower byte, first uint32, size uint32) []byte {
		e := make([]byte, 32)
		copy(e[0:11], short[:])
		e[11] = attr
		e[12] = lower
		le.PutUint16(e[14:], tm)
		le.PutUint16(e[16:], dt)
		le.PutUint16(e[18:], dt)
		le.PutUint16(e[20:], uint16(first>>16))
		le.PutUint16(e[22:], tm)
		le.PutUint16(e[24:], dt)
		le.PutUint16(e[26:], uint16(first))
		le.PutUint32(e[28:], size)
		return e
	}
	var buf []byte
	if n == f.root {
		var l [11]byte
		copy(l[:], padLabel(f.label))
		buf = append(buf, ent(l, 0x08, 0, 0, 0)...)
	} else {
		var dot, dotdot [11]byte
		copy(dot[:], ".          ")
		copy(dotdot[:], "..         ")
		buf = append(buf, ent(dot, 0x10, 0, n.first, 0)...)
		buf = append(buf, ent(dotdot, 0x10, 0, parent, 0)...)
	}
	for _, c := range n.children {
		if c.lfn {
			buf = append(buf, lfnEntries(c.name, c.short)...)
		}
		if c.dir {
			buf = append(buf, ent(c.short, 0x10, c.lower, c.first, 0)...)
		} else {
			buf = append(buf, ent(c.short, 0x20, c.lower, c.first, uint32(c.file.Size))...)
		}
	}
	full := make([]byte, int64(n.clusters)*int64(f.cs))
	copy(full, buf)
	return full
}

// alignedWriter junta gravações sequenciais em blocos grandes e alinhados.
type alignedWriter struct {
	dev  io.WriterAt
	off  int64 // posição do início de buf no disco
	buf  []byte
	n    int
	p    *progress
	werr error
}

func (w *alignedWriter) flush() error {
	if w.n == 0 || w.werr != nil {
		return w.werr
	}
	n := w.n
	if r := n % alignSize; r != 0 {
		for i := n; i < n+alignSize-r; i++ {
			w.buf[i] = 0
		}
		n += alignSize - r
	}
	if _, err := w.dev.WriteAt(w.buf[:n], w.off); err != nil {
		w.werr = fmt.Errorf("falha ao gravar em %s: %w", human(w.off), err)
		return w.werr
	}
	w.off += int64(w.n)
	w.n = 0
	return nil
}

func (w *alignedWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		c := copy(w.buf[w.n:], b)
		w.n += c
		b = b[c:]
		total += c
		if w.n == len(w.buf) {
			if err := w.flush(); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// zeros escreve n bytes zero.
func (w *alignedWriter) zeros(n int64) error {
	z := make([]byte, 64<<10)
	for n > 0 {
		k := int64(len(z))
		if n < k {
			k = n
		}
		if _, err := w.Write(z[:k]); err != nil {
			return err
		}
		n -= k
	}
	return nil
}

func syncAny(dev any) error {
	if f, ok := dev.(*os.File); ok {
		return syncDevice(f)
	}
	return nil
}

// writeBigFAT grava a partição inteira e depois a tabela MBR; em seguida relê e confere.
func (f *bigFAT) write(dev rwAt, devSize int64, p *progress) error {
	f.dev = dev
	f.partOff = bigPartStart
	if f.partOff+int64(f.partSecs)*512 > devSize {
		return errors.New("partição maior que o pendrive")
	}
	buf := make([]byte, chunkSize)
	// 1. área reservada + FATs
	mh := sha256.New()
	metaLen := f.dataOff
	for off := int64(0); off < metaLen; off += int64(len(buf)) {
		n := int64(len(buf))
		if metaLen-off < n {
			n = metaLen - off
		}
		f.metaChunk(buf[:n], off)
		mh.Write(buf[:n])
		if _, err := dev.WriteAt(buf[:n], f.partOff+off); err != nil {
			return fmt.Errorf("falha ao gravar a FAT: %w", err)
		}
	}
	copy(f.metaSum[:], mh.Sum(nil))
	// 2. pastas (contíguas no começo da área de dados)
	w := &alignedWriter{dev: dev, off: f.clusterOff(2), buf: buf}
	parent := map[*bigNode]uint32{}
	var setParent func(n *bigNode)
	setParent = func(n *bigNode) {
		for _, c := range n.children {
			if c.dir {
				if n == f.root {
					parent[c] = 0
				} else {
					parent[c] = n.first
				}
				setParent(c)
			}
		}
	}
	setParent(f.root)
	for _, d := range f.dirs {
		if w.off+int64(w.n) != f.clusterOff(d.first) {
			return errors.New("erro interno: pastas fora de ordem")
		}
		data := f.dirData(d, parent[d])
		d.sum = sha256.Sum256(data)
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	// 3. arquivos
	p.set("writing", "Gravando os arquivos do Windows no pendrive…", f.fileBytes)
	for _, n := range f.order {
		if n.clusters == 0 {
			n.sum = sha256.Sum256(nil)
			continue
		}
		if w.off+int64(w.n) != f.clusterOff(n.first) {
			return errors.New("erro interno: arquivos fora de ordem")
		}
		h := sha256.New()
		r := newSegReader(n.file.Segs)
		copied, err := io.CopyBuffer(io.MultiWriter(w, h, progressWriter{p}), io.LimitReader(r, n.file.Size), make([]byte, 1<<20))
		if err != nil {
			return fmt.Errorf("falha ao copiar %s: %w", n.file.Path, err)
		}
		if copied != n.file.Size {
			return fmt.Errorf("falha ao ler %s (arquivo menor que o esperado)", n.file.Path)
		}
		copy(n.sum[:], h.Sum(nil))
		if err := w.zeros(int64(n.clusters)*int64(f.cs) - n.file.Size); err != nil {
			return err
		}
	}
	if err := w.flush(); err != nil {
		return err
	}
	// 4. fim do pendrive: apaga restos de uma GPT antiga (cópia de segurança)
	tail := devSize &^ (alignSize - 1)
	if tail-bigAlign >= f.partOff+int64(f.partSecs)*512 {
		z := make([]byte, bigAlign)
		if _, err := dev.WriteAt(z, tail-bigAlign); err != nil {
			return fmt.Errorf("falha ao gravar o fim do pendrive: %w", err)
		}
	}
	if err := syncAny(dev); err != nil {
		return fmt.Errorf("falha ao descarregar o cache: %w", err)
	}
	// 5. MBR por último: o sistema só enxerga a partição quando tudo está gravado
	head := make([]byte, bigPartStart)
	copy(head, f.mbr())
	if _, err := dev.WriteAt(head, 0); err != nil {
		return fmt.Errorf("falha ao gravar a tabela de partições: %w", err)
	}
	if err := syncAny(dev); err != nil {
		return fmt.Errorf("falha ao descarregar o cache: %w", err)
	}
	return f.verify(dev, p)
}

func (f *bigFAT) mbr() []byte {
	le := binary.LittleEndian
	m := make([]byte, 512)
	copy(m, bootMessageCode(0, legacyBootMsg))
	var sig [4]byte
	_, _ = rand.Read(sig[:])
	copy(m[440:444], sig[:])
	e := m[446:462]
	e[0] = 0x80                         // ativa
	e[1], e[2], e[3] = 0xFE, 0xFF, 0xFF // CHS "fora do alcance": usar LBA
	e[4] = 0x0C                         // FAT32 (LBA)
	e[5], e[6], e[7] = 0xFE, 0xFF, 0xFF
	le.PutUint32(e[8:], uint32(f.partOff/512))
	le.PutUint32(e[12:], f.partSecs)
	m[510], m[511] = 0x55, 0xAA
	return m
}

// verify relê do pendrive a FAT, as pastas e cada arquivo e compara o SHA256.
func (f *bigFAT) verify(dev io.ReaderAt, p *progress) error {
	p.set("verifying", "Verificando o que foi gravado…", f.fileBytes)
	buf := make([]byte, chunkSize)
	readHash := func(off, n int64, prog bool) ([32]byte, error) {
		h := sha256.New()
		for done := int64(0); done < n; {
			k := int64(len(buf))
			if n-done < k {
				k = n - done
			}
			rk := padLen(k) // leituras alinhadas
			m, err := dev.ReadAt(buf[:rk], off+done)
			if int64(m) < k {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				var z [32]byte
				return z, fmt.Errorf("falha ao reler o pendrive: %w", err)
			}
			h.Write(buf[:k])
			if prog {
				p.add(k)
			}
			done += k
		}
		var s [32]byte
		copy(s[:], h.Sum(nil))
		return s, nil
	}
	s, err := readHash(f.partOff, f.dataOff, false)
	if err != nil {
		return err
	}
	if s != f.metaSum {
		return errors.New("verificação falhou: a FAT gravada difere (pendrive com defeito?)")
	}
	for _, d := range f.dirs {
		s, err := readHash(f.clusterOff(d.first), int64(d.clusters)*int64(f.cs), false)
		if err != nil {
			return err
		}
		if s != d.sum {
			return fmt.Errorf("verificação falhou na pasta %q (pendrive com defeito?)", d.name)
		}
	}
	for _, n := range f.order {
		if n.clusters == 0 {
			continue
		}
		s, err := readHash(f.clusterOff(n.first), n.file.Size, true)
		if err != nil {
			return err
		}
		if s != n.sum {
			return fmt.Errorf("verificação falhou: %s gravado difere do original (pendrive com defeito?)", n.file.Path)
		}
	}
	mbr := make([]byte, alignSize)
	if _, err := dev.ReadAt(mbr, 0); err != nil {
		return fmt.Errorf("falha ao reler o pendrive: %w", err)
	}
	if !bytes.Equal(mbr[446:462], f.mbr()[446:462]) || mbr[510] != 0x55 || mbr[511] != 0xAA {
		return errors.New("verificação falhou: tabela de partições não confere")
	}
	return nil
}
