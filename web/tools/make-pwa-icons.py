#!/usr/bin/env python3
"""Gera os ícones do PWA (mesmo desenho do logo do painel: anel branco sobre
laranja) em web/static/pwa/. Uso: python3 web/tools/make-pwa-icons.py"""
import os
from PIL import Image, ImageDraw

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "static", "pwa")
ORANGE, WHITE = (233, 84, 32, 255), (255, 255, 255, 255)


def draw(size: int, maskable: bool, scale: float | None = None) -> Image.Image:
    S = size * 4  # desenha grande e reduz (bordas suaves)
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    if maskable:  # fundo inteiro; o sistema recorta no formato dele
        d.rectangle([0, 0, S, S], fill=ORANGE)
        scale = scale or 0.62  # desenho dentro da zona segura (80% central)
    else:
        d.rounded_rectangle([0, 0, S - 1, S - 1], radius=int(S * 0.22), fill=ORANGE)
        scale = 0.78
    c, r = S / 2, S * 0.30 * scale
    w = S * 0.085 * scale
    d.ellipse([c - r, c - r, c + r, c + r], outline=WHITE, width=int(w))
    r2 = S * 0.085 * scale
    d.ellipse([c - r2, c - r2, c + r2, c + r2], fill=WHITE)
    return img.resize((size, size), Image.LANCZOS)


os.makedirs(OUT, exist_ok=True)
for name, size, mask in [("icon-192.png", 192, False), ("icon-512.png", 512, False),
                         ("maskable-512.png", 512, True), ("apple-touch-icon.png", 180, True)]:
    # o iPhone só arredonda os cantos (sem recorte circular): desenho maior
    draw(size, mask, 0.78 if name.startswith("apple") else None).save(os.path.join(OUT, name), optimize=True)
print("ícones em", os.path.abspath(OUT))
