package main

// Partição "INSTALAR" no espaço livre depois da ISO: leva o isoBuilder para
// Windows, macOS e Linux. Quem recebe o pendrive abre a partição e, com dois
// cliques, reinicia o computador pelo pendrive (com confirmação de formatação).
//
// Na tabela GPT da ISO (híbrida, com MBR de proteção 0xEE):
//   - as partições "dados básicos" da ISO passam a "recuperação do Windows" com o
//     atributo "sem letra de unidade": o Windows nunca monta essas partições (não
//     aparece o D: pedindo para formatar). O boot não depende do tipo;
//   - entra a partição 4 (FAT32, rótulo INSTALAR);
//   - a cópia de segurança da GPT vai para o fim do pendrive.

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	gptSector      = 512
	toolsPartBytes = 64 << 20 // 64 MiB
	toolsLabel     = "INSTALAR"
	stickMarker    = "uab-stick.json"
)

var (
	gptTypeBasicData = mustGUID("EBD0A0A2-B9E5-4433-87C0-68B6B72699C7")
	// Windows RE: o Windows nunca dá letra a esse tipo (o tipo "Linux" ele montou num teste real)
	gptTypeWinRE = mustGUID("DE94BBA4-06D1-4D40-A16A-BFD50179D6AC")
)

// atributos GPT: 0 = necessária à plataforma; 63 = sem letra de unidade (Windows)
const gptAttrHideFromWindows = uint64(1)<<63 | 1

// mustGUID converte o texto do GUID para o formato em disco (3 campos little-endian).
func mustGUID(s string) [16]byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		panic("GUID inválido: " + s)
	}
	var g [16]byte
	g[0], g[1], g[2], g[3] = b[3], b[2], b[1], b[0]
	g[4], g[5] = b[5], b[4]
	g[6], g[7] = b[7], b[6]
	copy(g[8:], b[8:])
	return g
}

type rwAt interface {
	io.ReaderAt
	io.WriterAt
}

type gptHeader struct {
	raw          []byte
	alternateLBA uint64
	firstUsable  uint64
	entriesLBA   uint64
	numEntries   uint32
	entrySize    uint32
}

func readGPT(dev io.ReaderAt) (*gptHeader, []byte, error) {
	h := make([]byte, gptSector)
	if _, err := dev.ReadAt(h, gptSector); err != nil {
		return nil, nil, err
	}
	if string(h[0:8]) != "EFI PART" {
		return nil, nil, errors.New("pendrive sem tabela GPT")
	}
	le := binary.LittleEndian
	hs := le.Uint32(h[12:])
	if hs < 92 || hs > gptSector {
		return nil, nil, errors.New("cabeçalho GPT inválido")
	}
	chk := append([]byte(nil), h[:hs]...)
	le.PutUint32(chk[16:], 0)
	if crc32.ChecksumIEEE(chk) != le.Uint32(h[16:]) {
		return nil, nil, errors.New("cabeçalho GPT corrompido (CRC)")
	}
	g := &gptHeader{raw: h, alternateLBA: le.Uint64(h[32:]), firstUsable: le.Uint64(h[40:]),
		entriesLBA: le.Uint64(h[72:]), numEntries: le.Uint32(h[80:]), entrySize: le.Uint32(h[84:])}
	if g.entrySize != 128 || g.numEntries == 0 || g.numEntries > 1024 {
		return nil, nil, errors.New("tabela GPT fora do padrão")
	}
	ents := make([]byte, int(g.numEntries)*int(g.entrySize))
	if _, err := dev.ReadAt(ents, int64(g.entriesLBA)*gptSector); err != nil {
		return nil, nil, err
	}
	if crc32.ChecksumIEEE(ents) != le.Uint32(h[88:]) {
		return nil, nil, errors.New("entradas GPT corrompidas (CRC)")
	}
	return g, ents, nil
}

