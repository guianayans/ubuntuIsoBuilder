#!/usr/bin/env python3
"""Gera um autounattend.xml de TESTE (VM) com o gerador real (web/app/winconfig.py).

Uso: make_test_xml.py <saida.xml> --panel-url URL --token-file F --pubkey-file F
                      [--remote-url URL --meshid-file F] [--password-file F]
                      [--image "Windows 11 Enterprise Evaluation"] [--no-key]
                      [--apps "7zip.7zip Notepad++.Notepad++"] [--build wintest01]

--image/--no-key existem só para testar com a ISO de avaliação (Enterprise), que não
aceita a chave genérica do Pro: trocam o nome da edição aplicada pelo dism e tiram a chave.
"""
import argparse
import os
import re
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "web"))
from app import winconfig  # noqa: E402


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("--panel-url", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--pubkey-file", default="")
    ap.add_argument("--remote-url", default="")
    ap.add_argument("--meshid-file", default="")
    ap.add_argument("--password-file", required=True)
    ap.add_argument("--image", default="")
    ap.add_argument("--no-key", action="store_true")
    ap.add_argument("--apps", default="7zip.7zip Notepad++.Notepad++")
    ap.add_argument("--build", default="wintest01")
    ap.add_argument("--hostname", default="pc-teste-01")
    ap.add_argument("--user", default="suporte")
    a = ap.parse_args()
    rd = lambda p: open(p).read().strip() if p else ""  # noqa: E731
    cfg = {"OS": "windows", "WIN_VERSION": "11", "WIN_EDITION": "Pro", "WIN_LANG": "pt-BR", "UI_LANG": "pt-BR",
           "HOSTNAME_": a.hostname, "ADMIN_USER": a.user, "REALNAME": "Suporte Técnico",
           "TIMEZONE_WIN": "E. South America Standard Time", "INPUT_LOCALE": "0416:00010416",
           "PANEL_ACCESS": "yes" if a.pubkey_file else "no", "REMOTE_AGENT": "yes" if a.remote_url else "no",
           "WIN_APPS": a.apps, "DISK_MODE": "largest"}
    ctx = {"build_id": a.build, "panel_url": a.panel_url, "report_token": rd(a.token_file),
           "panel_pubkey": rd(a.pubkey_file), "remote_agent_url": a.remote_url, "remote_meshid": rd(a.meshid_file)}
    xml = winconfig.render_autounattend(cfg, {"password": rd(a.password_file), "win_key": ""}, ctx)
    if a.image:
        xml = xml.replace('"image": "Windows 11 Pro"', '"image": "%s"' % a.image)
    if a.no_key:
        xml = re.sub(r"\s*<ProductKey><Key>[^<]*</Key><WillShowUI>OnError</WillShowUI></ProductKey>", "", xml)
        xml = re.sub(r"\s*<ProductKey>[^<]*</ProductKey>", "", xml)
    fd = os.open(a.out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(xml)
    print(f"{a.out}: {len(xml)} bytes")


if __name__ == "__main__":
    main()
