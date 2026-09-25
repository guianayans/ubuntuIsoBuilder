#!/usr/bin/env python3
"""Hub WireGuard do ISO Builder ("ponto de encontro").

Roda num container separado com NET_ADMIN. O painel grava o estado desejado em
<STATE>/desired.json; este processo:
  1. mantém a interface wg0 (10.77.0.1/16) com a chave privada do hub;
  2. aplica os peers (servidores e dispositivos) com `wg syncconf`;
  3. aplica o firewall de isolamento (cada dispositivo só alcança o servidor
     dele; servidores só respondem; nada sai para a internet/LAN da casa);
  4. oferece um SOCKS5 com usuário/senha, só para destinos da VPN, que o
     painel usa para abrir o terminal SSH;
  5. publica <STATE>/hub-status.json (chave pública, último handshake de cada peer).
"""
from __future__ import annotations

import asyncio
import ipaddress
import json
import os
import struct
import subprocess
import sys
import time

STATE = os.environ.get("HUB_STATE", "/state")
KEYDIR = os.environ.get("HUB_KEYDIR", "/hubkeys")
IFACE = os.environ.get("HUB_IFACE", "wg0")
PORT = int(os.environ.get("HUB_PORT", "51822"))
NET = ipaddress.ip_network(os.environ.get("HUB_NET", "10.77.0.0/16"))
LANMAP = ipaddress.ip_network(os.environ.get("HUB_LANMAP", "10.200.0.0/16"))
HUB_IP = next(NET.hosts())  # 10.77.0.1
SOCKS_LISTEN = os.environ.get("HUB_SOCKS", "0.0.0.0:1080")
CHAIN = "UAB-HUB"


def log(*a):
    print(time.strftime("%F %T"), "hub:", *a, flush=True)


def run(*cmd, input=None, check=True):
    r = subprocess.run(cmd, input=input, capture_output=True, text=True)
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(cmd)}: {r.stderr.strip() or r.stdout.strip()}")
    return r


