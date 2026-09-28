; fat32.asm — setor de boot (VBR) da partição FAT32 do pendrive do Windows (isoBuilder),
; BIOS antiga (Legacy/CSM). Código próprio do projeto (montar com build.sh, nasm).
;
; Saída: 1536 bytes = setor 0 da partição (512) + estágio 2 (2 setores, gravados nos
; setores 2 e 3 da partição; o setor 1 é o FSInfo e o 6 é a cópia do setor de boot).
; No setor 0 os bytes 3..89 (BPB do FAT32) são preenchidos pelo gravador (Go); aqui são zeros.
;
; O que faz:
;   1. setor 0 (carregado pela MBR em 0000:7C00, DL = disco): confere as extensões LBA da
;      INT 13h e lê o estágio 2 (setores 2..3 da partição) para 0000:7E00;
;   2. estágio 2: calcula FAT e área de dados pelo BPB, percorre a pasta raiz (seguindo a
;      cadeia de clusters na FAT) procurando "BOOTMGR" (nome 8.3), carrega o arquivo inteiro
;      seguindo a cadeia de clusters (funciona contíguo ou fragmentado, qualquer tamanho de
;      cluster de 512 B a 64 KB) em 2000:0000 e salta para 2000:0000 com DL = disco,
;      DS = ES = SS = 0, SP = 7C00 e o setor de boot (com o BPB) em 0000:7C00 — o que o
;      BOOTMGR espera; ele então lê \boot\BCD e inicia o WinPE (\sources\boot.wim).
; Limites: setores de 512 bytes; LBA de 32 bits (partição nos primeiros 2 TiB, como em MBR).

bits 16
org 0x7C00

; campos do BPB (preenchidos pelo gravador)
BPB_BPS     equ 0x7C00 + 11     ; word: bytes por setor
BPB_SPC     equ 0x7C00 + 13     ; byte: setores por cluster
BPB_RSVD    equ 0x7C00 + 14     ; word: setores reservados
BPB_NFATS   equ 0x7C00 + 16     ; byte: número de FATs
BPB_HIDDEN  equ 0x7C00 + 28     ; dword: LBA do início da partição
BPB_FATSZ   equ 0x7C00 + 36     ; dword: setores por FAT
BPB_ROOT    equ 0x7C00 + 44     ; dword: cluster da pasta raiz
BPB_DRIVE   equ 0x7C00 + 64     ; byte: número do disco (atualizado com o DL recebido)

STAGE2_LBA  equ 2               ; setor (relativo à partição) do estágio 2
STAGE2_SECS equ 2
DIR_SEG     equ 0x1000          ; buffer de um cluster da pasta raiz (até 64 KB)
FAT_BUF     equ 0x8200          ; um setor da FAT (logo depois do estágio 2)
LOAD_SEG    equ 0x2000          ; BOOTMGR em 2000:0000
MAX_CHUNK   equ 64              ; setores por leitura (32 KB; algumas BIOS limitam a 127)

        jmp     short start
        nop
        times 90 - ($ - $$) db 0        ; OEM + BPB (gravador)

start:
        jmp     0x0000:.cs0             ; CS = 0 (algumas BIOS usam 07C0:0000)
.cs0:
        cli
        xor     ax, ax
        mov     ss, ax
        mov     sp, 0x7C00
        mov     ds, ax
        mov     es, ax
        sti
        cld
        mov     [BPB_DRIVE], dl

        mov     ah, 0x41
        mov     bx, 0x55AA
        int     0x13
        jc      no_lba
        cmp     bx, 0xAA55
        jne     no_lba
        test    cl, 1
        jz      no_lba

        mov     eax, [BPB_HIDDEN]
        add     eax, STAGE2_LBA
        mov     cx, STAGE2_SECS
        mov     bx, stage2_magic         ; 0000:7E00
        call    read_sectors
        cmp     dword [stage2_magic], 'UAB2'
        jne     bad_stage2
        jmp     stage2

no_lba:
        mov     si, msg_nolba
        jmp     fatal
