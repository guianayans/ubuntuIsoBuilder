package main

// Gravador mínimo de FAT32 (só criação): monta, em memória, um volume com
// pastas e arquivos com nomes longos (LFN), que Windows, macOS e Linux leem.
// Usado para a partição "INSTALAR" do pendrive. Os arquivos ficam contíguos.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	fatSector      = 512
	fatReserved    = 32
	fatClusterSecs = 1 // cluster de 512 bytes (volume pequeno)
)

type fatNode struct {
	name     string
	dir      bool
	data     []byte
	children []*fatNode
	short    [11]byte
	first    uint32 // primeiro cluster
	clusters uint32
}

// buildFAT32 devolve a imagem do volume (size bytes). files: caminho "a/b/c" -> conteúdo.
// hiddenSectors = LBA onde a partição começa (vai no BPB, como o Windows espera).
func buildFAT32(size int64, label string, files map[string][]byte, hiddenSectors uint32, now time.Time) ([]byte, error) {
	totalSecs := uint32(size / fatSector)
	// tamanho da FAT (fórmula da especificação da Microsoft)
	tmp1 := totalSecs - fatReserved
	tmp2 := uint32((256*fatClusterSecs + 2) / 2)
	fatSz := (tmp1 + tmp2 - 1) / tmp2
	dataStart := fatReserved + 2*fatSz
	nClusters := (totalSecs - dataStart) / fatClusterSecs
	if nClusters < 65525 {
		return nil, errors.New("volume pequeno demais para FAT32")
	}
	cs := uint32(fatSector * fatClusterSecs)

	// árvore
	root := &fatNode{dir: true}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.Split(strings.Trim(k, "/"), "/")
		cur := root
		for i, p := range parts {
			var next *fatNode
			for _, c := range cur.children {
				if c.name == p {
					next = c
				}
			}
			if next == nil {
				next = &fatNode{name: p, dir: i < len(parts)-1}
				cur.children = append(cur.children, next)
			}
			if i == len(parts)-1 {
				next.data = files[k]
			}
			cur = next
		}
	}

	// nomes curtos (8.3) únicos por pasta
	var assignShort func(n *fatNode)
	assignShort = func(n *fatNode) {
		used := map[string]bool{}
		for _, c := range n.children {
			c.short = shortName(c.name, used)
			if c.dir {
				assignShort(c)
			}
		}
	}
	assignShort(root)

	// alocação contígua: pasta, depois o conteúdo dela
	next := uint32(2)
	var alloc func(n *fatNode, isRoot bool)
	alloc = func(n *fatNode, isRoot bool) {
		if n.dir {
			entries := 0
			if isRoot {
				entries++ // rótulo do volume
			} else {
				entries += 2 // "." e ".."
			}
			for _, c := range n.children {
				entries += lfnCount(c.name) + 1
			}
			entries++ // marcador de fim
			n.clusters = (uint32(entries)*32 + cs - 1) / cs
		} else {
			n.clusters = (uint32(len(n.data)) + cs - 1) / cs
		}
		if n.clusters > 0 {
			n.first = next
			next += n.clusters
		}
		for _, c := range n.children {
			alloc(c, false)
		}
	}
	alloc(root, true)
	if next-2 > nClusters {
		return nil, fmt.Errorf("arquivos não cabem na partição (%d KB)", size/1024)
	}

	img := make([]byte, size)
	le := binary.LittleEndian
	// ---- setor de boot (BPB FAT32)
	bs := img[0:fatSector]
	copy(bs[0:], []byte{0xEB, 0x58, 0x90})
	copy(bs[3:], "MSWIN4.1")
	le.PutUint16(bs[11:], fatSector)
	bs[13] = fatClusterSecs
	le.PutUint16(bs[14:], fatReserved)
	bs[16] = 2
	bs[21] = 0xF8
	le.PutUint16(bs[24:], 63)
	le.PutUint16(bs[26:], 255)
	le.PutUint32(bs[28:], hiddenSectors)
	le.PutUint32(bs[32:], totalSecs)
	le.PutUint32(bs[36:], fatSz)
	le.PutUint32(bs[44:], 2) // cluster da raiz
	le.PutUint16(bs[48:], 1) // FSInfo
	le.PutUint16(bs[50:], 6) // cópia do boot
	bs[64] = 0x80
	bs[66] = 0x29
	le.PutUint32(bs[67:], uint32(now.Unix()))
	copy(bs[71:82], padLabel(label))
	copy(bs[82:90], "FAT32   ")
	bs[510], bs[511] = 0x55, 0xAA
	// ---- FSInfo
	fi := img[fatSector : 2*fatSector]
	le.PutUint32(fi[0:], 0x41615252)
	le.PutUint32(fi[484:], 0x61417272)
	le.PutUint32(fi[488:], nClusters-(next-2))
	le.PutUint32(fi[492:], next)
	le.PutUint32(fi[508:], 0xAA550000)
	copy(img[6*fatSector:7*fatSector], bs)
	copy(img[7*fatSector:8*fatSector], fi)

	// ---- FATs
	fat := make([]byte, fatSz*fatSector)
	le.PutUint32(fat[0:], 0x0FFFFFF8)
	le.PutUint32(fat[4:], 0x0FFFFFFF)
	var chain func(n *fatNode)
	chain = func(n *fatNode) {
		for i := uint32(0); i < n.clusters; i++ {
			c := n.first + i
			v := c + 1
			if i == n.clusters-1 {
				v = 0x0FFFFFFF
			}
			le.PutUint32(fat[c*4:], v)
		}
		for _, ch := range n.children {
			chain(ch)
		}
	}
	chain(root)
	copy(img[fatReserved*fatSector:], fat)
	copy(img[(fatReserved+fatSz)*fatSector:], fat)

	clusterOff := func(c uint32) int64 { return int64(dataStart+(c-2)*fatClusterSecs) * fatSector }
	dt, tm := dosTime(now)

	// ---- pastas e arquivos
	var write func(n *fatNode, parent uint32, isRoot bool)
	write = func(n *fatNode, parent uint32, isRoot bool) {
		if !n.dir {
			copy(img[clusterOff(n.first):], n.data)
			return
		}
		var buf []byte
		ent := func(short [11]byte, attr byte, first, size uint32) []byte {
			e := make([]byte, 32)
			copy(e[0:11], short[:])
			e[11] = attr
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
		if isRoot {
			var l [11]byte
			copy(l[:], padLabel(label))
			buf = append(buf, ent(l, 0x08, 0, 0)...)
		} else {
			var dot, dotdot [11]byte
			copy(dot[:], ".          ")
			copy(dotdot[:], "..         ")
			buf = append(buf, ent(dot, 0x10, n.first, 0)...)
			buf = append(buf, ent(dotdot, 0x10, parent, 0)...)
		}
		for _, c := range n.children {
			buf = append(buf, lfnEntries(c.name, c.short)...)
			attr, sz := byte(0x20), uint32(len(c.data))
			if c.dir {
				attr, sz = 0x10, 0
			}
			buf = append(buf, ent(c.short, attr, c.first, sz)...)
		}
		copy(img[clusterOff(n.first):], buf)
		self := n.first
		if isRoot {
			self = 0 // ".." de uma subpasta da raiz aponta para 0
		}
		for _, c := range n.children {
			write(c, self, false)
		}
	}
	write(root, 0, true)
	return img, nil
}

