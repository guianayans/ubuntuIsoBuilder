#!/usr/bin/env python3
"""Gera web/static/pkgicons.js: os ícones do catálogo de pacotes extras (estilo Ninite),
os dos apps do Windows (windows/apps.json, campo "icon") e os logos dos sistemas.

Os ícones ficam EMBUTIDOS no painel (a CSP só aceita imagens do próprio site e nada é
carregado de terceiros ao abrir a página). Fontes, baixadas só quando este script roda:
  - marcas: Simple Icons (CC0 1.0) — https://simpleicons.org
  - demais: Lucide (ISC) — https://lucide.dev
Uso: python3 web/tools/make-pkg-icons.py
"""
from __future__ import annotations

import json
import os
import re
import urllib.request

SI = "https://cdn.jsdelivr.net/npm/simple-icons@16/"
LU = "https://cdn.jsdelivr.net/npm/lucide-static@latest/icons/{}.svg"
OUT = os.path.join(os.path.dirname(__file__), "..", "static", "pkgicons.js")
WIN_APPS = os.path.join(os.path.dirname(__file__), "..", "..", "windows", "apps.json")
# cor do bloco dos ícones genéricos (lucide:<nome>) dos apps do Windows, pela categoria
WIN_CAT = {"navegadores": "#2563eb", "compactadores": "#d97706", "arquivos": "#d97706", "utilitários": "#475569",
           "utilitarios": "#475569", "acesso remoto": "#7c3aed", "remoto": "#7c3aed", "escritório": "#0d9488",
           "escritorio": "#0d9488", "documentos": "#0d9488", "mídia": "#db2777", "midia": "#db2777",
           "comunicação": "#16a34a", "comunicacao": "#16a34a", "segurança": "#dc2626", "seguranca": "#dc2626",
           "desenvolvimento": "#4f46e5", "sistema": "#0284c7", "rede": "#7c3aed", "runtimes": "#9333ea"}
WIN_PALETTE = ["#0284c7", "#7c3aed", "#475569", "#d97706", "#0d9488", "#dc2626", "#4f46e5", "#16a34a", "#9333ea"]

# cor do bloco por categoria (ícones genéricos)
CAT = {"mon": "#0284c7", "net": "#7c3aed", "term": "#475569", "file": "#d97706",
       "disk": "#0d9488", "sec": "#dc2626", "srv": "#4f46e5", "bak": "#16a34a", "hw": "#9333ea"}

# pacote -> ("b", slug do Simple Icons) | ("l", nome no Lucide, categoria)
ICONS = {
    "btop": ("l", "activity", "mon"), "glances": ("l", "gauge", "mon"), "iotop": ("l", "hard-drive", "mon"),
    "iftop": ("l", "arrow-down-up", "mon"), "nload": ("l", "chart-line", "mon"), "ncdu": ("l", "chart-pie", "mon"),
    "sysstat": ("l", "chart-column", "mon"), "lm-sensors": ("l", "thermometer", "mon"),
    "smartmontools": ("l", "heart-pulse", "mon"), "nvtop": ("b", "nvidia"),
    "net-tools": ("l", "network", "net"), "nmap": ("l", "radar", "net"), "tcpdump": ("l", "scan-search", "net"),
    "mtr-tiny": ("l", "route", "net"), "iperf3": ("l", "arrow-right-left", "net"), "traceroute": ("l", "waypoints", "net"),
    "dnsutils": ("l", "globe", "net"), "whois": ("l", "search", "net"), "ethtool": ("l", "ethernet-port", "net"),
    "speedtest-cli": ("b", "speedtest"),
    "vim": ("b", "vim"), "micro": ("l", "square-pen", "term"), "tmux": ("b", "tmux"), "screen": ("l", "monitor", "term"),
    "bash-completion": ("b", "gnubash"), "tree": ("l", "folder-tree", "term"), "jq": ("b", "json"),
    "zip": ("l", "file-archive", "file"), "p7zip-full": ("b", "7zip"), "rclone": ("b", "rclone"), "pv": ("l", "fast-forward", "file"),
    "nfs-common": ("l", "folder-sync", "disk"), "cifs-utils": ("l", "folder-input", "disk"), "samba": ("l", "share-2", "disk"),
    "ntfs-3g": ("l", "hard-drive", "disk"), "exfatprogs": ("l", "usb", "disk"), "nvme-cli": ("l", "memory-stick", "disk"),
    "zfsutils-linux": ("b", "openzfs"), "mdadm": ("l", "layers", "disk"), "hdparm": ("l", "disc-3", "disk"),
    "ufw": ("l", "brick-wall", "sec"), "clamav": ("l", "shield-check", "sec"), "lynis": ("l", "clipboard-check", "sec"),
    "auditd": ("l", "scroll-text", "sec"), "unattended-upgrades": ("l", "refresh-cw", "sec"),
    "nginx": ("b", "nginx"), "apache2": ("b", "apache"), "mariadb-server": ("b", "mariadb"), "postgresql": ("b", "postgresql"),
    "redis-server": ("b", "redis"), "python3-pip": ("b", "python"), "nodejs": ("b", "nodedotjs"), "openjdk-21-jre-headless": ("b", "openjdk"),
    "restic": ("l", "archive-restore", "bak"), "borgbackup": ("l", "database-backup", "bak"),
    "pciutils": ("l", "cpu", "hw"), "usbutils": ("l", "usb", "hw"), "dmidecode": ("l", "circuit-board", "hw"), "lshw": ("l", "list-tree", "hw"),
}