bad_stage2:
        mov     si, msg_stage2
        jmp     fatal
read_error:
        mov     si, msg_read
        ; continua em fatal

; SI = mensagem; espera uma tecla e devolve o controle à BIOS (próximo dispositivo)
fatal:
        xor     ax, ax
        mov     ds, ax
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

; Lê CX setores (CX <= 127) a partir do LBA EAX (absoluto no disco) para ES:BX.
; Preserva todos os registradores.
read_sectors:
        pushad
        mov     [dap_want], cx
        mov     [dap_off], bx
        mov     [dap_seg], es
        mov     [dap_lba], eax
        mov     di, 5
.try:
        mov     ax, [dap_want]          ; a BIOS pode alterar o contador em erro
        mov     [dap_count], ax
        mov     si, dap
        mov     ah, 0x42
        mov     dl, [BPB_DRIVE]
        int     0x13
        jnc     .ok
        xor     ax, ax
        mov     dl, [BPB_DRIVE]
        int     0x13
        dec     di
        jnz     .try
        jmp     read_error
.ok:
        popad
        ret

dap:            db 0x10, 0
dap_count:      dw 0
dap_off:        dw 0
dap_seg:        dw 0
dap_lba:        dd 0, 0
dap_want:       dw 0

msg_nolba:      db 13, 10, "BIOS sem acesso LBA (INT 13h).", 0
msg_stage2:     db 13, 10, "Setor de boot incompleto: grave o pendrive de novo.", 0
msg_read:       db 13, 10, "Erro ao ler o pendrive.", 0
msg_key:        db 13, 10, "Pressione uma tecla.", 13, 10, 0

%if ($ - $$) > 510
%error "setor 0 passou de 510 bytes"
%endif
        times 510 - ($ - $$) db 0
        dw      0xAA55

; ---------------------------------------------------------------- estágio 2 (0000:7E00)
stage2_magic:   db 'UAB2'
stage2:
        ; somente setores de 512 bytes
        cmp     word [BPB_BPS], 512
        jne     bad_fs
        movzx   ax, byte [BPB_SPC]
        test    ax, ax
        jz      bad_fs
        cmp     ax, 128
        ja      bad_fs
        mov     [spc], ax
        shl     ax, 5                   ; parágrafos por cluster (spc * 512 / 16)
        mov     [clus_paras], ax
        ; FAT = início + reservados; dados = FAT + nFATs * tamanho da FAT
        movzx   eax, word [BPB_RSVD]
        add     eax, [BPB_HIDDEN]
        mov     [fat_lba], eax
        movzx   ecx, byte [BPB_NFATS]
        mov     eax, [BPB_FATSZ]
        mul     ecx
        add     eax, [fat_lba]
        mov     [data_lba], eax
        ; memória convencional disponível (KB -> segmento do topo)
        int     0x12
        shl     ax, 6
        mov     [top_seg], ax

        ; ------------------------------------------------ procura BOOTMGR na raiz
        mov     eax, [BPB_ROOT]
        mov     word [guard], 0
.dir_cluster:
        call    check_cluster
        mov     [cur], eax
        mov     bx, DIR_SEG
        call    read_cluster
        mov     cx, [spc]
        shl     cx, 4                   ; entradas de 32 bytes por cluster
        push    DIR_SEG
        pop     es
        xor     di, di
.entry:
        mov     al, [es:di]
        test    al, al
        jz      not_found               ; fim da pasta
        cmp     al, 0xE5
        je      .next                   ; apagada
        test    byte [es:di + 11], 0x18 ; pasta, rótulo ou LFN (0x0F)
        jnz     .next
        push    cx
        push    di
        mov     si, bootmgr_name
        mov     cx, 11
        repe    cmpsb
        pop     di
        pop     cx
        je      found
.next:
        add     di, 32
        loop    .entry
        xor     ax, ax
        mov     es, ax
        inc     word [guard]
        jz      bad_fs
        mov     eax, [cur]
        call    next_cluster
        cmp     eax, 0x0FFFFFF8
        jb      .dir_cluster
        jmp     not_found

