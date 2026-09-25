#!/usr/bin/env python3
"""Desenha o ícone do isoBuilder (pendrive com disco) e gera icon.png (1024),
icon.icns (macOS) e icon.ico (Windows). Uso: python3 make-icon.py [pasta]"""
import io, os, struct, sys
from PIL import Image, ImageDraw, ImageFilter

S = 4096  # desenha grande e reduz (antisserrilhado)
out = sys.argv[1] if len(sys.argv) > 1 else os.path.dirname(os.path.abspath(__file__))


def draw() -> Image.Image:
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    # fundo: quadrado arredondado estilo macOS (grade de 1024: 824 de lado, raio 185)
    m, r = int(S * 100 / 1024), int(S * 185 / 1024)
    grad = Image.new("RGBA", (S, S))
    gd = ImageDraw.Draw(grad)
    top, bot = (247, 120, 66), (199, 55, 18)
    for y in range(S):
        t = y / S
        gd.line([(0, y), (S, y)], fill=tuple(int(top[i] + (bot[i] - top[i]) * t) for i in range(3)) + (255,))
    mask = Image.new("L", (S, S), 0)
    ImageDraw.Draw(mask).rounded_rectangle([m, m, S - m, S - m], r, fill=255)
    # sombra suave
    sh = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    ImageDraw.Draw(sh).rounded_rectangle([m, m + S // 80, S - m, S - m + S // 80], r, fill=(0, 0, 0, 90))
    img.alpha_composite(sh.filter(ImageFilter.GaussianBlur(S // 60)))
    img.paste(grad, (0, 0), mask)
    d = ImageDraw.Draw(img)
    u = S / 1024
    # pendrive: conector (topo) + corpo
    cx = S / 2
    cw, ch = 250 * u, 150 * u
    ctop = 215 * u
    d.rounded_rectangle([cx - cw / 2, ctop, cx + cw / 2, ctop + ch + 40 * u], 26 * u, fill=(255, 236, 226, 255))
    for dx in (-60, 60):
        d.rounded_rectangle([cx + dx * u - 28 * u, ctop + 45 * u, cx + dx * u + 28 * u, ctop + 95 * u], 8 * u,
                            fill=(214, 78, 34, 255))
    bw, btop, bbot = 400 * u, ctop + ch, 812 * u
    d.rounded_rectangle([cx - bw / 2, btop, cx + bw / 2, bbot], 70 * u, fill=(255, 255, 255, 255))
    # disco (ISO) no corpo
    dy, R = (btop + bbot) / 2 + 10 * u, 135 * u
    d.ellipse([cx - R, dy - R, cx + R, dy + R], fill=(233, 84, 32, 255))
    R2 = 95 * u
    d.ellipse([cx - R2, dy - R2, cx + R2, dy + R2], fill=(242, 118, 70, 255))
    R3 = 34 * u
    d.ellipse([cx - R3, dy - R3, cx + R3, dy + R3], fill=(255, 255, 255, 255))
    # brilho do disco
    d.arc([cx - 115 * u, dy - 115 * u, cx + 115 * u, dy + 115 * u], 200, 250, fill=(255, 255, 255, 170), width=int(14 * u))
    return img


big = draw()
png = lambda n: (lambda b: (big.resize((n, n), Image.LANCZOS).save(b, "PNG"), b.getvalue())[1])(io.BytesIO())
open(os.path.join(out, "icon.png"), "wb").write(png(1024))
# .icns: entradas PNG (macOS 10.7+)
ents = [(b"icp4", 16), (b"icp5", 32), (b"icp6", 64), (b"ic07", 128), (b"ic08", 256), (b"ic09", 512), (b"ic10", 1024)]
body = b"".join(t + struct.pack(">I", 8 + len(p)) + p for t, p in ((t, png(n)) for t, n in ents))
open(os.path.join(out, "icon.icns"), "wb").write(b"icns" + struct.pack(">I", 8 + len(body)) + body)
# .ico: PNG embutido por tamanho (Windows Vista+)
sizes = [16, 24, 32, 48, 64, 128, 256]
datas = [png(n) for n in sizes]
hdr = struct.pack("<HHH", 0, 1, len(sizes))
off = 6 + 16 * len(sizes)
dirs = b""
for n, p in zip(sizes, datas):
    dirs += struct.pack("<BBBBHHII", n % 256, n % 256, 0, 0, 1, 32, len(p), off)
    off += len(p)
open(os.path.join(out, "icon.ico"), "wb").write(hdr + dirs + b"".join(datas))
print("ícones gerados em", out)
