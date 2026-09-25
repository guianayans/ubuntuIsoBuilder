#!/usr/bin/env python3
"""Núcleo do ubuntu-autoinstall-builder.

Subcomandos:
  render      Gera nocloud/user-data, nocloud/meta-data e payload/uab.conf
  check       Valida um diretório nocloud gerado (estrutura Autoinstall etc.)
  grub        Gera grub.cfg/loopback.cfg a partir dos originais da ISO

Usa apenas a biblioteca padrão + PyYAML (jsonschema é opcional).
"""
from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import shlex
import string
import sys

import yaml

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

BASE_PACKAGES = ["curl", "wget", "git", "ca-certificates", "openssh-server",
                 "htop", "unzip", "rsync"]

HOSTNAME_RE = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
USER_RE = re.compile(r"^[a-z_][a-z0-9_-]{0,31}$")
PUBKEY_RE = re.compile(
    r"^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|"
    r"sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/=]+( .*)?$")
PKG_RE = re.compile(r"^[a-z0-9][a-z0-9+.-]+$")
SHA512_RE = re.compile(r"^\$6\$(rounds=\d+\$)?[./A-Za-z0-9]{1,16}\$[./A-Za-z0-9]{86}$")


# --------------------------------------------------------------------------- util
def load_env(path: str) -> dict:
    cfg = {}
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            key, sep, val = line.partition("=")
            if not sep or not re.match(r"^[A-Z_][A-Z0-9_]*$", key):
                raise SystemExit(f"linha inválida em {path}: {line[:40]}")
            parts = shlex.split(val)
            cfg[key] = parts[0] if parts else ""
    return cfg


def read_keys(path: str) -> list[str]:
    keys = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line and not line.startswith("#"):
                keys.append(line)
    return keys


class Dumper(yaml.SafeDumper):
    """Dumper que não gera aliases e mantém strings multilinha legíveis."""

    def ignore_aliases(self, data):  # noqa: D401
        return True


def _str_repr(dumper, data):
    if "\n" in data:
        return dumper.represent_scalar("tag:yaml.org,2002:str", data, style="|")
    return dumper.represent_scalar("tag:yaml.org,2002:str", data)


Dumper.add_representer(str, _str_repr)


def dump_yaml(obj) -> str:
    return yaml.dump(obj, Dumper=Dumper, sort_keys=False, allow_unicode=True, width=1000)


# --------------------------------------------------------------------------- render
def build_network(cfg: dict) -> dict:
    if cfg.get("NET_MODE", "dhcp") == "dhcp":
        common = {"dhcp4": True, "dhcp6": False, "dhcp-identifier": "mac", "optional": True}
        return {"version": 2, "ethernets": {
            "uab-en": {"match": {"name": "en*"}, **common},
            "uab-eth": {"match": {"name": "eth*"}, **common},
        }}
    mtype, mval = cfg["NET_MATCH_TYPE"], cfg["NET_MATCH_VALUE"]
    if mtype == "mac":
        match = {"macaddress": mval.lower()}
    elif mtype == "name":
        match = {"name": mval}
    else:
        match = {"name": mval or "en*"}
    eth = {
        "match": match,
        "dhcp4": False,
        "dhcp6": False,
        "addresses": [cfg["NET_ADDRESS"]],
        "routes": [{"to": "default", "via": cfg["NET_GATEWAY"]}],
        "nameservers": {"addresses": [d for d in cfg["NET_DNS"].split(",") if d]},
    }
    if cfg.get("NET_SEARCH"):
        eth["nameservers"]["search"] = [cfg["NET_SEARCH"]]
    return {"version": 2, "ethernets": {"uab-lan": eth}}