// addToolsPartition cria a partição INSTALAR com os arquivos dados.
// Devolve o LBA inicial dela. devSize = tamanho do pendrive em bytes.
func addToolsPartition(dev rwAt, devSize int64, files map[string][]byte) (uint64, error) {
	le := binary.LittleEndian
	g, ents, err := readGPT(dev)
	if err != nil {
		return 0, err
	}
	devSecs := uint64(devSize / gptSector)
	entSecs := (uint64(len(ents)) + gptSector - 1) / gptSector
	newLastUsable := devSecs - 1 - entSecs - 1
	var lastEnd uint64
	free := -1
	for i := 0; i < int(g.numEntries); i++ {
		e := ents[i*128 : (i+1)*128]
		if bytes.Equal(e[0:16], make([]byte, 16)) {
			if free < 0 {
				free = i
			}
			continue
		}
		if end := le.Uint64(e[40:]); end > lastEnd {
			lastEnd = end
		}
		var t [16]byte
		copy(t[:], e[0:16])
		if t == gptTypeBasicData {
			copy(e[0:16], gptTypeWinRE[:]) // o Windows não monta nem oferece "formatar"
			le.PutUint64(e[48:], le.Uint64(e[48:])|gptAttrHideFromWindows)
		}
	}
	if free < 0 {
		return 0, errors.New("tabela GPT sem entrada livre")
	}
	start := (lastEnd + 1 + 2047) / 2048 * 2048 // alinhado a 1 MiB
	end := start + toolsPartBytes/gptSector - 1
	if end > newLastUsable {
		return 0, errors.New("pendrive sem espaço livre depois da ISO")
	}
	// nova entrada
	e := ents[free*128 : (free+1)*128]
	copy(e[0:16], gptTypeBasicData[:])
	if _, err := rand.Read(e[16:32]); err != nil {
		return 0, err
	}
	e[16+7] = e[16+7]&0x0F | 0x40 // GUID versão 4
	e[16+8] = e[16+8]&0x3F | 0x80
	le.PutUint64(e[32:], start)
	le.PutUint64(e[40:], end)
	le.PutUint64(e[48:], 0)
	for i := 56; i < 128; i++ {
		e[i] = 0
	}
	for i, u := range utf16.Encode([]rune(toolsLabel)) {
		le.PutUint16(e[56+i*2:], u)
	}

	// sistema de arquivos primeiro (se falhar, a tabela continua como estava)
	fs, err := buildFAT32(toolsPartBytes, toolsLabel, files, uint32(start), time.Now())
	if err != nil {
		return 0, err
	}
	if _, err := dev.WriteAt(fs, int64(start)*gptSector); err != nil {
		return 0, fmt.Errorf("falha ao gravar a partição INSTALAR: %w", err)
	}

	entCRC := crc32.ChecksumIEEE(ents)
	mkHeader := func(my, alt, entLBA uint64) []byte {
		h := append([]byte(nil), g.raw...)
		hs := le.Uint32(h[12:])
		le.PutUint64(h[24:], my)
		le.PutUint64(h[32:], alt)
		le.PutUint64(h[48:], newLastUsable)
		le.PutUint64(h[72:], entLBA)
		le.PutUint32(h[88:], entCRC)
		le.PutUint32(h[16:], 0)
		le.PutUint32(h[16:], crc32.ChecksumIEEE(h[:hs]))
		return h
	}
	backupEntLBA := devSecs - 1 - entSecs
	// cópia de segurança da ISO (agora no meio do pendrive): apaga o cabeçalho antigo
	if g.alternateLBA > 1 && g.alternateLBA < start {
		if _, err := dev.WriteAt(make([]byte, gptSector), int64(g.alternateLBA)*gptSector); err != nil {
			return 0, err
		}
	}
	if _, err := dev.WriteAt(ents, int64(backupEntLBA)*gptSector); err != nil {
		return 0, err
	}
	if _, err := dev.WriteAt(mkHeader(devSecs-1, 1, backupEntLBA), int64(devSecs-1)*gptSector); err != nil {
		return 0, err
	}
	if _, err := dev.WriteAt(ents, int64(g.entriesLBA)*gptSector); err != nil {
		return 0, err
	}
	if _, err := dev.WriteAt(mkHeader(1, devSecs-1, g.entriesLBA), gptSector); err != nil {
		return 0, err
	}
	// MBR de proteção cobrindo o pendrive inteiro
	mbr := make([]byte, gptSector)
	if _, err := dev.ReadAt(mbr, 0); err == nil && mbr[510] == 0x55 && mbr[511] == 0xAA {
		for i := 0; i < 4; i++ {
			p := mbr[446+i*16 : 446+(i+1)*16]
			if p[4] == 0xEE {
				n := devSecs - 1
				if n > 0xFFFFFFFF {
					n = 0xFFFFFFFF
				}
				le.PutUint32(p[12:], uint32(n))
			}
		}
		if _, err := dev.WriteAt(mbr, 0); err != nil {
			return 0, err
		}
	}
	return start, nil
}