func padLabel(l string) string {
	l = strings.ToUpper(l)
	if len(l) > 11 {
		l = l[:11]
	}
	return l + strings.Repeat(" ", 11-len(l))
}

func dosTime(t time.Time) (date, tm uint16) {
	y := t.Year() - 1980
	if y < 0 {
		y = 0
	}
	date = uint16(y<<9 | int(t.Month())<<5 | t.Day())
	tm = uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)
	return
}

// shortName gera um nome 8.3 único (o nome de verdade vai nas entradas LFN).
func shortName(name string, used map[string]bool) [11]byte {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToUpper(s) {
			switch {
			case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("_-~", r):
				b.WriteRune(r)
			case r == ' ', r == '.':
			default:
				b.WriteRune('_')
			}
		}
		return b.String()
	}
	ext := strings.TrimPrefix(path.Ext(name), ".")
	base := strings.TrimSuffix(name, path.Ext(name))
	if base == "" {
		base, ext = name, ""
	}
	b, e := clean(base), clean(ext)
	if len(e) > 3 {
		e = e[:3]
	}
	if b == "" {
		b = "ARQ"
	}
	for i := 1; ; i++ {
		suf := fmt.Sprintf("~%d", i)
		bb := b
		if len(bb)+len(suf) > 8 {
			bb = bb[:8-len(suf)]
		}
		cand := bb + suf
		key := cand + "." + e
		if !used[key] {
			used[key] = true
			var out [11]byte
			copy(out[:], fmt.Sprintf("%-8s%-3s", cand, e))
			return out
		}
	}
}

func lfnCount(name string) int { return (len(utf16.Encode([]rune(name))) + 12) / 13 }

// lfnEntries: entradas de nome longo (em ordem inversa, como a especificação pede).
func lfnEntries(name string, short [11]byte) []byte {
	var sum byte
	for _, c := range short {
		sum = ((sum & 1) << 7) + (sum >> 1) + c
	}
	u := utf16.Encode([]rune(name))
	n := lfnCount(name)
	padded := make([]uint16, n*13)
	for i := range padded {
		switch {
		case i < len(u):
			padded[i] = u[i]
		case i == len(u):
			padded[i] = 0x0000
		default:
			padded[i] = 0xFFFF
		}
	}
	out := make([]byte, 0, n*32)
	for i := n; i >= 1; i-- {
		e := make([]byte, 32)
		e[0] = byte(i)
		if i == n {
			e[0] |= 0x40
		}
		e[11], e[13] = 0x0F, sum
		ch := padded[(i-1)*13 : i*13]
		pos := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
		for k, p := range pos {
			binary.LittleEndian.PutUint16(e[p:], ch[k])
		}
		out = append(out, e...)
	}
	return out
}