def build_storage_match(cfg: dict) -> dict:
    mtype, mval = cfg["STORAGE_MATCH_TYPE"], cfg.get("STORAGE_MATCH_VALUE", "")
    if mtype == "serial":
        return {"serial": mval if "*" in mval else f"*{mval}*"}
    if mtype == "model":
        return {"model": mval}
    if mtype == "path":
        return {"path": mval}
    if mtype in ("single", "largest"):
        return {"size": "largest"}
    raise SystemExit(f"STORAGE_MATCH_TYPE desconhecido: {mtype}")


def build_storage(cfg: dict) -> dict:
    layout = {"name": cfg.get("STORAGE_LAYOUT", "lvm")}
    if layout["name"] == "lvm":
        layout["sizing-policy"] = "all"
    layout["match"] = build_storage_match(cfg)
    return {"layout": layout}


def render(args) -> None:
    cfg = load_env(args.config)
    for ov in args.override or []:
        k, _, v = ov.partition("=")
        cfg[k] = v
    with open(args.password_hash, encoding="utf-8") as fh:
        pwhash = fh.read().strip()
    keys = read_keys(args.authorized_keys)
    for extra in args.extra_keys or []:
        keys += read_keys(extra)

    values = {
        "LOCALE": cfg["LOCALE"],
        "KEYBOARD_LAYOUT": cfg["KEYBOARD_LAYOUT"],
        "KEYBOARD_VARIANT": cfg.get("KEYBOARD_VARIANT", ""),
        "TIMEZONE": cfg["TIMEZONE"],
        "HOSTNAME": cfg["HOSTNAME_"],
        "ADMIN_USER": cfg["ADMIN_USER"],
        "ADMIN_REALNAME": cfg.get("ADMIN_REALNAME") or cfg["ADMIN_USER"],
        "PASSWORD_HASH": pwhash,
        "INSTANCE_ID": f"uab-{cfg['HOSTNAME_']}-{args.build_id}",
    }
    # json.dumps produz strings entre aspas duplas, que também são YAML válido.
    quoted = {k: json.dumps(v, ensure_ascii=False) for k, v in values.items()}

    with open(os.path.join(ROOT, "templates", "user-data.yaml"), encoding="utf-8") as fh:
        tpl = fh.read()
    text = string.Template(tpl).substitute(quoted)
    ud = yaml.safe_load(text)
    ai = ud["autoinstall"]
    ai["ssh"]["authorized-keys"] = keys
    ai["network"] = build_network(cfg)
    ai["storage"] = build_storage(cfg)

    os.makedirs(args.out_dir, exist_ok=True)
    header = ("#cloud-config\n"
              f"# Gerado por ubuntu-autoinstall-builder, build {args.build_id}.\n"
              "# Contém o hash da senha: não publique este arquivo.\n")
    write(os.path.join(args.out_dir, "user-data"), header + dump_yaml(ud), 0o600)
    with open(os.path.join(ROOT, "templates", "meta-data.yaml"), encoding="utf-8") as fh:
        md = string.Template(fh.read()).substitute(quoted)
    write(os.path.join(args.out_dir, "meta-data"), md, 0o644)
    write(os.path.join(args.out_dir, "vendor-data"), "", 0o644)

    if args.payload_conf:
        pkgs = BASE_PACKAGES + [p for p in cfg.get("EXTRA_PACKAGES", "").split() if p]
        conf = {
            "UAB_BUILD_ID": args.build_id,
            "UAB_HOSTNAME": cfg["HOSTNAME_"],
            "UAB_ADMIN_USER": cfg["ADMIN_USER"],
            "UAB_PACKAGES": " ".join(dict.fromkeys(pkgs)),
            # Coolify exige Docker (instalado antes, pelo repositório oficial)
            "UAB_DOCKER": "yes" if cfg.get("COOLIFY_ENABLED") == "yes" else cfg.get("DOCKER_ENABLED", "no"),
            "UAB_COOLIFY": "yes" if cfg.get("COOLIFY_ENABLED") == "yes" else "no",
            "UAB_COOLIFY_EMAIL": cfg.get("COOLIFY_ADMIN_EMAIL", ""),
            "UAB_COCKPIT": "yes" if cfg.get("COCKPIT_ENABLED") == "yes" else "no",
            "UAB_TAILSCALE": cfg.get("TAILSCALE_ENABLED", "no"),
            "UAB_TAILSCALE_HOSTNAME": cfg.get("TAILSCALE_HOSTNAME") or cfg["HOSTNAME_"],
            "UAB_TAILSCALE_EXTRA_ARGS": cfg.get("TAILSCALE_EXTRA_ARGS", ""),
            "UAB_STORAGE_MATCH_TYPE": cfg["STORAGE_MATCH_TYPE"],
            "UAB_STORAGE_MATCH_VALUE": cfg.get("STORAGE_MATCH_VALUE", ""),
            "UAB_STORAGE_MIN_GB": cfg.get("STORAGE_MIN_GB", "8"),
            "UAB_STORAGE_ALLOW_USB": cfg.get("STORAGE_ALLOW_USB", "no"),
            "UAB_SSH_AUTH": "password" if cfg.get("SSH_AUTH") == "password" else "key",
            "UAB_PANEL_URL": cfg.get("PANEL_URL", ""),
            # WireGuard pelo hub do painel (a configuração chega pelo relatório)
            "UAB_WG": "yes" if cfg.get("WG_ENABLED") == "yes" and cfg.get("PANEL_URL") else "no",
        }
        body = "# Configuração do provisionador (sem segredos)\n" + "".join(
            f"{k}={shlex.quote(v)}\n" for k, v in conf.items())
        write(args.payload_conf, body, 0o644)
    print(f"user-data gerado em {args.out_dir}")