found:
        mov     ax, [es:di + 20]        ; cluster (parte alta)
        shl     eax, 16
        mov     ax, [es:di + 26]        ; cluster (parte baixa)
        mov     ecx, [es:di + 28]       ; tamanho
        xor     bx, bx
        mov     es, bx
        jecxz   not_found

        ; ------------------------------------------------ carrega BOOTMGR em 2000:0000
        mov     word [load_seg], LOAD_SEG
.load:
        call    check_cluster
        mov     [cur], eax
        mov     bx, [load_seg]
        mov     dx, bx
        add     dx, [clus_paras]
        jc      too_big
        cmp     dx, [top_seg]
        ja      too_big
        call    read_cluster
        mov     [load_seg], dx
        mov     eax, [cur]
        call    next_cluster
        cmp     eax, 0x0FFFFFF8
        jb      .load

        ; ------------------------------------------------ salta para o BOOTMGR
        mov     dl, [BPB_DRIVE]
        xor     ax, ax
        mov     ds, ax
        mov     es, ax
        cli
        mov     ss, ax
        mov     sp, 0x7C00
        sti
        jmp     LOAD_SEG:0x0000

not_found:
        mov     si, msg_notfound
        jmp     fatal
too_big:
        mov     si, msg_toobig
        jmp     fatal
bad_fs:
        mov     si, msg_badfs
        jmp     fatal

; EAX = cluster; erro se estiver fora de 2..0x0FFFFFF6
check_cluster:
        cmp     eax, 2
        jb      bad_fs
        cmp     eax, 0x0FFFFFF7
        jae     bad_fs
        ret

; Lê o cluster EAX para BX:0000 (em pedaços de até MAX_CHUNK setores).
; Preserva todos os registradores.
read_cluster:
        pushad
        push    es
        sub     eax, 2
        movzx   ecx, word [spc]
        mul     ecx
        add     eax, [data_lba]
        mov     dx, [spc]               ; setores que faltam
.chunk:
        mov     cx, dx
        cmp     cx, MAX_CHUNK
        jbe     .n
        mov     cx, MAX_CHUNK
.n:
        mov     es, bx
        push    bx
        xor     bx, bx
        call    read_sectors
        pop     bx
        movzx   esi, cx
        add     eax, esi
        shl     cx, 5
        add     bx, cx                  ; próximo segmento
        shr     cx, 5
        sub     dx, cx
        jnz     .chunk
        pop     es
        popad
        ret

; EAX = cluster -> EAX = próximo cluster (entrada da FAT, 28 bits). Guarda o último setor lido.
next_cluster:
        push    es
        push    ebx
        push    ecx
        push    si
        xor     bx, bx
        mov     es, bx
        mov     ebx, eax
        shr     eax, 7                  ; 128 entradas por setor
        add     eax, [fat_lba]
        cmp     eax, [fat_cached]
        je      .have
        mov     [fat_cached], eax
        push    bx
        mov     bx, FAT_BUF
        mov     cx, 1
        call    read_sectors
        pop     bx
.have:
        and     bx, 127
        shl     bx, 2
        mov     eax, [FAT_BUF + bx]
        and     eax, 0x0FFFFFFF
        pop     si
        pop     ecx
        pop     ebx
        pop     es
        ret

bootmgr_name:   db "BOOTMGR    "
msg_notfound:   db 13, 10, "BOOTMGR nao encontrado no pendrive.", 0
msg_toobig:     db 13, 10, "BOOTMGR grande demais para a memoria.", 0
msg_badfs:      db 13, 10, "Sistema de arquivos FAT32 invalido.", 0

spc:            dw 0
clus_paras:     dw 0
top_seg:        dw 0
load_seg:       dw 0
guard:          dw 0
fat_lba:        dd 0
data_lba:       dd 0
cur:            dd 0
fat_cached:     dd 0xFFFFFFFF

%if ($ - $$) > 512 * (1 + STAGE2_SECS)
%error "estagio 2 passou de 2 setores"
%endif
        times 512 * (1 + STAGE2_SECS) - ($ - $$) db 0
