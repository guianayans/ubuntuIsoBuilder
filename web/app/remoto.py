"""Remoto (MeshCentral) do ISO Builder em /remoto/: login único e dados para as ISOs.

O container do remoto publica, num volume só entre os dois, a chave de login
(loginkey) e o id do grupo "Servidores ISO Builder" (meshid)."""
from __future__ import annotations

import base64
import json
import os
import re
import time

SHARED = os.environ.get("REMOTO_SHARED", "/data/remoto")
PATH = "/remoto/"


def _read(name: str) -> str:
    try:
        with open(os.path.join(SHARED, name), encoding="utf-8") as fh:
            return fh.read().strip()
    except OSError:
        return ""


def available() -> bool:
    return bool(re.fullmatch(r"[0-9a-fA-F]{64,}", _read("loginkey")))


def mesh_id() -> str:
    m = _read("meshid")
    return m if re.fullmatch(r"[A-Za-z0-9@$_\-]{40,100}", m) else ""


def login_token(user: str = "admin", domain: str = "remoto") -> str:
    """Token de login do MeshCentral (encodeCookie: AES-256-GCM, iv|tag|dados em base64
    com + -> @ e / -> $). Vale 60 min; o MeshCentral só aceita com AllowLoginToken."""
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    key = bytes.fromhex(_read("loginkey"))[:32]
    data = json.dumps({"u": f"user/{domain}/{user}", "a": 3, "time": int(time.time())}, separators=(",", ":")).encode()
    iv = os.urandom(12)
    sealed = AESGCM(key).encrypt(iv, data, None)  # dados + tag (16 bytes no fim)
    raw = iv + sealed[-16:] + sealed[:-16]
    return base64.b64encode(raw).decode().replace("+", "@").replace("/", "$")


# --------------------------------------------------------------------- API do MeshCentral
# O painel fala com o remoto pela mesma API em tempo real do meshctrl (control.ashx),
# autenticado como admin com a chave de login (nunca sai do servidor).
WS_URL = os.environ.get("REMOTO_WS", "ws://remoto:4021/remoto/control.ashx")
MODES = {"desktop": 11, "terminal": 12, "files": 13}
HIDE_ALL = 63  # esconde cabeçalho, barra do topo, rodapé, título, menu lateral e abas do aparelho


def _auth_cookie(user: str = "admin", domain: str = "remoto") -> str:  # user = conta no MeshCentral
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    key = bytes.fromhex(_read("loginkey"))[:32]
    data = json.dumps({"userid": f"user/{domain}/{user}", "domainid": domain, "time": int(time.time())},
                      separators=(",", ":")).encode()
    iv = os.urandom(12)
    sealed = AESGCM(key).encrypt(iv, data, None)
    return base64.b64encode(iv + sealed[-16:] + sealed[:-16]).decode().replace("+", "@").replace("/", "$")


async def _call(requests: list[dict], want: set[str], timeout: float = 8.0, user: str = "admin") -> dict[str, dict]:
    """Envia as ações e devolve a 1ª resposta de cada ação pedida."""
    import asyncio
    import websockets
    got: dict[str, dict] = {}
    async with websockets.connect(WS_URL + "?auth=" + _auth_cookie(user), open_timeout=timeout,
                                  max_size=16 << 20) as ws:
        for r in requests:
            await ws.send(json.dumps(r))
        async def collect():
            while want - set(got):
                msg = json.loads(await ws.recv())
                a = msg.get("action")
                if a in want and a not in got:
                    got[a] = msg
        await asyncio.wait_for(collect(), timeout)
    return got


def short_id(full: str) -> str:
    return full.rsplit("/", 1)[-1]


async def devices(user: str = "admin") -> list[dict]:
    """Aparelhos que a conta enxerga (o MeshCentral filtra pelas permissões dela)."""
    r = await _call([{"action": "meshes"}, {"action": "nodes"}], {"meshes", "nodes"}, user=user)
    groups = {m.get("_id"): m.get("name", "") for m in r["meshes"].get("meshes", [])}
    out = []
    for mid, nodes in (r["nodes"].get("nodes") or {}).items():
        for n in nodes:
            out.append({
                "id": short_id(n.get("_id", "")), "name": n.get("name", ""), "group": groups.get(mid, ""),
                "online": bool((n.get("conn") or 0) & 1), "os": n.get("osdesc") or "", "ip": n.get("ip") or "",
                "icon": n.get("icon", 1), "last": n.get("lastconnect") or 0,
            })
    out.sort(key=lambda d: (not d["online"], d["name"].lower()))
    return out