def write(path: str, content: str, mode: int) -> None:
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        fh.write(content)
    os.chmod(tmp, mode)
    os.replace(tmp, path)


# --------------------------------------------------------------------------- check
class Checker:
    def __init__(self):
        self.results: list[tuple[str, bool, str]] = []

    def add(self, section: str, cond: bool, msg: str) -> bool:
        self.results.append((section, bool(cond), msg))
        return bool(cond)


def check(args) -> int:
    c = Checker()
    ud_path = os.path.join(args.dir, "user-data")
    md_path = os.path.join(args.dir, "meta-data")
    c.add("NoCloud", os.path.isfile(ud_path), "user-data existe")
    c.add("NoCloud", os.path.isfile(md_path), "meta-data existe")
    if not os.path.isfile(ud_path):
        return report(c, args.json)

    with open(ud_path, encoding="utf-8") as fh:
        text = fh.read()
    c.add("NoCloud", text.startswith("#cloud-config\n"), "user-data começa com #cloud-config")
    try:
        ud = yaml.safe_load(text)
        c.add("Autoinstall", True, "YAML do user-data válido")
    except yaml.YAMLError as exc:
        c.add("Autoinstall", False, f"YAML inválido: {exc}")
        return report(c, args.json)
    try:
        md = yaml.safe_load(open(md_path, encoding="utf-8")) if os.path.isfile(md_path) else None
        c.add("NoCloud", isinstance(md, dict) and "instance-id" in md, "meta-data com instance-id")
    except yaml.YAMLError as exc:
        c.add("NoCloud", False, f"meta-data inválido: {exc}")

    ai = (ud or {}).get("autoinstall")
    if not c.add("Autoinstall", isinstance(ai, dict), "chave 'autoinstall' presente"):
        return report(c, args.json)
    c.add("Autoinstall", ai.get("version") == 1, "autoinstall.version == 1")
    c.add("Autoinstall", ai.get("interactive-sections") == [], "sem seções interativas")
    c.add("Autoinstall", ai.get("shutdown") == "reboot", "reinicia automaticamente ao concluir")
    c.add("Autoinstall", any("preflight.sh" in str(x) for x in ai.get("early-commands", [])),
          "early-commands executa preflight do disco")
    c.add("Autoinstall", any("late-install.sh" in str(x) for x in ai.get("late-commands", [])),
          "late-commands instala o provisionador")

    schema_path = os.path.join(ROOT, "schema", "autoinstall-schema.json")
    try:
        import jsonschema  # type: ignore
        with open(schema_path, encoding="utf-8") as fh:
            schema = json.load(fh)
        try:
            jsonschema.validate(ai, schema)
            c.add("Autoinstall", True, "schema oficial do Subiquity: válido")
        except jsonschema.ValidationError as exc:
            c.add("Autoinstall", False, f"schema oficial do Subiquity: {exc.message} em {list(exc.path)}")
    except ImportError:
        c.add("Autoinstall", True, "schema oficial pulado (python3-jsonschema não instalado)")

    # Identity / hostname
    ident = ai.get("identity") or {}
    c.add("Hostname", bool(HOSTNAME_RE.match(str(ident.get("hostname", "")))),
          f"hostname válido: {ident.get('hostname')}")
    c.add("Autoinstall", bool(USER_RE.match(str(ident.get("username", "")))),
          f"usuário válido: {ident.get('username')}")
    c.add("Autoinstall", bool(SHA512_RE.match(str(ident.get("password", "")))),
          "senha é hash SHA-512 ($6$), não texto puro")
    for key in ("locale", "timezone", "keyboard"):
        c.add("Autoinstall", key in ai, f"{key} definido")

    # SSH
    ssh = ai.get("ssh") or {}
    keys = ssh.get("authorized-keys") or []
    ssh_auth = "key"
    if args.payload and os.path.isfile(os.path.join(args.payload, "uab.conf")):
        ssh_auth = load_env(os.path.join(args.payload, "uab.conf")).get("UAB_SSH_AUTH", "key")
    c.add("SSH", ssh.get("install-server") is True, "OpenSSH Server será instalado")
    if ssh_auth == "password":
        c.add("SSH", True, f"modo usuário e senha (senha só da rede local/Tailscale; {len(keys)} chave(s) opcional(is))")
    else:
        c.add("SSH", len(keys) >= 1, f"{len(keys)} chave(s) pública(s) autorizada(s)")
    c.add("SSH", all(PUBKEY_RE.match(k) for k in keys), "todas as chaves têm formato de chave pública")
    c.add("SSH", "PRIVATE KEY" not in text and "-----BEGIN" not in text,
          "nenhuma chave privada no user-data")

    # Network
    net = ai.get("network") or {}
    eths = net.get("ethernets") or {}
    ok_net = net.get("version") == 2 and len(eths) >= 1
    for name, e in eths.items():
        if not isinstance(e.get("match"), dict) or not e["match"]:
            ok_net = False
        if e.get("dhcp4"):
            continue
        try:
            addrs = [ipaddress.ip_interface(a) for a in e.get("addresses", [])]
            gws = [ipaddress.ip_address(r["via"]) for r in e.get("routes", []) if r.get("to") == "default"]
            ok_net &= bool(addrs) and bool(gws) and all(any(g in a.network for a in addrs) for g in gws)
            for d in (e.get("nameservers") or {}).get("addresses", []):
                ipaddress.ip_address(d)
        except (ValueError, KeyError, TypeError):
            ok_net = False
    desc = ", ".join("%s:%s" % (k, v.get("match")) for k, v in eths.items())
    c.add("Network", ok_net, f"netplan v2 válido com interface identificada por match ({desc})")

    # Storage
    st = ai.get("storage") or {}
    lay = st.get("layout") or {}
    match = lay.get("match") or {}
    c.add("Storage", lay.get("name") in ("lvm", "direct"), f"layout {lay.get('name')}")
    c.add("Storage", bool(match) and set(match) <= {"serial", "model", "path", "size"},
          f"disco identificado explicitamente: {match}")
    c.add("Storage", "password" not in lay, "sem LUKS (exigiria senha no console)")

    # Payload / Tailscale
    if args.payload:
        conf_path = os.path.join(args.payload, "uab.conf")
        if c.add("Tailscale", os.path.isfile(conf_path), "uab.conf presente no payload"):
            conf = load_env(conf_path)
            ts = conf.get("UAB_TAILSCALE")
            if ts == "yes":
                c.add("Tailscale", bool(HOSTNAME_RE.match(conf.get("UAB_TAILSCALE_HOSTNAME", ""))),
                      f"hostname Tailscale: {conf.get('UAB_TAILSCALE_HOSTNAME')}")
                if args.ts_key:
                    ok_key = os.path.isfile(args.ts_key) and re.match(
                        r"^tskey-(auth-)?[A-Za-z0-9_-]{10,}$", open(args.ts_key).read().strip())
                    c.add("Tailscale", bool(ok_key), "auth key presente e com formato válido")
                    if ok_key:
                        mode = os.stat(args.ts_key).st_mode & 0o777
                        c.add("Tailscale", mode & 0o077 == 0, f"permissão da auth key restrita ({oct(mode)})")
                keytext = open(args.ts_key).read().strip() if args.ts_key and os.path.isfile(args.ts_key) else None
                if keytext:
                    c.add("Tailscale", keytext not in text, "auth key NÃO aparece no user-data")
            else:
                c.add("Tailscale", True, "Tailscale desativado na configuração")
            c.add("Autoinstall", all(PKG_RE.match(p) for p in conf.get("UAB_PACKAGES", "").split()),
                  "lista de pacotes válida")
    return report(c, args.json)


