; testbootmgr.asm — BOOTMGR FALSO, só para testes do código de boot (não vai para o pendrive).
; O teste (bootcode_test.go / bootcode/qemu-test.sh) completa o arquivo até centenas de KB
; com bytes aleatórios e grava no cabeçalho o tamanho e a soma dos bytes 16..fim. Quando o
; setor de boot o carrega em 2000:0000, ele confere:
;   - CS = 2000, SS = 0, SP = 7C00 e DL = disco (>= 0x80);
;   - o setor de boot com o BPB em 0000:7C00 ("FAT32   " em 0x52, 55AA);
;   - a soma dos bytes carregados depois deste código (o arquivo inteiro chegou na memória);
; e escreve o resultado na serial (COM1) e na tela, depois encerra o QEMU pela porta 0xF4
; (dispositivo isa-debug-exit): código de saída 1 = OK, 3 = falhou.

bits 16
org 0

        jmp     near main
        db      0
file_len:  dd 0                 ; preenchido pelo teste
file_sum:  dd 0                 ; soma dos bytes code_end..file_len-1 (preenchida pelo teste)
        dd      0

main:
        mov     [cs:ent_dl], dl
        mov     [cs:ent_sp], sp
        mov     [cs:ent_ss], ss
        mov     ax, cs
        mov     [cs:ent_cs], ax
        mov     ds, ax              ; pilha continua em 0000:7Cxx (fora do arquivo)
        cld

        mov     byte [ok], 1
        cmp     word [ent_cs], 0x2000
        jne     .bad_regs
        cmp     word [ent_ss], 0
        jne     .bad_regs
        cmp     word [ent_sp], 0x7C00
        jne     .bad_regs
        cmp     byte [ent_dl], 0x80
        jb      .bad_regs
        jmp     .bpb
.bad_regs:
        mov     byte [ok], 0
        mov     si, m_regs
        call    puts

.bpb:
        xor     ax, ax
        mov     es, ax
        cmp     dword [es:0x7C52], 'FAT3'
        jne     .bad_bpb
        cmp     word [es:0x7DFE], 0xAA55
        je      .sum
.bad_bpb:
        mov     byte [ok], 0
        mov     si, m_bpb
        call    puts

.sum:
        mov     ecx, [file_len]
        sub     ecx, code_end           ; as variáveis acima mudam: fora da soma
        xor     ebx, ebx
        mov     ax, 0x2000
        mov     es, ax
        mov     di, code_end
.loop:
        movzx   eax, byte [es:di]
        add     ebx, eax
        inc     di
        jnz     .same
        mov     ax, es
        add     ax, 0x1000
        mov     es, ax
.same:
        dec     ecx
        jnz     .loop
        cmp     ebx, [file_sum]
        je      .report
        mov     byte [ok], 0
        mov     si, m_sum
        call    puts

.report:
        mov     si, m_head
        call    puts
        mov     al, [ent_dl]
        call    puthex
        mov     si, m_len
        call    puts
        mov     eax, [file_len]
        shr     eax, 16
        call    puthex
        mov     eax, [file_len]
        shr     eax, 8
        call    puthex
        mov     al, byte [file_len]
        call    puthex
        cmp     byte [ok], 1
        jne     .fail
        mov     si, m_ok
        call    puts
        xor     al, al
        out     0xF4, al
        jmp     .halt
.fail:
        mov     si, m_fail
        call    puts
        mov     al, 1
        out     0xF4, al
.halt:
        hlt
        jmp     .halt

; caractere AL na serial e na tela
putc:
        push    ax
        push    bx
        push    dx
        mov     ah, al
        mov     dx, 0x3FD
.wait:
        in      al, dx
        test    al, 0x20
        jz      .wait
        mov     dx, 0x3F8
        mov     al, ah
        out     dx, al
        mov     ah, 0x0E
        mov     bx, 0x0007
        int     0x10
        pop     dx
        pop     bx
        pop     ax
        ret

puts:
        lodsb
        test    al, al
        jz      .d
        call    putc
        jmp     puts
.d:     ret

puthex:
        push    ax
        shr     al, 4
        call    .nib
        pop     ax
        and     al, 0x0F
.nib:
        and     al, 0x0F
        add     al, '0'
        cmp     al, '9'
        jbe     .p
        add     al, 7
.p:     jmp     putc

ok:      db 0
ent_dl:  db 0
ent_sp:  dw 0
ent_ss:  dw 0
ent_cs:  dw 0
m_regs:  db "UABTEST registradores errados na entrada", 13, 10, 0
m_bpb:   db "UABTEST BPB ausente em 0000:7C00", 13, 10, 0
m_sum:   db "UABTEST soma do arquivo NAO confere", 13, 10, 0
m_head:  db "UABTEST BOOTMGR falso carregado, DL=", 0
m_len:   db " tamanho=", 0
m_ok:    db " RESULTADO=OK", 13, 10, 0
m_fail:  db " RESULTADO=FALHOU", 13, 10, 0
code_end:
