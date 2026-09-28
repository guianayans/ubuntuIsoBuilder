"""WireGuard pelo ponto de encontro (hub) — lado do painel.

Cada máquina recebe um slot n (1..254):
  10.77.n.1          servidor (a chave privada é gerada NO servidor)
  10.77.n.10..250    seus dispositivos (Mac, celular…), um por acesso gerado
  10.200.n.0/24      mapa da rede local do cliente (opcional, via NETMAP no servidor)
O painel grava <WG_STATE>/desired.json; o container do hub aplica.
"""
from __future__ import annotations

import base64
import ipaddress
import json
import os
import re
import secrets
import time

from . import core

WG_STATE = os.environ.get("WG_STATE", os.path.join(core.DATA_DIR, "wg"))
HUB_ENDPOINT = os.environ.get("HUB_ENDPOINT", "").strip()      # ex.: meu-ddns.exemplo.com:51822
if HUB_ENDPOINT and ":" not in HUB_ENDPOINT:  # só o endereço: completa com a porta do hub
    HUB_ENDPOINT = f"{HUB_ENDPOINT}:{os.environ.get('HUB_PORT', '51822').strip() or '51822'}"
HUB_SOCKS = os.environ.get("HUB_SOCKS", "wg-hub:1080")
NET = ipaddress.ip_network(os.environ.get("HUB_NET", "10.77.0.0/16"))
LANMAP = ipaddress.ip_network(os.environ.get("HUB_LANMAP", "10.200.0.0/16"))
HUB_IP = next(NET.hosts())
SOCKS_FILE = os.path.join(core.DATA_DIR, "panel", "hub-socks")
HANDSHAKE_OK = 180  # s: servidores mandam keepalive a cada 25 s
KEY_RE = re.compile(r"^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$")


def enabled() -> bool:
    return bool(re.match(r"^[A-Za-z0-9.-]+:\d{2,5}$", HUB_ENDPOINT))


def init_db() -> None:
    with core.db() as con:
        con.executescript(
            """
            CREATE TABLE IF NOT EXISTS wg_machines (
                machine_key TEXT PRIMARY KEY,
                slot INTEGER UNIQUE NOT NULL,
                server_pubkey TEXT NOT NULL,
                lan INTEGER NOT NULL DEFAULT 0,
                lan_cidr TEXT NOT NULL DEFAULT '',
                lan_if TEXT NOT NULL DEFAULT '',
                updated_at INTEGER NOT NULL
            );
            CREATE TABLE IF NOT EXISTS wg_clients (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                machine_key TEXT NOT NULL,
                name TEXT NOT NULL,
                pubkey TEXT NOT NULL,
                ip TEXT NOT NULL,
                created_at INTEGER NOT NULL,
                created_by TEXT NOT NULL DEFAULT ''
            );
            """
        )


def nets(slot: int):
    base = int(NET.network_address) + slot * 256
    lan = int(LANMAP.network_address) + slot * 256
    return (ipaddress.ip_network(f"{ipaddress.ip_address(base)}/24"),
            ipaddress.ip_network(f"{ipaddress.ip_address(lan)}/24"))


def server_ip(slot: int) -> str:
    return str(next(nets(slot)[0].hosts()))