def report(c: Checker, as_json: bool) -> int:
    if as_json:
        print(json.dumps([{"section": s, "ok": o, "msg": m} for s, o, m in c.results], ensure_ascii=False))
    else:
        for s, o, m in c.results:
            print(f"{'OK' if o else 'ERR'}\t{s}\t{m}")
    return 0 if all(o for _, o, _ in c.results) else 1


# --------------------------------------------------------------------------- grub
KERNEL_RE = re.compile(r"^\s*linux\s+(\S+)\s*(.*?)\s*---\s*$")
INITRD_RE = re.compile(r"^\s*initrd\s+(\S+)\s*$")


def parse_entries(cfg_text: str) -> list[dict]:
    """Extrai menuentries (título, kernel, args, initrd) do grub.cfg original."""
    entries, cur = [], None
    for line in cfg_text.splitlines():
        m = re.match(r'^\s*menuentry\s+["\']([^"\']+)["\']', line)
        if m:
            cur = {"title": m.group(1)}
            entries.append(cur)
            continue
        if cur is None:
            continue
        km = KERNEL_RE.match(line)
        if km:
            cur["kernel"], cur["args"] = km.group(1), km.group(2)
        im = INITRD_RE.match(line)
        if im:
            cur["initrd"] = im.group(1)
        if line.strip() == "}":
            cur = None
    return [e for e in entries if "kernel" in e and "initrd" in e and e["kernel"].startswith("/casper/")]


