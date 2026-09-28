; mbr.asm — código de boot da MBR do pendrive do Windows (isoBuilder), BIOS antiga (Legacy/CSM).
; Código próprio do projeto (montar com: nasm -f bin -o mbr.bin mbr.asm; ver build.sh).
;
; O que faz:
;   1. a BIOS carrega este setor em 0000:7C00 e passa o número do disco em DL;
;   2. copia a si mesmo para 0000:0600 (libera 7C00 para o setor de boot da partição);
;   3. acha a partição ativa (0x80) na tabela (446..509);
;   4. lê o 1º setor dela com INT 13h AH=42h (LBA, extensões EDD) para 0000:7C00;
;   5. confere a assinatura 55AA e salta para 0000:7C00 com DL = disco e DS:SI = entrada
;      da partição (convenção das MBRs), SS:SP = 0000:7C00.
; Em erro: mostra o motivo, espera uma tecla e devolve o controle à BIOS (INT 18h: próximo
; dispositivo de boot).
;
; Só os bytes 0..439 são código; 440..443 (assinatura do disco), 444..445, a tabela de
; partições (446..509) e 55AA são do gravador: o Go copia apenas o código.

bits 16
org 0x0600

CODE_MAX    equ 440
PTABLE      equ 0x0600 + 446
VBR         equ 0x7C00

start:
        cli
        xor     ax, ax
        mov     ss, ax
        mov     sp, VBR
        mov     ds, ax
        mov     es, ax
        sti
        cld
        mov     si, VBR
        mov     di, 0x0600
        mov     cx, 256
        rep     movsw
        jmp     0x0000:relocated

relocated:
        mov     [drive], dl

        ; partição ativa
        mov     si, PTABLE
        mov     cx, 4
.find:
        cmp     byte [si], 0x80
        je      .found
        add     si, 16
        loop    .find
        mov     si, msg_noactive
        jmp     fatal

.found:
        mov     [part], si

        ; extensões LBA da INT 13h
        mov     ah, 0x41
        mov     bx, 0x55AA
        mov     dl, [drive]
        int     0x13
        jc      .nolba
        cmp     bx, 0xAA55
        jne     .nolba
        test    cl, 1
        jnz     .lba
.nolba:
        mov     si, msg_nolba
        jmp     fatal

.lba:
        mov     si, [part]
        mov     eax, [si + 8]
        mov     [dap_lba], eax
        mov     di, 5                   ; tentativas
.read:
        mov     word [dap_count], 1
        mov     si, dap
        mov     ah, 0x42
        mov     dl, [drive]
        int     0x13
        jnc     .check
        xor     ax, ax                  ; reinicia o controlador e tenta de novo
        mov     dl, [drive]
        int     0x13
        dec     di
        jnz     .read
        mov     si, msg_read
        jmp     fatal

.check:
        cmp     word [VBR + 510], 0xAA55
        je      .go
        mov     si, msg_novbr
        jmp     fatal

.go:
        mov     si, [part]
        mov     dl, [drive]
        jmp     0x0000:VBR

; SI = mensagem (termina em 0)
fatal:
        call    print
        mov     si, msg_key
        call    print
        xor     ax, ax
        int     0x16
        int     0x18
.halt:
        hlt
        jmp     .halt

print:
        lodsb
        test    al, al
        jz      .done
        mov     ah, 0x0E
        mov     bx, 0x0007
        int     0x10
        jmp     print
.done:
        ret

drive:  db 0x80
part:   dw 0

dap:            db 0x10, 0
dap_count:      dw 1
dap_buf:        dw VBR, 0x0000
dap_lba:        dd 0, 0

msg_noactive:   db 13, 10, "Pendrive do Windows: nenhuma particao ativa.", 0
msg_nolba:      db 13, 10, "Esta BIOS nao tem acesso LBA ao disco (INT 13h).", 0
msg_read:       db 13, 10, "Erro ao ler o pendrive.", 0
msg_novbr:      db 13, 10, "Setor de boot da particao invalido.", 0
msg_key:        db 13, 10, "Pressione uma tecla para continuar.", 13, 10, 0

%if ($ - $$) > CODE_MAX
%error "codigo da MBR passou de 440 bytes"
%endif
        times CODE_MAX - ($ - $$) db 0