# --------------------------------------------------------------------- chave e interface
def hub_keys() -> tuple[str, str]:
    os.makedirs(KEYDIR, mode=0o700, exist_ok=True)
    kp = os.path.join(KEYDIR, "hub.key")
    if not os.path.exists(kp):
        priv = run("wg", "genkey").stdout.strip()
        fd = os.open(kp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as fh:
            fh.write(priv + "\n")
    priv = open(kp).read().strip()
    pub = run("wg", "pubkey", input=priv + "\n").stdout.strip()
    return priv, pub


def ensure_iface(priv: str) -> None:
    if run("ip", "link", "show", IFACE, check=False).returncode != 0:
        run("ip", "link", "add", IFACE, "type", "wireguard")
        log(f"interface {IFACE} criada")
    kf = os.path.join(KEYDIR, "hub.key")
    run("wg", "set", IFACE, "listen-port", str(PORT), "private-key", kf)
    addr = f"{HUB_IP}/{NET.prefixlen}"
    if addr not in run("ip", "-4", "addr", "show", IFACE).stdout:
        run("ip", "addr", "add", addr, "dev", IFACE)
    run("ip", "link", "set", IFACE, "up")


# --------------------------------------------------------------------- estado desejado
def load_desired() -> dict:
    try:
        with open(os.path.join(STATE, "desired.json")) as fh:
            d = json.load(fh)
        return d if isinstance(d, dict) else {}
    except (FileNotFoundError, json.JSONDecodeError):
        return {}  # sem permissão de leitura: o erro aparece no log em vez de "0 peers"


def valid_key(k: str) -> bool:
    return isinstance(k, str) and len(k) == 44 and k.endswith("=")


def machine_nets(slot: int) -> tuple[ipaddress.IPv4Network, ipaddress.IPv4Network]:
    """Rede da máquina (10.77.<slot>.0/24) e o mapa da LAN dela (10.200.<slot>.0/24)."""
    base = int(NET.network_address) + slot * 256
    lan = int(LANMAP.network_address) + slot * 256
    return (ipaddress.ip_network(f"{ipaddress.ip_address(base)}/24"),
            ipaddress.ip_network(f"{ipaddress.ip_address(lan)}/24"))


def build(desired: dict):
    """Converte o estado desejado em config do wg + regras de firewall + rotas."""
    peers, rules, routes = [], [], []
    for m in desired.get("machines", []):
        slot = int(m.get("slot", 0))
        if not 1 <= slot <= 254 or not valid_key(m.get("server_pubkey", "")):
            continue
        mnet, lmap = machine_nets(slot)
        server_ip = next(mnet.hosts())  # 10.77.<slot>.1
        s_allowed = [f"{server_ip}/32"]
        if m.get("lan"):
            s_allowed.append(str(lmap))
            routes.append(str(lmap))
        peers.append((m["server_pubkey"], s_allowed))
        dests = [f"{server_ip}/32"] + ([str(lmap)] if m.get("lan") else [])
        for c in m.get("clients", []):
            try:
                cip = ipaddress.ip_address(c.get("ip", ""))
            except ValueError:
                continue
            if cip not in mnet or cip == server_ip or not valid_key(c.get("pubkey", "")):
                continue
            peers.append((c["pubkey"], [f"{cip}/32"]))
            for dst in dests:  # o dispositivo só alcança o servidor (e a LAN) desta máquina
                rules.append(["-i", IFACE, "-o", IFACE, "-s", f"{cip}/32", "-d", dst, "-j", "ACCEPT"])
    return peers, rules, routes


def wg_conf(priv: str, peers) -> str:
    lines = ["[Interface]", f"PrivateKey = {priv}", f"ListenPort = {PORT}", ""]
    for pub, allowed in peers:
        lines += ["[Peer]", f"PublicKey = {pub}", f"AllowedIPs = {', '.join(allowed)}", ""]
    return "\n".join(lines)


def apply_firewall(rules) -> None:
    """Cadeia própria: respostas liberadas; dispositivos -> servidor da máquina;
    todo o resto que entra pelo wg0 é descartado (inclusive para a internet)."""
    ipt = ["iptables", "-w"]
    if run(*ipt, "-L", CHAIN, "-n", check=False).returncode != 0:
        run(*ipt, "-N", CHAIN)
    run(*ipt, "-F", CHAIN)
    run(*ipt, "-A", CHAIN, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT")
    for r in rules:
        run(*ipt, "-A", CHAIN, *r)
    run(*ipt, "-A", CHAIN, "-i", IFACE, "-j", "DROP")
    run(*ipt, "-A", CHAIN, "-o", IFACE, "-j", "DROP")
    if run(*ipt, "-C", "FORWARD", "-j", CHAIN, check=False).returncode != 0:
        run(*ipt, "-I", "FORWARD", "1", "-j", CHAIN)
    # Ninguém da VPN fala com o próprio hub, exceto respostas (o painel usa o SOCKS pela rede interna).
    for args in (["-i", IFACE, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"],
                 ["-i", IFACE, "-j", "DROP"]):
        if run(*ipt, "-C", "INPUT", *args, check=False).returncode != 0:
            run(*ipt, "-A", "INPUT", *args)


_lan_routes: set[str] = set()


def apply_routes(routes) -> None:
    """Rotas dos mapas de LAN (10.200.<slot>.0/24) pelo wg0; remove as que saíram."""
    want = set(routes)
    for r in want:
        run("ip", "route", "replace", r, "dev", IFACE)
    for r in _lan_routes - want:
        run("ip", "route", "del", r, "dev", IFACE, check=False)
    _lan_routes.clear()
    _lan_routes.update(want)


def write_status(pub: str) -> None:
    peers = {}
    for line in run("wg", "show", IFACE, "dump").stdout.splitlines()[1:]:
        f = line.split("\t")
        if len(f) >= 8:
            peers[f[0]] = {"endpoint": f[2], "last_handshake": int(f[4] or 0), "rx": int(f[5] or 0), "tx": int(f[6] or 0)}
    st = {"pubkey": pub, "port": PORT, "net": str(NET), "lanmap": str(LANMAP), "hub_ip": str(HUB_IP),
          "updated": int(time.time()), "peers": peers}
    tmp = os.path.join(STATE, ".hub-status.tmp")
    with open(tmp, "w") as fh:
        json.dump(st, fh)
    os.chmod(tmp, 0o644)
    os.replace(tmp, os.path.join(STATE, "hub-status.json"))


# --------------------------------------------------------------------- SOCKS5 (só para o painel)
def socks_allowed(ip: str) -> bool:
    try:
        a = ipaddress.ip_address(ip)
    except ValueError:
        return False
    return (a in NET or a in LANMAP) and a != HUB_IP


async def socks_handle(reader, writer):
    async def fail(code=1):
        try:
            writer.write(b"\x05" + bytes([code]) + b"\x00\x01" + bytes(6))
            await writer.drain()
        finally:
            writer.close()
    try:
        head = await asyncio.wait_for(reader.readexactly(2), 10)
        methods = await reader.readexactly(head[1])
        if head[0] != 5 or 2 not in methods:
            writer.write(b"\x05\xff")
            await writer.drain()
            return writer.close()
        writer.write(b"\x05\x02")  # usuário/senha (RFC 1929)
        await writer.drain()
        ver, ulen = await reader.readexactly(2)
        user = (await reader.readexactly(ulen)).decode(errors="replace")
        plen = (await reader.readexactly(1))[0]
        pw = (await reader.readexactly(plen)).decode(errors="replace")
        want = load_desired().get("socks", {})
        if not want.get("password") or user != want.get("user") or pw != want.get("password"):
            writer.write(b"\x01\x01")
            await writer.drain()
            return writer.close()
        writer.write(b"\x01\x00")
        req = await reader.readexactly(4)
        if req[1] != 1 or req[3] != 1:  # só CONNECT para IPv4 literal
            return await fail(7 if req[1] != 1 else 8)
        ip = ".".join(str(b) for b in await reader.readexactly(4))
        port = struct.unpack(">H", await reader.readexactly(2))[0]
        if not socks_allowed(ip):
            return await fail(2)
        try:
            r2, w2 = await asyncio.wait_for(asyncio.open_connection(ip, port), 15)
        except (OSError, asyncio.TimeoutError):
            return await fail(4)
        writer.write(b"\x05\x00\x00\x01" + bytes(6))
        await writer.drain()

        async def pipe(a, b):
            try:
                while data := await a.read(65536):
                    b.write(data)
                    await b.drain()
            except (ConnectionError, OSError):
                pass
            finally:
                b.close()
        await asyncio.gather(pipe(reader, w2), pipe(r2, writer))
    except (asyncio.IncompleteReadError, asyncio.TimeoutError, ConnectionError, OSError):
        writer.close()


# --------------------------------------------------------------------- laço principal
async def reconcile_loop(priv: str, pub: str):
    last = None
    while True:
        try:
            desired = load_desired()
            peers, rules, routes = build(desired)
            sig = json.dumps([peers, rules, routes], sort_keys=True)
            if sig != last:
                conf = wg_conf(priv, peers)
                path = os.path.join(KEYDIR, "wg0.conf")
                fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
                with os.fdopen(fd, "w") as fh:
                    fh.write(conf)
                run("wg", "syncconf", IFACE, path)
                apply_firewall(rules)
                apply_routes(routes)
                log(f"aplicado: {len(peers)} peer(s), {len(rules)} regra(s), {len(routes)} rota(s) de LAN")
                last = sig
            write_status(pub)
        except Exception as exc:  # noqa: BLE001
            log("ERRO:", exc)
            last = None
        await asyncio.sleep(3)


async def main():
    priv, pub = hub_keys()
    ensure_iface(priv)
    os.makedirs(STATE, exist_ok=True)
    log(f"hub pronto: {IFACE} {HUB_IP}/{NET.prefixlen} porta {PORT}/udp, chave {pub[:10]}…")
    h, p = SOCKS_LISTEN.rsplit(":", 1)
    server = await asyncio.start_server(socks_handle, h, int(p))
    log(f"SOCKS5 (só destinos da VPN, com senha) em {SOCKS_LISTEN}")
    async with server:
        await asyncio.gather(server.serve_forever(), reconcile_loop(priv, pub))


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        sys.exit(0)