def grub(args) -> None:
    with open(args.orig, encoding="utf-8") as fh:
        orig = fh.read()
    entries = parse_entries(orig)
    if not entries:
        raise SystemExit("Não encontrei entradas /casper/ no grub.cfg original; ISO não suportada.")
    main = entries[0]
    hwe = next((e for e in entries[1:] if "hwe" in e["kernel"]), None)
    marker = f".uab-installed-{args.build_id}"
    # ';' é separador de comandos no GRUB: o argumento vai entre aspas simples
    # para o kernel receber exatamente ds=nocloud;s=/cdrom/nocloud/
    ai_args = "autoinstall 'ds=nocloud;s=/cdrom/nocloud/'"
    if args.extra_args:
        ai_args += " " + args.extra_args
    loop = args.loopback

    def ai_entry(e, title, eid):
        base = e["args"]
        a = f"{base} {ai_args}".strip()
        return (f'menuentry "{title}" --id {eid} {{\n'
                f"\tset gfxpayload=keep\n"
                f"\tlinux\t{e['kernel']} {a} ---\n"
                f"\tinitrd\t{e['initrd']}\n}}\n")

    out = [f"# Gerado por ubuntu-autoinstall-builder — build {args.build_id} — host {args.hostname}\n"]
    if not loop:
        out.append(
            "loadfont unicode\n"
            "set menu_color_normal=white/black\n"
            "set menu_color_highlight=black/light-gray\n"
            "\n"
            "# Proteção contra reinstalação: se o disco já contém uma instalação feita\n"
            "# por ESTA ISO (marcador gravado no /boot), inicia o sistema instalado.\n"
            "insmod part_gpt\ninsmod part_msdos\ninsmod ext2\n"
            "set uab_disk=\n"
            f"if search --no-floppy --file --set=uab_disk /{marker}; then\n"
            "\tset uab_cfg=/grub/grub.cfg\n"
            f"elif search --no-floppy --file --set=uab_disk /boot/{marker}; then\n"
            "\tset uab_cfg=/boot/grub/grub.cfg\n"
            "fi\n"
            'if [ -n "$uab_disk" ]; then\n'
            "\tset default=uab-installed\n"
            "\tset timeout=10\n"
            "else\n"
            "\tset default=uab-autoinstall\n"
            f"\tset timeout={args.timeout}\n"
            "fi\n\n"
            'if [ -n "$uab_disk" ]; then\n'
            f'menuentry "Iniciar o Ubuntu ja instalado ({args.hostname}) - NAO reinstala" --id uab-installed {{\n'
            "\tset root=$uab_disk\n"
            "\tconfigfile $uab_cfg\n"
            "}\n"
            "fi\n")
    out.append(ai_entry(main, f"Instalacao AUTOMATICA {args.hostname} (APAGA o disco de destino)", "uab-autoinstall"))
    if hwe:
        out.append(ai_entry(hwe, f"Instalacao AUTOMATICA {args.hostname} - kernel HWE (APAGA o disco)", "uab-autoinstall-hwe"))
    out.append("\n# ---- Entradas originais da ISO (instalacao manual) ----\n")
    body = []
    for line in orig.splitlines():
        if re.match(r"^\s*(set timeout=|loadfont |set menu_color_)", line):
            continue
        body.append(line)
    out.append("\n".join(body).strip("\n") + "\n")
    with open(args.out, "w", encoding="utf-8") as fh:
        fh.write("".join(out))
    print(f"{args.out}: kernel={main['kernel']} initrd={main['initrd']} hwe={'sim' if hwe else 'nao'}")


