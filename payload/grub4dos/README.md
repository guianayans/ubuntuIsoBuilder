# grub4dos (grldr, grldr.mbr)

Usados para instalar **Ubuntu num PC Windows que liga em BIOS/Legacy, sem pendrive**.

O gravador do Windows, no modo `--auto-format` com um build de Ubuntu, quando o PC é BIOS
(não UEFI), baixa estes dois arquivos do painel (`/t/<token>/grldr` e `/t/<token>/grldr.mbr`),
copia o kernel/initrd do Ubuntu para `C:\uab-linux\`, escreve um `menu.lst` e cria uma entrada
BCD do tipo *bootsector* apontando para o `grldr.mbr`, disparada **uma única vez**
(`bcdedit /set {bootmgr} bootsequence`). Assim o `bootmgr` do Windows encadeia o grub4dos, que
dá boot no instalador do Ubuntu. O MBR não é tocado: se o grldr falhar, o próximo boot volta ao
Windows.

## Origem

- Projeto: grub4dos (chenall) — https://github.com/chenall/grub4dos
- Release: `grub4dos-0.4.6a-2020-08-09` (BIOS)
- Licença: GPL

SHA256:
- `grldr`     — `dece3f8d20f84ae0d0fb892b5c3a2d19e7233d0d8885b0027a6f43d77239128d`
- `grldr.mbr` — `f5c6e8e2c1eb7380285fa9cb1c9168e92d5b3b55cde052c043ba81ed17b9acef`

Para atualizar: baixe o release oficial (`.7z`), extraia e copie `grldr` e `grldr.mbr` aqui.
