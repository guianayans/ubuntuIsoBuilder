#!/usr/bin/env python3
"""Gera rsrc_windows_<arch>.syso (ícone + informações de versão) para o linker do Go
embutir no .exe. Sem dependências: escreve o objeto COFF com a seção .rsrc.
Uso: python3 winres.py <icon.ico> <pasta-saida> <versao AAAA.MM.DD>"""
import struct, sys

ico_path, outdir, version = sys.argv[1], sys.argv[2], sys.argv[3]
NAME, DESC = "isoBuilder", "isoBuilder — grava a ISO no pendrive"
LANG, CP = 0x0416, 1200  # português (Brasil), Unicode

# ---- ícones do .ico
ico = open(ico_path, "rb").read()
_, _, n = struct.unpack_from("<HHH", ico, 0)
icons = []
for i in range(n):
    w, h, cc, _, planes, bpp, size, off = struct.unpack_from("<BBBBHHII", ico, 6 + 16 * i)
    icons.append((w, h, cc, planes, bpp, ico[off:off + size]))
group = struct.pack("<HHH", 0, 1, n) + b"".join(
    struct.pack("<BBBBHHIH", w, h, cc, 0, planes, bpp, len(d), i + 1) for i, (w, h, cc, planes, bpp, d) in enumerate(icons))


# ---- VS_VERSIONINFO
def pad4(b):
    return b + b"\0" * (-len(b) % 4)


def node(key, value=b"", children=b"", text=False):
    k = pad4(struct.pack("<HHH", 0, 0, 0) + (key + "\0").encode("utf-16-le"))
    vlen = len(value) // 2 if text else len(value)
    body = k + pad4(value) + children if children else k + value
    return pad4(struct.pack("<HHH", len(body), vlen, 1 if text else 0) + body[6:])


parts = [int(x) for x in (version.split(".") + ["0"] * 4)[:4] if x.isdigit()] + [0] * 4
v = parts[:4]
fixed = struct.pack("<13I", 0xFEEF04BD, 0x10000, (v[0] << 16) | v[1], (v[2] << 16) | v[3],
                    (v[0] << 16) | v[1], (v[2] << 16) | v[3], 0x3F, 0, 0x4, 0x1, 0, 0, 0)
strings = b"".join(node(k, (s + "\0").encode("utf-16-le"), text=True) for k, s in [
    ("CompanyName", NAME), ("FileDescription", DESC), ("FileVersion", version), ("InternalName", NAME),
    ("OriginalFilename", NAME + ".exe"), ("ProductName", NAME), ("ProductVersion", version)])
sfi = node("StringFileInfo", children=node(f"{LANG:04X}{CP:04X}", children=strings, text=True), text=True)
vfi = node("VarFileInfo", children=node("Translation", struct.pack("<HH", LANG, CP)), text=True)
versioninfo = node("VS_VERSION_INFO", fixed, pad4(sfi) + vfi)

# ---- árvore de recursos: tipo -> id -> idioma -> dado
res = {3: {i + 1: d[5] for i, d in enumerate(icons)}, 14: {1: group}, 16: {1: versioninfo}}


def build(machine, reltype):
    types = sorted(res)
    dirs, leaves = [], []  # calcula o layout: diretórios, entradas de dados, dados
    size_dir = lambda cnt: 16 + 8 * cnt
    off = size_dir(len(types))
    type_off, name_off = {}, {}
    for t in types:
        type_off[t] = off
        off += size_dir(len(res[t]))
    for t in types:
        for nid in sorted(res[t]):
            name_off[(t, nid)] = off
            off += size_dir(1)
    entry_off = {}
    for t in types:
        for nid in sorted(res[t]):
            entry_off[(t, nid)] = off
            off += 16
    data_off = {}
    for t in types:
        for nid in sorted(res[t]):
            off += -off % 8
            data_off[(t, nid)] = off
            off += len(res[t][nid])
    buf = bytearray(off + (-off % 8))
    hdr = lambda cnt: struct.pack("<IIHHHH", 0, 0, 0, 0, 0, cnt)
    buf[0:16] = hdr(len(types))
    for i, t in enumerate(types):
        struct.pack_into("<II", buf, 16 + 8 * i, t, 0x80000000 | type_off[t])
        o = type_off[t]
        buf[o:o + 16] = hdr(len(res[t]))
        for j, nid in enumerate(sorted(res[t])):
            struct.pack_into("<II", buf, o + 16 + 8 * j, nid, 0x80000000 | name_off[(t, nid)])
            no = name_off[(t, nid)]
            buf[no:no + 16] = hdr(1)
            struct.pack_into("<II", buf, no + 16, LANG, entry_off[(t, nid)])
            eo, do = entry_off[(t, nid)], data_off[(t, nid)]
            struct.pack_into("<IIII", buf, eo, do, len(res[t][nid]), 0, 0)  # RVA relativo à seção (+ relocação)
            buf[do:do + len(res[t][nid])] = res[t][nid]
    relocs = sorted(entry_off.values())
    raw_ptr = 20 + 40
    rel_ptr = raw_ptr + len(buf)
    sym_ptr = rel_ptr + 10 * len(relocs)
    coff = struct.pack("<HHIIIHH", machine, 1, 0, sym_ptr, 1, 0, 0)
    sec = struct.pack("<8sIIIIIIHHI", b".rsrc", 0, 0, len(buf), raw_ptr, rel_ptr, 0, len(relocs), 0, 0x40000040)
    rel = b"".join(struct.pack("<IIH", r, 0, reltype) for r in relocs)
    sym = struct.pack("<8sIhHBB", b".rsrc", 0, 1, 0, 3, 0)
    return coff + sec + bytes(buf) + rel + sym + struct.pack("<I", 4)


for arch, machine, reltype in (("amd64", 0x8664, 3), ("arm64", 0xAA64, 2)):  # ADDR32NB
    open(f"{outdir}/rsrc_windows_{arch}.syso", "wb").write(build(machine, reltype))
print("recursos do Windows gerados:", n, "ícones + versão", version)