# --------------------------------------------------------------------- hub
def socks_credentials() -> tuple[str, str]:
    os.makedirs(os.path.dirname(SOCKS_FILE), mode=0o700, exist_ok=True)
    if not os.path.exists(SOCKS_FILE):
        fd = os.open(SOCKS_FILE, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as fh:
            fh.write(secrets.token_urlsafe(24))
    return "painel", open(SOCKS_FILE).read().strip()


def hub_status() -> dict:
    try:
        with open(os.path.join(WG_STATE, "hub-status.json")) as fh:
            st = json.load(fh)
    except (OSError, json.JSONDecodeError):
        return {"online": False, "message": "Hub WireGuard não está rodando (serviço wg-hub)."}
    st["online"] = time.time() - st.get("updated", 0) < 60
    if not st["online"]:
        st["message"] = "Hub WireGuard parou de responder."
    return st


def write_desired() -> None:
    """Grava o estado desejado para o hub (sem nenhuma chave privada)."""
    user, pw = socks_credentials()
    with core.db() as con:
        # máquinas arquivadas ficam fora do hub (túnel pausado; volta igual ao restaurar)
        ms = con.execute("SELECT * FROM wg_machines WHERE machine_key NOT IN "
                         "(SELECT id FROM machines WHERE archived_at IS NOT NULL) ORDER BY slot").fetchall()
        cs = con.execute("SELECT * FROM wg_clients").fetchall()
    by = {}
    for c in cs:
        by.setdefault(c["machine_key"], []).append({"pubkey": c["pubkey"], "ip": c["ip"]})
    desired = {"socks": {"user": user, "password": pw}, "updated": int(time.time()),
               "machines": [{"slot": m["slot"], "server_pubkey": m["server_pubkey"], "lan": bool(m["lan"]),
                             "clients": by.get(m["machine_key"], [])} for m in ms]}
    os.makedirs(WG_STATE, exist_ok=True)
    tmp = os.path.join(WG_STATE, ".desired.tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as fh:
        json.dump(desired, fh)
    os.replace(tmp, os.path.join(WG_STATE, "desired.json"))


# --------------------------------------------------------------------- servidores (via relatório)
def register_server(machine_key: str, pubkey: str) -> dict:
    """Registra/atualiza a chave pública do servidor e devolve a config dele."""
    if not KEY_RE.match(pubkey or ""):
        return {"error": "chave pública WireGuard inválida"}
    with core.db() as con:
        row = con.execute("SELECT * FROM wg_machines WHERE machine_key=?", (machine_key,)).fetchone()
        if not row:
            used = {r[0] for r in con.execute("SELECT slot FROM wg_machines")}
            slot = next((n for n in range(1, 255) if n not in used), None)
            if slot is None:
                return {"error": "sem slots livres no hub (máximo 254 máquinas)"}
            con.execute("INSERT INTO wg_machines (machine_key,slot,server_pubkey,updated_at) VALUES (?,?,?,?)",
                        (machine_key, slot, pubkey, core.now()))
            changed = True
        else:
            changed = row["server_pubkey"] != pubkey
            if changed:
                con.execute("UPDATE wg_machines SET server_pubkey=?, updated_at=? WHERE machine_key=?",
                            (pubkey, core.now(), machine_key))
        row = con.execute("SELECT * FROM wg_machines WHERE machine_key=?", (machine_key,)).fetchone()
    if changed:
        write_desired()
    return server_config(row)


def server_config(row) -> dict:
    st = hub_status()
    if not st.get("pubkey"):
        return {"pending": "hub offline"}
    mnet, lmap = nets(row["slot"])
    cfg = {"address": f"{server_ip(row['slot'])}/32", "hub_pubkey": st["pubkey"], "endpoint": HUB_ENDPOINT,
           "allowed_ips": [f"{HUB_IP}/32", str(mnet)], "keepalive": 25, "net": str(NET), "lanmap_net": str(LANMAP)}
    if row["lan"] and row["lan_cidr"]:
        cfg["lan"] = {"cidr": row["lan_cidr"], "map": str(lmap), "iface": row["lan_if"]}
    return cfg


# --------------------------------------------------------------------- seus dispositivos
def gen_keypair() -> tuple[str, str]:
    from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey
    from cryptography.hazmat.primitives import serialization as ser
    k = X25519PrivateKey.generate()
    priv = k.private_bytes(ser.Encoding.Raw, ser.PrivateFormat.Raw, ser.NoEncryption())
    pub = k.public_key().public_bytes(ser.Encoding.Raw, ser.PublicFormat.Raw)
    return base64.b64encode(priv).decode(), base64.b64encode(pub).decode()


def machine_wg(machine_key: str):
    with core.db() as con:
        return con.execute("SELECT * FROM wg_machines WHERE machine_key=?", (machine_key,)).fetchone()


def create_client(machine_key: str, name: str, created_by: str) -> dict:
    """Cria um acesso para um dispositivo seu. A chave privada só existe nesta resposta."""
    m = machine_wg(machine_key)
    if not m:
        raise core.Invalid("Esta máquina ainda não se conectou ao hub (aguarde o primeiro relatório).")
    st = hub_status()
    if not st.get("pubkey"):
        raise core.Invalid("O hub WireGuard não está rodando.")
    mnet, lmap = nets(m["slot"])
    with core.db() as con:
        used = {r[0] for r in con.execute("SELECT ip FROM wg_clients WHERE machine_key=?", (machine_key,))}
        ip = next((str(h) for h in list(mnet.hosts())[9:250] if str(h) not in used), None)
        if not ip:
            raise core.Invalid("Limite de dispositivos para esta máquina atingido.")
        priv, pub = gen_keypair()
        con.execute("INSERT INTO wg_clients (machine_key,name,pubkey,ip,created_at,created_by) VALUES (?,?,?,?,?,?)",
                    (machine_key, name, pub, ip, core.now(), created_by))
    write_desired()
    allowed = [f"{server_ip(m['slot'])}/32"] + ([str(lmap)] if m["lan"] else [])
    conf = "\n".join([
        "[Interface]",
        f"# {name} -> servidor {server_ip(m['slot'])} (ISO Builder)",
        f"PrivateKey = {priv}",
        f"Address = {ip}/32",
        "",
        "[Peer]",
        f"PublicKey = {st['pubkey']}",
        f"Endpoint = {HUB_ENDPOINT}",
        f"AllowedIPs = {', '.join(allowed)}",
        "PersistentKeepalive = 25",
        "",
    ])
    qr = ""
    try:
        import segno
        import io
        buf = io.BytesIO()
        segno.make(conf, error="m").save(buf, kind="svg", scale=5, border=2, dark="#111", light="#fff")
        qr = "data:image/svg+xml;base64," + base64.b64encode(buf.getvalue()).decode()
    except ImportError:
        pass
    return {"name": name, "ip": ip, "config": conf, "qr": qr, "server_ip": server_ip(m["slot"]),
            "lan_map": str(lmap) if m["lan"] else ""}


def list_clients(machine_key: str) -> list[dict]:
    st = hub_status().get("peers", {})
    with core.db() as con:
        rows = con.execute("SELECT * FROM wg_clients WHERE machine_key=? ORDER BY id", (machine_key,)).fetchall()
    return [{"id": r["id"], "name": r["name"], "ip": r["ip"], "created_at": r["created_at"],
             "created_by": r["created_by"], "last_handshake": st.get(r["pubkey"], {}).get("last_handshake", 0)}
            for r in rows]


def delete_client(machine_key: str, cid: int) -> None:
    with core.db() as con:
        con.execute("DELETE FROM wg_clients WHERE id=? AND machine_key=?", (cid, machine_key))
    write_desired()


def set_lan(machine_key: str, enabled_: bool, cidr: str, iface: str) -> None:
    m = machine_wg(machine_key)
    if not m:
        raise core.Invalid("Esta máquina ainda não se conectou ao hub.")
    if enabled_:
        try:
            net = ipaddress.ip_network(cidr, strict=False)
        except ValueError:
            raise core.Invalid("Rede local inválida.")
        if net.version != 4 or net.prefixlen != 24:
            raise core.Invalid("Por enquanto o acesso à rede do cliente funciona com redes /24 (ex.: 192.168.0.0/24).")
        if net.overlaps(NET) or net.overlaps(LANMAP):
            raise core.Invalid("A rede local do cliente conflita com a rede da VPN.")
        if not re.match(r"^[A-Za-z0-9._-]{1,15}$", iface or ""):
            raise core.Invalid("Interface de rede inválida.")
        cidr = str(net)
    with core.db() as con:
        con.execute("UPDATE wg_machines SET lan=?, lan_cidr=?, lan_if=?, updated_at=? WHERE machine_key=?",
                    (1 if enabled_ else 0, cidr if enabled_ else "", iface if enabled_ else "", core.now(), machine_key))
    write_desired()


def forget_machine(machine_key: str) -> None:
    with core.db() as con:
        con.execute("DELETE FROM wg_clients WHERE machine_key=?", (machine_key,))
        con.execute("DELETE FROM wg_machines WHERE machine_key=?", (machine_key,))
    write_desired()


def machine_info(machine_key: str) -> dict | None:
    m = machine_wg(machine_key)
    if not m:
        return None
    st = hub_status()
    hs = st.get("peers", {}).get(m["server_pubkey"], {}).get("last_handshake", 0)
    return {"slot": m["slot"], "server_ip": server_ip(m["slot"]), "lan": bool(m["lan"]), "lan_cidr": m["lan_cidr"],
            "lan_if": m["lan_if"], "lan_map": str(nets(m["slot"])[1]), "last_handshake": hs,
            "connected": bool(hs) and time.time() - hs < HANDSHAKE_OK, "hub_online": st.get("online", False)}