# --------------------------------------------------------------------------- main
def main() -> int:
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)

    r = sub.add_parser("render")
    r.add_argument("--config", required=True)
    r.add_argument("--password-hash", required=True)
    r.add_argument("--authorized-keys", required=True)
    r.add_argument("--extra-keys", action="append")
    r.add_argument("--out-dir", required=True)
    r.add_argument("--payload-conf")
    r.add_argument("--build-id", required=True)
    r.add_argument("--override", action="append", help="KEY=VALUE (usado pelo test-iso.sh)")

    k = sub.add_parser("check")
    k.add_argument("dir")
    k.add_argument("--payload")
    k.add_argument("--ts-key")
    k.add_argument("--json", action="store_true")

    g = sub.add_parser("grub")
    g.add_argument("--orig", required=True)
    g.add_argument("--out", required=True)
    g.add_argument("--build-id", required=True)
    g.add_argument("--hostname", required=True)
    g.add_argument("--timeout", type=int, default=10)
    g.add_argument("--extra-args", default="")
    g.add_argument("--loopback", action="store_true")

    a = ap.parse_args()
    if a.cmd == "render":
        render(a)
        return 0
    if a.cmd == "check":
        return check(a)
    grub(a)
    return 0


if __name__ == "__main__":
    sys.exit(main())
