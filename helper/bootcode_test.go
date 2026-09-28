package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Estrutura do código de boot BIOS montado (bootcode/*.bin).
func TestBootCodeStructure(t *testing.T) {
	if len(mbrCode) != 440 {
		t.Fatalf("mbr.bin deveria ter 440 bytes (código até a assinatura do disco), tem %d", len(mbrCode))
	}
	if len(fat32Code) != 3*512 {
		t.Fatalf("fat32.bin deveria ter 3 setores, tem %d bytes", len(fat32Code))
	}
	if !bytes.Equal(fat32Code[:3], []byte{0xEB, 0x58, 0x90}) {
		t.Errorf("setor de boot deveria começar com JMP 0x5A / NOP: % x", fat32Code[:3])
	}
	if !bytes.Equal(fat32Code[bpbStart:bpbEnd], make([]byte, bpbEnd-bpbStart)) {
		t.Error("a área do BPB no fat32.bin deveria vir zerada (é do gravador)")
	}
	if fat32Code[510] != 0x55 || fat32Code[511] != 0xAA {
		t.Error("setor de boot sem 55AA")
	}
	if string(fat32Code[512:516]) != "UAB2" {
		t.Error("estágio 2 sem a marca UAB2 (o setor 0 confere essa marca)")
	}
	if !bytes.Contains(fat32Code[512:], []byte("BOOTMGR    ")) {
		t.Error("estágio 2 não procura BOOTMGR (8.3)")
	}
	if bytes.Contains(append(append([]byte{}, mbrCode...), fat32Code...), []byte("somente em modo UEFI")) {
		t.Error("mensagem antiga de só UEFI ainda presente")
	}
	for _, c := range []struct {
		lba  uint32
		want [3]byte
	}{{0, [3]byte{0, 1, 0}}, {2048, [3]byte{32, 33, 0}}, {16450559, [3]byte{254, 0xFF, 0xFF}}, {16450560, [3]byte{0xFE, 0xFF, 0xFF}}, {1 << 30, [3]byte{0xFE, 0xFF, 0xFF}}} {
		if got := chsOf(c.lba); got != c.want {
			t.Errorf("chsOf(%d) = % x, esperado % x", c.lba, got, c.want)
		}
	}
}

// fakeBootmgr: BOOTMGR falso (bootcode/testbootmgr.bin) completado com bytes aleatórios até
// size, com o tamanho e a soma dos bytes depois do código no cabeçalho (ele confere ao ser carregado).
func fakeBootmgr(t *testing.T, size int) []byte {
	code, err := os.ReadFile("bootcode/testbootmgr.bin")
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, size)
	rand.Read(b)
	copy(b, code)
	le := binary.LittleEndian
	le.PutUint32(b[4:], uint32(size))
	var sum uint32
	for _, x := range b[len(code):] {
		sum += uint32(x)
	}
	le.PutUint32(b[8:], sum)
	return b
}

// stickWithBootmgr grava num arquivo um pendrive (mesmo código do gravador) com BOOTMGR na
// raiz e muitos arquivos antes dele (a entrada de BOOTMGR cai depois do 1º cluster da raiz).
func stickWithBootmgr(t *testing.T, path string, devSize int64, bootmgr []byte) (*bigFAT, *os.File) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(devSize); err != nil {
		t.Fatal(err)
	}
	files := []fsFile{
		{Path: "bootmgr", Size: int64(len(bootmgr)), Segs: []segment{{Data: bootmgr}}},
		{Path: "bootmgr.efi", Size: 5, Segs: []segment{{Data: []byte("efi..")}}},
		{Path: "efi/boot/bootx64.efi", Size: 3, Segs: []segment{{Data: []byte("MZ.")}}},
	}
	for i := 0; i < 200; i++ { // nomes longos em ordem antes de "bootmgr"
		d := []byte(fmt.Sprintf("arquivo %d", i))
		files = append(files, fsFile{Path: fmt.Sprintf("a-arquivo-com-nome-longo-%03d.txt", i), Size: int64(len(d)), Segs: []segment{{Data: d}}})
	}
	partSize := (devSize - bigPartStart - bigAlign) &^ (bigAlign - 1)
	plan, err := planBigFAT(partSize, "WIN11", files, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.write(f, devSize, &progress{}); err != nil {
		t.Fatal(err)
	}
	return plan, f
}

func (f *bigFAT) node(name string) *bigNode {
	for _, n := range f.order {
		if n.name == name {
			return n
		}
	}
	return nil
}

// Pendrive gravado: MBR, setor de boot, estágio 2, cópia de segurança e BOOTMGR contíguo.
func TestBootCodeOnStick(t *testing.T) {
	le := binary.LittleEndian
	bm := fakeBootmgr(t, 300<<10)
	plan, f := stickWithBootmgr(t, filepath.Join(t.TempDir(), "dev.img"), 400<<20, bm)
	defer f.Close()
	sec := func(lba int64) []byte {
		b := make([]byte, 512)
		if _, err := f.ReadAt(b, lba*512); err != nil {
			t.Fatal(err)
		}
		return b
	}
	mbr := sec(0)
	if !bytes.Equal(mbr[:440], mbrCode) {
		t.Error("MBR sem o código de boot BIOS")
	}
	if le.Uint32(mbr[440:]) == 0 || le.Uint32(mbr[440:]) != plan.diskSig {
		t.Errorf("assinatura do disco inválida: %08x", le.Uint32(mbr[440:]))
	}
	e := mbr[446:462]
	if e[0] != 0x80 || e[4] != 0x0C || le.Uint32(e[8:]) != 2048 || le.Uint32(e[12:]) != plan.partSecs {
		t.Errorf("entrada da partição errada: % x", e)
	}
	if !bytes.Equal(e[1:4], []byte{32, 33, 0}) {
		t.Errorf("CHS do início errado: % x", e[1:4])
	}
	if !bytes.Equal(mbr[462:510], make([]byte, 48)) || mbr[510] != 0x55 || mbr[511] != 0xAA {
		t.Error("resto da tabela / 55AA errados")
	}
	vbr := sec(2048)
	if !bytes.Equal(vbr[:3], fat32Code[:3]) || !bytes.Equal(vbr[bpbEnd:], fat32Code[bpbEnd:512]) {
		t.Error("setor de boot sem o código BIOS")
	}
	if string(vbr[82:90]) != "FAT32   " || le.Uint16(vbr[11:]) != 512 || uint32(vbr[13])*512 != plan.cs ||
		le.Uint16(vbr[14:]) != uint16(plan.reserved) || vbr[16] != 2 || le.Uint32(vbr[28:]) != 2048 ||
		le.Uint32(vbr[32:]) != plan.partSecs || le.Uint32(vbr[36:]) != plan.fatSz || le.Uint32(vbr[44:]) != plan.root.first || vbr[64] != 0x80 {
		t.Errorf("BPB errado: % x", vbr[:90])
	}
	if !bytes.Equal(sec(2048+6), vbr) {
		t.Error("cópia do setor de boot (setor 6) difere")
	}
	fsinfo := sec(2048 + 1)
	if le.Uint32(fsinfo) != 0x41615252 || !bytes.Equal(sec(2048+7), fsinfo) {
		t.Error("FSInfo foi estragado pelo estágio 2")
	}
	st2 := append(sec(2048+2), sec(2048+3)...)
	if !bytes.Equal(st2, fat32Code[512:]) {
		t.Error("estágio 2 não está nos setores 2 e 3 da partição")
	}
	if plan.reserved < 8 {
		t.Errorf("área reservada pequena demais (%d setores)", plan.reserved)
	}
	// BOOTMGR: entrada 8.3 na raiz, cadeia contígua, conteúdo certo
	n := plan.node("bootmgr")
	if n == nil || string(n.short[:]) != "BOOTMGR    " {
		t.Fatalf("BOOTMGR sem nome 8.3 exato")
	}
	fat := make([]byte, int64(plan.fatSz)*512)
	f.ReadAt(fat, 2048*512+int64(plan.reserved)*512)
	c := n.first
	for i := uint32(0); i < n.clusters; i++ {
		next := le.Uint32(fat[(c+i)*4:]) & 0x0FFFFFFF
		if i < n.clusters-1 && next != c+i+1 || i == n.clusters-1 && next < 0x0FFFFFF8 {
			t.Fatalf("BOOTMGR não é contíguo (cluster %d -> %x)", c+i, next)
		}
	}
	got := make([]byte, len(bm))
	f.ReadAt(got, plan.clusterOff(n.first))
	if !bytes.Equal(got, bm) {
		t.Error("conteúdo do BOOTMGR difere")
	}
	// a entrada do BOOTMGR não está no 1º cluster da raiz (teste do percurso da pasta)
	root := make([]byte, int64(plan.root.clusters)*int64(plan.cs))
	f.ReadAt(root, plan.clusterOff(plan.root.first))
	at := bytes.Index(root, []byte("BOOTMGR    "))
	if at < 0 || at%32 != 0 {
		t.Fatal("entrada BOOTMGR não achada na raiz")
	}
	if at < int(plan.cs) {
		t.Logf("aviso: entrada de BOOTMGR no 1º cluster da raiz (cluster de %d bytes)", plan.cs)
	}
}

// fragment espalha o BOOTMGR pelo FIM da partição, em ordem inversa e com buracos (LBA alto,
// cadeia fragmentada), e estraga os clusters antigos: o setor de boot tem de seguir a FAT.
func fragment(t *testing.T, f *os.File, plan *bigFAT) {
	le := binary.LittleEndian
	n := plan.node("bootmgr")
	cs := int64(plan.cs)
	data := make([]byte, int64(n.clusters)*cs)
	f.ReadAt(data, plan.clusterOff(n.first))
	last := plan.nClusters + 1
	targets := make([]uint32, n.clusters)
	for i := range targets {
		targets[i] = last - uint32(i)*2
	}
	fatOff := func(copyN int64, c uint32) int64 {
		return plan.partOff + int64(plan.reserved)*512 + copyN*int64(plan.fatSz)*512 + int64(c)*4
	}
	put := func(c, v uint32) {
		b := make([]byte, 4)
		le.PutUint32(b, v)
		for k := int64(0); k < 2; k++ {
			f.WriteAt(b, fatOff(k, c))
		}
	}
	for i, tc := range targets {
		f.WriteAt(data[int64(i)*cs:int64(i+1)*cs], plan.clusterOff(tc))
		if i+1 < len(targets) {
			put(tc, targets[i+1])
		} else {
			put(tc, 0x0FFFFFFF)
		}
	}
	for i := uint32(0); i < n.clusters; i++ {
		put(n.first+i, 0)
	}
	f.WriteAt(bytes.Repeat([]byte{0xCC}, len(data)), plan.clusterOff(n.first))
	root := make([]byte, int64(plan.root.clusters)*cs)
	f.ReadAt(root, plan.clusterOff(plan.root.first))
	at := bytes.Index(root, []byte("BOOTMGR    "))
	le.PutUint16(root[at+20:], uint16(targets[0]>>16))
	le.PutUint16(root[at+26:], uint16(targets[0]))
	f.WriteAt(root, plan.clusterOff(plan.root.first))
	t.Logf("BOOTMGR fragmentado: clusters %d..%d (LBA até %d)", targets[len(targets)-1], targets[0], plan.clusterOff(targets[0])/512)
}

// Imagens para o teste de boot no QEMU/SeaBIOS (bootcode/qemu-test.sh):
// UAB_BOOTIMG=<arquivo> UAB_BOOTIMG_SIZE=<GB> [UAB_BOOTIMG_FRAG=1] go test -run TestMakeBootImage
func TestMakeBootImage(t *testing.T) {
	out := os.Getenv("UAB_BOOTIMG")
	if out == "" {
		t.Skip("UAB_BOOTIMG não definido")
	}
	gb, err := strconv.ParseFloat(os.Getenv("UAB_BOOTIMG_SIZE"), 64)
	if err != nil || gb < 1 {
		t.Fatal("UAB_BOOTIMG_SIZE inválido")
	}
	size := int64(gb*(1<<30)) &^ (1<<20 - 1)
	plan, f := stickWithBootmgr(t, out, size, fakeBootmgr(t, 460<<10))
	defer f.Close()
	if os.Getenv("UAB_BOOTIMG_FRAG") == "1" {
		fragment(t, f, plan)
	}
	t.Logf("%s: %s, cluster %d KB, raiz em %d cluster(s), BOOTMGR %d clusters", out, human(size), plan.cs>>10, plan.root.clusters, plan.node("bootmgr").clusters)
	if !strings.HasSuffix(out, ".img") {
		t.Fatal("use .img")
	}
}
