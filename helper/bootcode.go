package main

// Código de boot BIOS (Legacy/CSM) do pendrive do Windows, montado com o nasm a partir de
// bootcode/*.asm (bootcode/build.sh) e embutido aqui:
//   - mbrCode (440 bytes): MBR que carrega o setor de boot da partição ativa (INT 13h LBA);
//   - fat32Code (1536 bytes): setor de boot FAT32 (bytes 3..89 = BPB, preenchidos pelo
//     gravador) + estágio 2 (2 setores, nos setores 2 e 3 da partição) que acha BOOTMGR na
//     raiz seguindo a FAT e o inicia em 2000:0000 (ele lê \boot\BCD e inicia o WinPE).
// O UEFI não usa nada disso (inicia por \EFI\BOOT\BOOTX64.EFI): o mesmo pendrive inicia nos
// dois modos.

import _ "embed"

//go:embed bootcode/mbr.bin
var mbrCode []byte

//go:embed bootcode/fat32.bin
var fat32Code []byte

const (
	fat32Stage2Sector = 2 // setor (relativo à partição) onde começa o estágio 2
	bpbStart, bpbEnd  = 3, 90
)

// chsOf converte um LBA em CHS (geometria 255 cabeças x 63 setores) no formato da tabela
// de partições; além do cilindro 1023 usa o valor "fora do alcance" (FE FF FF).
func chsOf(lba uint32) [3]byte {
	const heads, secs = 255, 63
	c := lba / (heads * secs)
	if c > 1023 {
		return [3]byte{0xFE, 0xFF, 0xFF}
	}
	h := (lba / secs) % heads
	s := lba%secs + 1
	return [3]byte{byte(h), byte(s) | byte(c>>8)<<6, byte(c)}
}