async def invite_link(hours: int = 24) -> str:
    mid = mesh_id()
    if not mid:
        raise RuntimeError("grupo de servidores ainda não criado")
    r = await _call([{"action": "createInviteLink", "meshid": f"mesh/remoto/{mid}", "expire": hours, "flags": 0}],
                    {"createInviteLink"})
    url = r["createInviteLink"].get("url") or ""
    if not url:
        raise RuntimeError(r["createInviteLink"].get("result") or "o remoto não gerou o convite")
    return url


def view_url(node: str, mode: str, user: str = "admin") -> str:
    """Só o visualizador (tela/terminal/arquivos) do aparelho, já logado e sem a interface do MeshCentral."""
    if not re.fullmatch(r"[A-Za-z0-9@$_\-]{20,120}", node) or mode not in MODES:
        raise ValueError("aparelho ou visão inválidos")
    return f"{PATH}?login={login_token(user)}&gotonode={node}&viewmode={MODES[mode]}&hide={HIDE_ALL}"


# --------------------------------------------------------------------- sócios no MeshCentral
# Cada sócio do painel vira a conta "socio-<id>" no remoto, com permissão só nos
# aparelhos liberados. O MeshCentral aplica as permissões (tela/terminal/arquivos).
FULL_RIGHTS = 8 + 16 + 32 + 64 + 128 + 16384 + 32768   # como o --fullrights do meshctrl
R_REMOTE, R_NOTERMINAL, R_NOFILES, R_NODESKTOP = 8, 512, 1024, 65536


def partner_user(uid: int) -> str:
    return f"socio-{int(uid)}"


def device_rights(perms: int) -> int | None:
    """Bits do painel -> direitos do MeshCentral no aparelho (None = sem acesso)."""
    from . import core
    if not perms & core.P_VIEW:
        return None
    if perms & core.P_ALL == core.P_ALL:
        return FULL_RIGHTS
    remote = perms & (core.P_TERMINAL | core.P_DESKTOP | core.P_FILES)
    if not remote:
        return 0  # só ver
    return (R_REMOTE | (0 if perms & core.P_TERMINAL else R_NOTERMINAL)
            | (0 if perms & core.P_FILES else R_NOFILES) | (0 if perms & core.P_DESKTOP else R_NODESKTOP))


async def _ops(ops: list[dict], timeout: float = 15.0) -> list[dict]:
    """Envia várias ações do admin e espera a resposta de cada uma (responseid)."""
    import asyncio
    import websockets
    pending = {}
    for i, op in enumerate(ops):
        op["responseid"] = f"uab-{i}"
        pending[op["responseid"]] = None
    async with websockets.connect(WS_URL + "?auth=" + _auth_cookie(), open_timeout=timeout, max_size=16 << 20) as ws:
        for op in ops:
            await ws.send(json.dumps(op))

        async def collect():
            while any(v is None for v in pending.values()):
                msg = json.loads(await ws.recv())
                rid = msg.get("responseid")
                if rid in pending and pending[rid] is None:
                    pending[rid] = msg
        try:
            await asyncio.wait_for(collect(), timeout)
        except asyncio.TimeoutError:
            pass
    return [pending[f"uab-{i}"] or {} for i in range(len(ops))]


async def sync_partner(uid: int, name: str, machine_perms: dict[str, int], node_perms: dict[str, int],
                       hostnames: dict[str, str]) -> None:
    """Garante a conta do sócio e aplica as permissões em TODOS os aparelhos (dá ou tira)."""
    user = partner_user(uid)
    await _ops([{"action": "adduser", "username": user, "pass": base64.b64encode(os.urandom(24)).decode(),
                 "realname": name[:60] or user}])  # já existir não é erro
    want: dict[str, int] = {}
    host_of = {h.lower(): mid for mid, h in hostnames.items() if h}
    for d in await devices():
        perms = node_perms.get(d["id"], 0)
        mid = host_of.get(d["name"].lower())
        if mid:
            perms |= machine_perms.get(mid, 0)
        rights = device_rights(perms)
        want[d["id"]] = -1 if rights is None else rights
    ops = []
    for node, rights in want.items():
        op = {"action": "adddeviceuser", "nodeid": f"node/remoto/{node}", "userids": [f"user/remoto/{user}"]}
        if rights < 0:
            op.update(rights=0, remove=True)
        else:
            op.update(rights=rights)
        ops.append(op)
    if ops:
        await _ops(ops)


async def delete_partner(uid: int) -> None:
    await _ops([{"action": "deleteuser", "userid": f"user/remoto/{partner_user(uid)}"}])