# sistemas operacionais (ícone em Máquinas e no Remoto): slug do Simple Icons
OS = {"ubuntu": "ubuntu", "debian": "debian", "fedora": "fedora", "centos": "centos", "rocky": "rockylinux",
      "alma": "almalinux", "redhat": "redhat", "suse": "opensuse", "arch": "archlinux", "mint": "linuxmint",
      "proxmox": "proxmox", "macos": "apple", "freebsd": "freebsd", "raspberry": "raspberrypi", "kali": "kalilinux",
      "truenas": "truenas", "linux": "linux"}
# O Simple Icons não distribui logos da Microsoft (a pedido dela): ícone próprio e neutro.
WINDOWS = {"t": "b", "c": "#0078D4",
           "d": '<path d="M3 3h8.5v8.5H3zM12.5 3H21v8.5h-8.5zM3 12.5h8.5V21H3zM12.5 12.5H21V21h-8.5z"/>'}


def get(url: str) -> str:
    with urllib.request.urlopen(url, timeout=30) as r:
        return r.read().decode()


def inner(svg: str) -> str:
    body = re.sub(r"^.*?<svg[^>]*>", "", svg, flags=re.S)
    body = re.sub(r"</svg>\s*$", "", body.strip())
    body = re.sub(r"<title>.*?</title>", "", body, flags=re.S)
    return re.sub(r"\s+", " ", body).strip()


def win_icons(hexes: dict) -> dict:
    """Ícones dos apps do Windows: "icon" = slug do Simple Icons ou "lucide:<nome>".
    Slug que não existe (o Simple Icons não tem várias marcas, ex. Microsoft) vira ícone genérico."""
    if not os.path.exists(WIN_APPS):
        print("aviso: windows/apps.json não existe; WIN_ICONS fica vazio")
        return {}
    with open(WIN_APPS, encoding="utf-8") as fh:
        apps = json.load(fh)
    out = {}
    for a in apps:
        aid, icon, cat = a["id"], str(a.get("icon") or ""), str(a.get("cat") or "").strip().lower()
        color = WIN_CAT.get(cat) or WIN_PALETTE[sum(map(ord, cat)) % len(WIN_PALETTE)]
        if icon and not icon.startswith("lucide:") and icon in hexes:
            out[aid] = {"t": "b", "c": hexes[icon], "d": inner(get(SI + f"icons/{icon}.svg"))}
            print(f"windows {aid:32} b {icon}")
            continue
        name = icon[7:] if icon.startswith("lucide:") else "package"
        if icon and not icon.startswith("lucide:"):
            print(f"aviso: {aid}: '{icon}' não está no Simple Icons; usando ícone genérico")
        try:
            svg = get(LU.format(name))
        except Exception:  # noqa: BLE001
            print(f"aviso: {aid}: lucide '{name}' não existe; usando 'package'")
            svg = get(LU.format("package"))
        out[aid] = {"t": "l", "c": color, "d": inner(svg)}
        print(f"windows {aid:32} l {name}")
    return out


def main() -> None:
    data = json.loads(get(SI + "data/simple-icons.json"))
    data = data["icons"] if isinstance(data, dict) else data
    hexes = {}
    for x in data:
        s = x.get("slug") or re.sub(r"[^a-z0-9]", "", x["title"].lower().replace("+", "plus").replace(".", "dot"))
        hexes[s] = "#" + x["hex"]
    out = {}
    for pkg, spec in ICONS.items():
        if spec[0] == "b":
            svg = get(SI + f"icons/{spec[1]}.svg")
            out[pkg] = {"t": "b", "c": hexes[spec[1]], "d": inner(svg)}
        else:
            svg = get(LU.format(spec[1]))
            out[pkg] = {"t": "l", "c": CAT[spec[2]], "d": inner(svg)}
        print(f"{pkg:26} {spec[0]} {spec[1]}")
    win = win_icons(hexes)
    osic = {"windows": WINDOWS}
    for key, slug in OS.items():
        osic[key] = {"t": "b", "c": hexes[slug], "d": inner(get(SI + f"icons/{slug}.svg"))}
        print(f"sistema {key:18} {slug}")
    head = ("// Gerado por web/tools/make-pkg-icons.py — não edite à mão.\n"
            "// Ícones do catálogo de pacotes extras.\n"
            "// Marcas: Simple Icons (CC0 1.0, https://simpleicons.org); as marcas pertencem aos donos.\n"
            "// Demais: Lucide (ISC License, https://lucide.dev). Copyright (c) for portions of Lucide are held by\n"
            "// Cole Bemis 2013-2022 as part of Feather (MIT). All other copyright (c) for Lucide are held by\n"
            "// Lucide Contributors 2022. Permission to use, copy, modify, and/or distribute this software for any\n"
            "// purpose with or without fee is hereby granted, provided that the above copyright notice and this\n"
            "// permission notice appear in all copies. THE SOFTWARE IS PROVIDED \"AS IS\" AND THE AUTHOR DISCLAIMS\n"
            "// ALL WARRANTIES WITH REGARD TO THIS SOFTWARE.\n")
    with open(OUT, "w", encoding="utf-8") as fh:
        fh.write(head + "window.PKG_ICONS = " + json.dumps(out, ensure_ascii=False, separators=(",", ":")) + ";\n"
                 + "// Windows: ícone próprio (quatro quadrados), não o logo oficial da Microsoft.\n"
                 + "window.OS_ICONS = " + json.dumps(osic, ensure_ascii=False, separators=(",", ":")) + ";\n"
                 + "// Apps do Windows (windows/apps.json): id do winget -> ícone.\n"
                 + "window.WIN_ICONS = " + json.dumps(win, ensure_ascii=False, separators=(",", ":")) + ";\n")
    print("gravado:", os.path.normpath(OUT), os.path.getsize(OUT), "bytes")


if __name__ == "__main__":
    main()
