"""Painel de máquinas: chave SSH do painel, Tailscale do container e o
terminal SSH no navegador (WebSocket <-> asyncssh)."""
from __future__ import annotations

import asyncio
import ipaddress
import json
import os
import re
import struct
import subprocess

from . import core

PANEL_DIR = os.path.join(core.DATA_DIR, "panel")
KEY_PATH = os.path.join(PANEL_DIR, "id_ed25519")
TS_SOCKET = os.environ.get("TS_SOCKET", os.path.join(core.DATA_DIR, "tailscale", "tailscaled.sock"))
SOCKS_ADDR = os.environ.get("PANEL_SOCKS", "127.0.0.1:1055")
# De onde a chave do painel é aceita nos servidores (opção from= do authorized_keys).
PANEL_KEY_FROM = os.environ.get("PANEL_KEY_FROM", "100.64.0.0/10,fd7a:115c:a1e0::/48")
ONLINE_SECONDS = 25 * 60


def ensure_panel_key() -> str:
    """Gera (uma vez) a chave SSH do painel e devolve a pública."""
    os.makedirs(PANEL_DIR, mode=0o700, exist_ok=True)
    if not os.path.exists(KEY_PATH):
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "iso-builder-painel", "-f", KEY_PATH],
                       check=True, capture_output=True)
    os.chmod(KEY_PATH, 0o600)
    return open(KEY_PATH + ".pub").read().strip()


def panel_key_line(extra_from: list[str] | None = None) -> str:
    """Linha de authorized_keys com restrições (só a partir do Tailscale e do hub)."""
    frm = ",".join([x.strip() for x in PANEL_KEY_FROM.split(",") if x.strip()] + list(extra_from or []))
    return f'from="{frm}",no-agent-forwarding,no-X11-forwarding,no-port-forwarding {ensure_panel_key()}'


# --------------------------------------------------------------------- Tailscale do painel
def ts_status() -> dict:
    """Estado do Tailscale do próprio painel (userspace, dentro do container)."""
    if not os.path.exists(TS_SOCKET):
        return {"state": "off", "message": "Tailscale do painel não está em execução (defina TS_AUTHKEY no Coolify)."}
    try:
        r = subprocess.run(["tailscale", f"--socket={TS_SOCKET}", "status", "--json"],
                           capture_output=True, text=True, timeout=8)
        d = json.loads(r.stdout or "{}")
    except (OSError, subprocess.TimeoutExpired, json.JSONDecodeError) as exc:
        return {"state": "error", "message": f"falha ao consultar o Tailscale: {exc}"}
    st = d.get("BackendState", "")
    me = d.get("Self") or {}
    out = {"state": st, "ips": me.get("TailscaleIPs") or [], "dns": (me.get("DNSName") or "").rstrip("."),
           "hostname": me.get("HostName", "")}
    if st == "NeedsLogin":
        out["message"] = "O painel precisa de login no Tailscale: defina TS_AUTHKEY no Coolify e reinicie."
    elif st != "Running":
        out["message"] = f"Tailscale do painel em estado {st or 'desconhecido'}."
    return out


def ts_running() -> bool:
    return ts_status().get("state") == "Running"


# --------------------------------------------------------------------- conexão
async def socks5_connect(host: str, port: int, proxy: str = SOCKS_ADDR, auth: tuple[str, str] | None = None):
    """Conecta ao destino através do SOCKS5 do tailscaled (sem autenticação) e
    devolve o socket já conectado (pronto para o asyncssh)."""
    import socket
    loop = asyncio.get_running_loop()
    ph, pp = proxy.rsplit(":", 1)
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.setblocking(False)

    async def recv_exact(n: int) -> bytes:
        buf = b""
        while len(buf) < n:
            chunk = await asyncio.wait_for(loop.sock_recv(sock, n - len(buf)), 20)
            if not chunk:
                raise ConnectionError("proxy SOCKS5 fechou a conexão")
            buf += chunk
        return buf

    try:
        await asyncio.wait_for(loop.sock_connect(sock, (ph, int(pp))), 10)
        await loop.sock_sendall(sock, b"\x05\x01\x02" if auth else b"\x05\x01\x00")
        if await recv_exact(2) != (b"\x05\x02" if auth else b"\x05\x00"):
            raise ConnectionError("proxy SOCKS5 recusou o método de autenticação")
        if auth:
            u, p = auth[0].encode(), auth[1].encode()
            await loop.sock_sendall(sock, b"\x01" + bytes([len(u)]) + u + bytes([len(p)]) + p)
            if await recv_exact(2) != b"\x01\x00":
                raise ConnectionError("proxy SOCKS5: usuário/senha recusados")
        try:
            ip = ipaddress.ip_address(host)
            addr = (b"\x01" if ip.version == 4 else b"\x04") + ip.packed
        except ValueError:
            hb = host.encode("idna")
            addr = b"\x03" + bytes([len(hb)]) + hb
        await loop.sock_sendall(sock, b"\x05\x01\x00" + addr + struct.pack(">H", port))
        head = await recv_exact(4)
        if head[1] != 0:
            codes = {1: "falha geral", 2: "não permitido", 3: "rede inalcançável", 4: "máquina inalcançável",
                     5: "conexão recusada", 6: "tempo esgotado"}
            raise ConnectionError(f"SOCKS5: {codes.get(head[1], head[1])}")
        n = 4 if head[3] == 1 else 16 if head[3] == 4 else (await recv_exact(1))[0]
        await recv_exact(n + 2)
        return sock
    except BaseException:
        sock.close()
        raise


def ssh_target(m) -> tuple[str, int, bool]:
    """Escolhe host/porta: endereço manual > túnel WireGuard (conectado) > Tailscale."""
    rep = json.loads(m["report"] or "{}")
    if m["ssh_host"]:
        host = m["ssh_host"]
        via_ts = host.startswith("100.") or host.endswith(".ts.net")
        return host, int(m["ssh_port"] or 22), via_ts
    from . import wg
    wi = wg.machine_info(m["id"]) if wg.enabled() else None
    if wi and wi["connected"]:
        return wi["server_ip"], 22, False
    ts_ips = [ip for ip in (rep.get("tailscale") or {}).get("ips", []) if "." in ip]
    if ts_ips:
        return ts_ips[0], 22, True
    raise LookupError("A máquina ainda não informou um IP do Tailscale. Defina um endereço em Configurações.")


async def open_ssh(m, username: str):
    import asyncssh  # dependência só do terminal

    from . import wg
    host, port, via_ts = ssh_target(m)
    kw = dict(username=username, client_keys=[KEY_PATH], known_hosts=None, agent_path=None,
              connect_timeout=20, keepalive_interval=30)
    try:
        in_wg = ipaddress.ip_address(host) in wg.NET or ipaddress.ip_address(host) in wg.LANMAP
    except ValueError:
        in_wg = False
    if in_wg and wg.enabled():
        sock = await socks5_connect(host, port, proxy=wg.HUB_SOCKS, auth=wg.socks_credentials())
        return await asyncssh.connect(sock=sock, **kw), f"{host} (via WireGuard do seu servidor)"
    if via_ts and os.path.exists(TS_SOCKET) and os.environ.get("PANEL_DIRECT") != "1":
        sock = await socks5_connect(host, port)  # socket já conectado pelo proxy
        return await asyncssh.connect(sock=sock, **kw), f"{host} (via Tailscale do painel)"
    return await asyncssh.connect(host, port, **kw), f"{host}:{port}"


def machine_row(r, builds: dict | None = None) -> dict:
    rep = json.loads(r["report"] or "{}")
    ts = rep.get("tailscale") or {}
    online = core.now() - r["last_seen"] < ONLINE_SECONDS
    b = (builds or {}).get(r["build_id"]) or {}
    return {
        "id": r["id"], "build_id": r["build_id"], "hostname": r["hostname"], "label": r["label"],
        "notes": r["notes"], "first_seen": r["first_seen"], "last_seen": r["last_seen"], "online": online,
        "ssh_host": r["ssh_host"], "ssh_port": r["ssh_port"], "source_ip": r["source_ip"],
        "admin_user": rep.get("admin_user", ""), "ssh_auth": rep.get("ssh_auth", ""),
        "tailscale_ip": next((ip for ip in ts.get("ips", []) if "." in ip), ""),
        "tailscale_dns": ts.get("dns", ""), "tailscale_state": ts.get("state", ""),
        "local_ips": rep.get("local_ips", []), "os": rep.get("os", ""), "kernel": rep.get("kernel", ""),
        "uptime": rep.get("uptime_seconds"), "provisioned": rep.get("provisioned", ""),
        "steps": rep.get("steps", {}), "services": rep.get("services", {}), "resources": rep.get("resources", {}),
        "panel_access": bool(b.get("panel_access")), "build_hostname": b.get("hostname", ""),
        "wg_ip": (rep.get("wg") or {}).get("address", "").split("/")[0] if isinstance(rep.get("wg"), dict) else "",
        "report": rep,
    }


# --------------------------------------------------------------------- validação do relatório
SAFE = re.compile(r"[^\w .,:;/@+()=%<>\[\]{}|#~'\"!?-]")


def clean(v, depth=0):
    """Limita tamanho e tipos do relatório enviado pela máquina."""
    if depth > 4:
        return None
    if isinstance(v, dict):
        return {str(k)[:40]: clean(x, depth + 1) for k, x in list(v.items())[:60]}
    if isinstance(v, list):
        return [clean(x, depth + 1) for x in v[:40]]
    if isinstance(v, bool) or v is None:
        return v
    if isinstance(v, (int, float)):
        return v
    return SAFE.sub("", str(v))[:300]
