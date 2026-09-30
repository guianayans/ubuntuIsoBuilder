"""API e páginas da interface web do ubuntu-autoinstall-builder."""
from __future__ import annotations

import asyncio
import io
import json
import os
import re
import secrets
import struct
import time
import zipfile

from fastapi import Depends, FastAPI, HTTPException, Request, Response, WebSocket, WebSocketDisconnect
from fastapi.responses import FileResponse, JSONResponse, PlainTextResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles

from . import builder, core, panel, remoto, terminalsec, wg, winstore

STATIC_DIR = os.path.join(os.path.dirname(__file__), "..", "static")
HELPER_DIR = os.environ.get("HELPER_DIST", os.path.join(builder.UAB_ROOT, "helper", "dist"))
PUBLIC_URL = os.environ.get("PUBLIC_URL", "https://iso.gvtserver.online").rstrip("/")
# domínio dos e-mails gerados para convidados (vem de PUBLIC_URL)
# Domínio FIXO dos e-mails das contas temporárias (o admin escolhe só o prefixo).
GUEST_DOMAIN = (os.environ.get("GUEST_EMAIL_DOMAIN") or "gvtserver.online").strip().lower()
PREFIX_RE = re.compile(r"^[a-z0-9]([a-z0-9._-]{0,38}[a-z0-9])?$")
COOKIE = "uab_session"
MAX_BODY = 256 * 1024

# Atalho no topo (só admin) para o acesso remoto; vazio = sem o botão.
# Padrão: o remoto do próprio ISO Builder em /remoto/ (serviço "remoto" da stack),
# aberto já logado. REMOTE_URL=https://... usa outro (ex.: o remote antigo).
# REMOTE_EMBED=true abre DENTRO do painel; false = abre em nova aba.
REMOTE_URL = os.environ.get("REMOTE_URL", "/remoto/").strip()
REMOTE_LOCAL = REMOTE_URL == "/remoto/"
if not (REMOTE_LOCAL or REMOTE_URL.startswith("https://")):
    REMOTE_URL = ""
REMOTE_ORIGIN = "/".join(REMOTE_URL.split("/")[:3]) if REMOTE_URL and not REMOTE_LOCAL else ""
REMOTE_EMBED = bool(REMOTE_URL) and os.environ.get("REMOTE_EMBED", "true").strip().lower() in ("1", "true", "yes", "sim")

app = FastAPI(title="Ubuntu Autoinstall Builder", docs_url=None, redoc_url=None, openapi_url=None)


@app.on_event("startup")
def _startup() -> None:
    core.init_db()
    builder.start()
    panel.ensure_panel_key()
    wg.init_db()
    if wg.enabled():
        wg.write_desired()
    if not core._ADMIN_HASH:
        print("AVISO: ADMIN_PASSWORD ausente ou com menos de 12 caracteres: login do admin DESATIVADO.", flush=True)


@app.middleware("http")
async def security(request: Request, call_next):
    # Mutações só com JSON da própria origem (proteção CSRF, junto com SameSite=Strict).
    # Exceções: chamadas de programas autenticadas por token (sem cookie), não pelo navegador:
    # relatório das máquinas (/api/agent/…), aviso do gravador sobre o agente (/t/<token>/agent)
    # e andamento do "Formatar esta máquina" (/f/<token>/report).
    machine_call = request.url.path.startswith("/api/agent/") or bool(
        re.fullmatch(r"/t/[A-Za-z0-9_-]{20,100}/agent|/f/[A-Za-z0-9_-]{20,100}/report", request.url.path))
    if request.method in ("POST", "PUT", "PATCH", "DELETE") and not machine_call:
        if request.headers.get("x-requested-with") != "uab":
            return JSONResponse({"error": "requisição inválida"}, 403)
        origin = request.headers.get("origin")
        host = request.headers.get("x-forwarded-host") or request.headers.get("host")
        if origin and origin.split("://", 1)[-1] != host:
            return JSONResponse({"error": "origem inválida"}, 403)
        # envio da ISO do Windows (admin): arquivo grande, gravado em disco por streaming
        big_upload = request.url.path == "/api/admin/win-iso/upload"
        if not big_upload and int(request.headers.get("content-length") or 0) > MAX_BODY:
            return JSONResponse({"error": "requisição grande demais"}, 413)
    resp = await call_next(request)
    if request.url.path.startswith("/static/"):
        # com ?v= (endereço versionado) pode ficar em cache; sem, sempre revalida
        resp.headers["Cache-Control"] = ("public, max-age=31536000, immutable" if request.query_params.get("v")
                                         else "no-cache")
    resp.headers["X-Content-Type-Options"] = "nosniff"
    resp.headers["X-Frame-Options"] = "DENY"
    resp.headers["Referrer-Policy"] = "strict-origin-when-cross-origin"
    resp.headers["Content-Security-Policy"] = (
        "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; "
        f"connect-src 'self'; frame-src 'self' {REMOTE_ORIGIN if REMOTE_EMBED else ''} {terminalsec.origin()}; frame-ancestors 'none'; "
        "base-uri 'none'; form-action 'self'")
    if request.url.path.startswith("/api/"):
        resp.headers.setdefault("Cache-Control", "no-store")
    return resp


def client_ip(request: Request) -> str:
    fwd = request.headers.get("x-forwarded-for", "")
    return (fwd.split(",")[0].strip() if fwd else (request.client.host if request.client else "?"))


def current_user(request: Request) -> core.User:
    u = core.session_user(request.cookies.get(COOKIE))
    if not u:
        raise HTTPException(401, "faça login")
    return u


def admin_user(u: core.User = Depends(current_user)) -> core.User:
    if not u.is_admin:
        raise HTTPException(403, "apenas o administrador")
    return u


@app.exception_handler(HTTPException)
async def http_err(_: Request, exc: HTTPException):
    return JSONResponse({"error": exc.detail}, exc.status_code)


@app.exception_handler(core.Invalid)
async def invalid_err(_: Request, exc: core.Invalid):
    return JSONResponse({"error": str(exc)}, 400)


async def body(request: Request) -> dict:
    try:
        d = await request.json()
    except (json.JSONDecodeError, UnicodeDecodeError):
        raise HTTPException(400, "JSON inválido")
    if not isinstance(d, dict):
        raise HTTPException(400, "JSON inválido")
    return d


# ------------------------------------------------------------------ sessão
@app.post("/api/login")
async def login(request: Request, response: Response):
    d = await body(request)
    email = str(d.get("email") or "").strip().lower()[:120]
    ip = client_ip(request)
    if core.limiter.blocked(f"ip:{ip}", limit=20, window=900) or core.limiter.blocked(f"em:{email}", limit=6, window=900):
        raise HTTPException(429, "Muitas tentativas. Aguarde 15 minutos.")
    user = core.authenticate(email, str(d.get("password") or ""))
    if not user:
        core.limiter.hit(f"ip:{ip}", f"em:{email}")
        raise HTTPException(401, "E-mail ou senha incorretos, ou acesso expirado.")
    core.limiter.clear(f"em:{email}")
    token = core.create_session(user, ip)
    response.set_cookie(COOKIE, token, httponly=True, secure=core.COOKIE_SECURE, samesite="strict",
                        max_age=core.SESSION_HOURS * 3600, path="/")
    return {"user": user.public()}


@app.post("/api/logout")
async def logout(request: Request, response: Response):
    core.delete_session(request.cookies.get(COOKIE))
    response.delete_cookie(COOKIE, path="/")
    return {"ok": True}


@app.get("/api/remoto/login")
def remoto_login(u: core.User = Depends(current_user)):
    """Endereço do remoto já logado (token de 60 min) com a conta de quem pediu."""
    if REMOTE_LOCAL and remoto.available():
        return {"url": remoto.PATH + "?login=" + remoto.login_token(_remoto_user(u))}
    if not u.is_admin:
        raise HTTPException(403, "sem acesso ao Remoto")
    return {"url": REMOTE_URL}


def _remoto_ready():
    if not (REMOTE_LOCAL and remoto.available()):
        raise HTTPException(404, "O remoto ainda não está no ar.")


@app.get("/api/remoto/devices")
async def remoto_devices(u: core.User = Depends(current_user)):
    _remoto_ready()
    who = _remoto_user(u)
    import time as _t
    if u.is_partner and _t.time() - _partner_synced.get(u.id, 0) > 600:
        await _sync_partner(u.id)  # máquinas novas com o mesmo nome entram sozinhas
    try:
        return {"devices": await remoto.devices(who), "group_ready": bool(remoto.mesh_id())}
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(502, f"O remoto não respondeu ({type(exc).__name__}).")


@app.post("/api/admin/remoto/invite")
async def remoto_invite(request: Request, _: core.User = Depends(admin_user)):
    _remoto_ready()
    try:
        hours = int((await request.json()).get("hours") or 24)
    except (ValueError, TypeError):
        hours = 24
    try:
        return {"url": await remoto.invite_link(max(1, min(hours, 24 * 30)))}
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(502, f"Não foi possível gerar o convite: {exc}")


@app.get("/api/remoto/view")
def remoto_view(node: str, mode: str = "desktop", u: core.User = Depends(current_user)):
    _remoto_ready()
    try:  # o MeshCentral recusa o que a conta não pode (tela/terminal/arquivos)
        return {"url": remoto.view_url(node, mode, _remoto_user(u))}
    except ValueError as exc:
        raise HTTPException(400, str(exc))


@app.get("/api/me")
def me(u: core.User = Depends(current_user)):
    remote_ok = REMOTE_LOCAL and remoto.available() and (u.is_admin or (u.is_partner and bool(core.partner_perms(u.id))))
    return {"user": u.public(), "admin_email": core.ADMIN_EMAIL if u.is_admin else None,
            "features": {"wireguard": wg.enabled(),
                         "remote_url": (REMOTE_URL if (u.is_admin and (not REMOTE_LOCAL or remoto.available())) or remote_ok else ""),
                         "remote_embed": REMOTE_EMBED and (u.is_admin or remote_ok),
                         "remote_sso": remote_ok,
                         "remote_agent": bool(remoto.mesh_id()),
                         "terminalsec": terminalsec.configured(),
                         "windows": bool(core.winconfig())}}


@app.get("/api/terminalsec/status")
def terminalsec_status(u: core.User = Depends(current_user)):
    """Atalho do TerminalSec no header (só admin): verde conectado, vermelho sem conexão."""
    if not u.is_admin or not terminalsec.configured():
        raise HTTPException(404, "TerminalSec não configurado")
    return terminalsec.status()


@app.get("/api/windows/apps")
def windows_apps(u: core.User = Depends(current_user)):
    """Catálogo de apps do Windows (winget) para o assistente, com as opções de região."""
    if u.download_only or u.is_partner:
        raise HTTPException(403, "sem acesso")
    return {"available": bool(core.winconfig()), "apps": core.win_apps(),
            "timezones": core.WIN_TIMEZONES, "keyboards": core.WIN_KEYBOARDS}


# ------------------------------------------------------------------ contas temporárias (admin)
DURATIONS = {"1h": 3600, "24h": 86400, "3d": 3 * 86400, "7d": 7 * 86400, "30d": 30 * 86400}


def _user_row(r) -> dict:
    with core.db() as con:
        n = con.execute("SELECT COUNT(*) FROM builds WHERE user_id=?", (r["id"],)).fetchone()[0]
        linked = con.execute("SELECT hostname, status FROM builds WHERE id=?", (r["build_id"],)).fetchone() \
            if r["build_id"] else None
    state = "revogada" if r["revoked"] else ("expirada" if r["expires_at"] <= core.now() else "ativa")
    return {"id": r["id"], "email": r["email"], "label": r["label"], "created_at": r["created_at"],
            "expires_at": r["expires_at"], "last_login": r["last_login"], "state": state,
            "max_builds": r["max_builds"], "builds": n, "mode": r["mode"], "build_id": r["build_id"],
            "build_hostname": linked["hostname"] if linked else None,
            "build_ok": bool(linked and linked["status"] in ("done", "queued", "running")),
            "build_status": linked["status"] if linked else None,
            "perm_count": len(core.partner_perms(r["id"])) if r["mode"] == "partner" else 0,
            "remote_agent": bool(r["remote_agent"]), "agent": {"host": r["agent_host"], "os": r["agent_os"],
            "state": r["agent_state"], "message": r["agent_msg"], "at": r["agent_at"]}}


def _link_build(con, build_id: str, until: int, allow_pending: bool = False) -> str:
    """Valida a ISO a vincular e estende a validade dela até o fim do acesso.
    allow_pending: ISO ainda sendo gerada (entrega criada junto com a ISO)."""
    b = con.execute("SELECT * FROM builds WHERE id=?", (build_id,)).fetchone()
    ok = b and ((b["status"] == "done" and b["iso_path"]) or (allow_pending and b["status"] in ("queued", "running")))
    if not ok:
        raise core.Invalid("Escolha uma ISO pronta para vincular ao acesso.")
    con.execute("UPDATE builds SET expires_at=MAX(COALESCE(expires_at,0), ?) WHERE id=?", (until, build_id))
    return b["hostname"]


@app.get("/api/admin/users")
def list_users(_: core.User = Depends(admin_user)):
    with core.db() as con:
        rows = con.execute("SELECT * FROM users ORDER BY created_at DESC").fetchall()
    return {"users": [_user_row(r) for r in rows]}


def guest_email(prefix: str) -> str:
    """prefixo -> prefixo@gvtserver.online (vazio = prefixo aleatório)."""
    prefix = (prefix or "").strip().lower()
    if "@" in prefix:  # colou o e-mail inteiro: aceita só se já for do domínio fixo
        local, _, dom = prefix.partition("@")
        if dom != GUEST_DOMAIN:
            raise core.Invalid(f"Use só o prefixo: o domínio é sempre @{GUEST_DOMAIN}.")
        prefix = local
    if not prefix:
        prefix = f"convidado-{secrets.token_hex(3)}"
    if not PREFIX_RE.match(prefix):
        raise core.Invalid("Prefixo inválido: use letras minúsculas, números, ponto, hífen ou _ (2 a 40).")
    email = f"{prefix}@{GUEST_DOMAIN}"
    if email == core.ADMIN_EMAIL:
        raise core.Invalid("Esse e-mail é da conta administradora.")
    return email


def create_temp_user(con, *, prefix: str, label: str, duration: str, mode: str, build_id: str | None,
                     max_builds: int, allow_pending: bool = False, remote_agent: bool = False) -> dict:
    """Cria um acesso temporário (senha aleatória, mostrada só nesta resposta)."""
    email = guest_email(prefix)
    dur = DURATIONS.get(duration or "24h")
    if not dur:
        raise core.Invalid("Validade inválida.")
    until = core.now() + dur
    hostname = None
    if con.execute("SELECT 1 FROM users WHERE email=?", (email,)).fetchone():
        raise core.Invalid(f"Já existe um acesso {email}. Revogue/exclua o antigo ou use outro prefixo.")
    if mode == "download":
        hostname = _link_build(con, build_id or "", until, allow_pending=allow_pending)
        max_builds = 0
    password = core.generate_password()
    agent = 1 if (remote_agent and mode == "download" and _agent_available()) else 0
    con.execute("INSERT INTO users (email,label,pw_hash,created_at,expires_at,max_builds,mode,build_id,remote_agent) "
                "VALUES (?,?,?,?,?,?,?,?,?)",
                (email, label, core.hash_password(password), core.now(), until, max_builds, mode,
                 build_id if mode == "download" else None, agent))
    return {"email": email, "password": password, "expires_at": until, "url": PUBLIC_URL,
            "mode": mode, "iso": hostname}


def _agent_available() -> bool:
    """O Remoto deste painel está pronto para receber agentes (grupo criado)?"""
    return bool(REMOTE_LOCAL and remoto.available() and remoto.mesh_id())


def _bundle_agent(u: core.User) -> dict:
    """Gravador baixado PELA PRÓPRIA conta do cliente com "acesso remoto junto": leva o agente
    do Remoto (instalado só com a permissão da pessoa na tela do gravador)."""
    if not u.download_only or not _agent_available():
        return {}
    with core.db() as con:
        r = con.execute("SELECT remote_agent FROM users WHERE id=?", (u.id,)).fetchone()
    if not r or not r["remote_agent"]:
        return {}
    return {"agent": {"url": PUBLIC_URL.rstrip("/") + "/remoto", "meshid": remoto.mesh_id(), "account": u.email}}


@app.post("/api/admin/users/{uid}/agent")
async def user_agent_toggle(uid: int, request: Request, _: core.User = Depends(admin_user)):
    """Liga/desliga "instalar acesso remoto junto" no gravador de uma conta "só download"."""
    on = bool((await body(request)).get("enabled"))
    if on and not _agent_available():
        raise core.Invalid("O Remoto deste painel não está disponível.")
    with core.db() as con:
        r = con.execute("SELECT mode FROM users WHERE id=?", (uid,)).fetchone()
        if not r or r["mode"] != "download":
            raise HTTPException(404, "conta não encontrada (só contas \"só download\")")
        con.execute("UPDATE users SET remote_agent=? WHERE id=?", (1 if on else 0, uid))
    return {"ok": True}


@app.post("/t/{token}/agent")
async def token_agent_report(token: str, request: Request):
    """O gravador da conta do cliente avisa o resultado do agente (instalado/erro/recusado)."""
    import time as _t
    th = core.token_hash(token)
    with core.db() as con:
        r = con.execute("SELECT created_by, expires_at FROM dl_tokens WHERE token_hash=?", (th,)).fetchone()
    if not r or r["expires_at"] <= core.now():
        raise HTTPException(410, "link expirado ou inválido")
    if _t.time() - _agent_last.get("agent:" + th, 0) < 1:
        raise HTTPException(429, "devagar")
    _agent_last["agent:" + th] = _t.time()
    try:
        d = json.loads((await request.body())[:4096])
        assert isinstance(d, dict)
    except (ValueError, AssertionError):
        raise HTTPException(400, "JSON inválido")
    state = str(d.get("state") or "")
    if state not in ("ok", "erro", "recusado"):
        raise HTTPException(400, "estado inválido")
    clean = lambda v, n: panel.SAFE.sub("", str(v or ""))[:n]  # noqa: E731
    with core.db() as con:
        con.execute("UPDATE users SET agent_state=?, agent_msg=?, agent_host=?, agent_os=?, agent_at=? "
                    "WHERE id=? AND remote_agent=1",
                    (state, clean(d.get("message"), 200), clean(d.get("hostname"), 64), clean(d.get("os"), 80),
                     core.now(), r["created_by"]))
    return {"ok": True}


def create_partner(who: str, label: str) -> dict:
    """Sócio: conta permanente (sem validade), sem ISOs; acessa só as máquinas liberadas."""
    who = who.strip().lower()
    email = who if "@" in who else guest_email(who)
    if not core.EMAIL_RE.match(email) or email == core.ADMIN_EMAIL:
        raise core.Invalid("E-mail do sócio inválido.")
    password = core.generate_password()
    with core.db() as con:
        if con.execute("SELECT 1 FROM users WHERE email=?", (email,)).fetchone():
            raise core.Invalid(f"Já existe uma conta {email}.")
        con.execute("INSERT INTO users (email,label,pw_hash,created_at,expires_at,max_builds,mode,build_id) "
                    "VALUES (?,?,?,?,?,?,?,?)", (email, label, core.hash_password(password), core.now(),
                                                 core.PARTNER_UNTIL, 0, "partner", None))
    return {"email": email, "password": password, "expires_at": None, "url": PUBLIC_URL, "mode": "partner", "iso": None}


@app.get("/api/login-info")
def login_info():
    """Público: o login mostra só o prefixo e completa com este domínio."""
    return {"guest_domain": GUEST_DOMAIN}


@app.get("/api/admin/guest-domain")
def guest_domain(_: core.User = Depends(admin_user)):
    return {"domain": GUEST_DOMAIN}


@app.post("/api/admin/users")
async def create_user(request: Request, _: core.User = Depends(admin_user)):
    d = await body(request)
    label = str(d.get("label") or "").strip()[:60]
    try:
        max_builds = max(1, min(20, int(d.get("max_builds") or core.TEMP_MAX_BUILDS)))
    except (TypeError, ValueError):
        raise core.Invalid("Limite de ISOs inválido.")
    if d.get("mode") == "partner":
        return create_partner(str(d.get("prefix") or d.get("email") or ""), label)
    mode = "download" if d.get("mode") == "download" else "build"
    with core.db() as con:
        return create_temp_user(con, prefix=str(d.get("prefix") or d.get("email") or ""), label=label,
                                remote_agent=bool(d.get("remote_agent")),
                                duration=str(d.get("duration") or "24h"), mode=mode,
                                build_id=str(d.get("build_id") or "") or None, max_builds=max_builds)


@app.post("/api/admin/users/{uid}/revoke")
def revoke_user(uid: int, _: core.User = Depends(admin_user)):
    with core.db() as con:
        con.execute("UPDATE users SET revoked=1 WHERE id=?", (uid,))
        con.execute("DELETE FROM sessions WHERE user_id=?", (uid,))
        con.execute("DELETE FROM dl_tokens WHERE created_by=?", (uid,))  # gravadores já baixados param
    return {"ok": True}


@app.post("/api/admin/users/{uid}/reset")
def reset_user(uid: int, _: core.User = Depends(admin_user)):
    password = core.generate_password()
    with core.db() as con:
        r = con.execute("SELECT * FROM users WHERE id=?", (uid,)).fetchone()
        if not r:
            raise HTTPException(404, "conta não encontrada")
        con.execute("UPDATE users SET pw_hash=?, revoked=0 WHERE id=?", (core.hash_password(password), uid))
        con.execute("DELETE FROM sessions WHERE user_id=?", (uid,))
    linked = None
    if r["mode"] == "download" and r["build_id"]:
        with core.db() as con:
            b = con.execute("SELECT hostname FROM builds WHERE id=?", (r["build_id"],)).fetchone()
            linked = b["hostname"] if b else None
    return {"email": r["email"], "password": password, "expires_at": r["expires_at"], "url": PUBLIC_URL,
            "mode": r["mode"], "iso": linked}


@app.post("/api/admin/users/{uid}/extend")
async def extend_user(uid: int, request: Request, _: core.User = Depends(admin_user)):
    d = await body(request)
    dur = DURATIONS.get(str(d.get("duration") or "24h"))
    if not dur:
        raise core.Invalid("Validade inválida.")
    with core.db() as con:
        r = con.execute("SELECT * FROM users WHERE id=?", (uid,)).fetchone()
        if not r:
            raise HTTPException(404, "conta não encontrada")
        con.execute("UPDATE users SET expires_at=?, revoked=0 WHERE id=?", (core.now() + dur, uid))
        if r["mode"] == "download" and r["build_id"]:
            con.execute("UPDATE builds SET expires_at=MAX(COALESCE(expires_at,0), ?) WHERE id=?",
                        (core.now() + dur, r["build_id"]))
    return {"ok": True}


@app.post("/api/admin/users/{uid}/link")
async def relink_user(uid: int, request: Request, _: core.User = Depends(admin_user)):
    """Troca a ISO vinculada a um acesso "somente download"."""
    d = await body(request)
    with core.db() as con:
        r = con.execute("SELECT * FROM users WHERE id=?", (uid,)).fetchone()
        if not r or r["mode"] != "download":
            raise HTTPException(404, "acesso somente-download não encontrado")
        new_id = str(d.get("build_id") or "")
        _link_build(con, new_id, r["expires_at"])
        con.execute("UPDATE users SET build_id=? WHERE id=?", (new_id, uid))
        # gravadores que a pessoa baixou para a ISO antiga deixam de funcionar
        con.execute("DELETE FROM dl_tokens WHERE created_by=? AND build_id!=?", (uid, new_id))
    return {"ok": True}


@app.post("/api/admin/users/{uid}/partner")
def make_partner(uid: int, _: core.User = Depends(admin_user)):
    """Transforma uma conta temporária (download ou gerar ISOs) em sócio: mesmo login e
    senha, sem validade, sem ISOs. Vale também para conta expirada/revogada (reativa).
    As ISOs que a conta gerou passam para o admin (os PCs delas continuam aparecendo).
    Devolve as máquinas instaladas com as ISOs dela, para sugerir "Ver" nas permissões."""
    with core.db() as con:
        r = con.execute("SELECT * FROM users WHERE id=?", (uid,)).fetchone()
        if not r:
            raise HTTPException(404, "conta não encontrada")
        if r["mode"] == "partner":
            raise core.Invalid("Esta conta já é de sócio.")
        builds = {x[0] for x in con.execute("SELECT id FROM builds WHERE user_id=?", (uid,))}
        builds |= {x[0] for x in con.execute("SELECT DISTINCT build_id FROM downloads WHERE email=?", (r["email"],))}
        if r["build_id"]:
            builds.add(r["build_id"])
        con.execute("UPDATE builds SET user_id=? WHERE user_id=?", (core.ADMIN_ID, uid))
        con.execute("DELETE FROM dl_tokens WHERE created_by=?", (uid,))  # links de download do gravador
        con.execute("UPDATE users SET mode='partner', expires_at=?, revoked=0, build_id=NULL, max_builds=0 WHERE id=?",
                    (core.PARTNER_UNTIL, uid))
        suggest = [x[0] for x in con.execute(
            f"SELECT id FROM machines WHERE archived_at IS NULL AND build_id IN ({','.join('?' * len(builds))})",
            tuple(builds))] if builds else []
    return {"ok": True, "email": r["email"], "suggest": suggest}


@app.delete("/api/admin/users/{uid}")
async def delete_user(uid: int, _: core.User = Depends(admin_user)):
    with core.db() as con:
        was_partner = con.execute("SELECT 1 FROM users WHERE id=? AND mode='partner'", (uid,)).fetchone()
    if was_partner and REMOTE_LOCAL and remoto.available():
        try:
            await remoto.delete_partner(uid)
        except Exception:  # noqa: BLE001
            pass
    with core.db() as con:
        con.execute("DELETE FROM partner_perms WHERE user_id=?", (uid,))
        # o registro das ISOs fica (passa para o admin): pendrive já gravado com elas
        # continua se reportando e o PC aparece em Máquinas mesmo instalado depois.
        # Só os arquivos das ISOs prontas são apagados do servidor.
        for r in con.execute("SELECT id, status FROM builds WHERE user_id=?", (uid,)).fetchall():
            if r["status"] not in ("queued", "running"):
                builder.delete_build_files(r["id"])
                con.execute("UPDATE builds SET status=CASE WHEN status='done' THEN 'deleted' ELSE status END, iso_path=NULL, "
                            "message=CASE WHEN status='done' THEN 'ISO excluída (a conta que gerou foi removida)' ELSE message END "
                            "WHERE id=?", (r["id"],))
        con.execute("UPDATE builds SET user_id=? WHERE user_id=?", (core.ADMIN_ID, uid))
        con.execute("DELETE FROM sessions WHERE user_id=?", (uid,))
        con.execute("DELETE FROM dl_tokens WHERE created_by=?", (uid,))
        con.execute("DELETE FROM users WHERE id=?", (uid,))
    return {"ok": True}


# ------------------------------------------------------------------ sócios (permissões por máquina)
def _partner(uid: int):
    with core.db() as con:
        r = con.execute("SELECT * FROM users WHERE id=? AND mode='partner'", (uid,)).fetchone()
    if not r:
        raise HTTPException(404, "sócio não encontrado")
    return r


def _machine_hosts() -> dict[str, str]:
    with core.db() as con:
        return {r["id"]: r["hostname"] for r in con.execute("SELECT id, hostname FROM machines")}


_partner_synced: dict[int, float] = {}


async def _sync_partner(uid: int) -> str:
    """Aplica as permissões do sócio no Remoto. Devolve aviso (vazio = ok)."""
    if not (REMOTE_LOCAL and remoto.available()):
        return ""
    r = _partner(uid)
    perms = core.partner_perms(uid)
    with core.db() as con:
        archived = {r[0] for r in con.execute("SELECT id FROM machines WHERE archived_at IS NOT NULL")}
    mp = {ref: (0 if ref in archived else p) for (k, ref), p in perms.items() if k == "machine"}
    np_ = {ref: p for (k, ref), p in perms.items() if k == "node"}
    try:
        await remoto.sync_partner(uid, r["label"] or r["email"], mp, np_, _machine_hosts())
    except Exception as exc:  # noqa: BLE001
        return f"Permissões salvas, mas o Remoto não respondeu ({type(exc).__name__}); tento de novo quando o sócio abrir o Remoto."
    import time as _t
    _partner_synced[uid] = _t.time()
    return ""


@app.get("/api/admin/users/{uid}/perms")
async def get_partner_perms(uid: int, _: core.User = Depends(admin_user)):
    _partner(uid)
    machines = [{"id": m["id"], "hostname": m["hostname"], "label": m["label"], "online": m["online"]} for m in _machines()]
    hosts = {m["hostname"].lower() for m in machines}
    nodes, note = [], ""
    if REMOTE_LOCAL and remoto.available():
        try:  # aparelhos que só existem no Remoto (PCs adicionados por convite)
            nodes = [{"id": d["id"], "name": d["name"], "os": d["os"], "online": d["online"]}
                     for d in await remoto.devices() if d["name"].lower() not in hosts]
        except Exception as exc:  # noqa: BLE001
            note = f"O Remoto não respondeu ({type(exc).__name__}): só as máquinas do painel aparecem."
    items = [{"kind": k, "ref": ref, "perms": p} for (k, ref), p in core.partner_perms(uid).items()]
    return {"items": items, "machines": machines, "nodes": nodes, "note": note,
            "bits": {"view": core.P_VIEW, "terminal": core.P_TERMINAL, "desktop": core.P_DESKTOP,
                     "files": core.P_FILES, "vpn": core.P_VPN, "config": core.P_CONFIG}}


@app.post("/api/admin/users/{uid}/perms")
async def save_partner_perms(uid: int, request: Request, _: core.User = Depends(admin_user)):
    _partner(uid)
    d = await body(request)
    items = d.get("items") if isinstance(d.get("items"), list) else []
    core.set_partner_perms(uid, items[:500])
    return {"ok": True, "warning": await _sync_partner(uid)}


def _need(u: core.User, mid: str, bit: int) -> None:
    if not core.machine_perms(u, mid) & bit:
        raise HTTPException(404 if bit == core.P_VIEW else 403, "sem permissão nesta máquina")


def _remoto_user(u: core.User) -> str:
    """Conta do MeshCentral de quem está logado (admin ou sócio)."""
    if u.is_admin:
        return "admin"
    if u.is_partner:
        return remoto.partner_user(u.id)
    raise HTTPException(403, "sem acesso ao Remoto")


# ------------------------------------------------------------------ ISO oficial (admin)
@app.get("/api/admin/base")
def base_info(_: core.User = Depends(admin_user)):
    out = {}
    for s in ("24.04", "26.04"):
        out[s] = {"cached": builder.cached_iso(s), "task": builder.base_status.get(s)}
    total, used, free = __import__("shutil").disk_usage(core.DATA_DIR)
    return {"series": out, "disk": {"total": total, "free": free}, "keep_base": builder.KEEP_BASE_ISO}


@app.post("/api/admin/base/{series}")
def base_download(series: str, _: core.User = Depends(admin_user)):
    if series not in ("24.04", "26.04"):
        raise HTTPException(400, "série inválida")
    builder.start_base_download(series)
    return {"ok": True}


@app.delete("/api/admin/base/{series}")
def base_delete(series: str, _: core.User = Depends(admin_user)):
    if series not in ("24.04", "26.04"):
        raise HTTPException(400, "série inválida")
    if builder.base_in_use(series):
        raise HTTPException(409, "Há uma geração ou download usando esta ISO; aguarde terminar.")
    return {"ok": True, "freed": builder.delete_base_iso(series)}


# ------------------------------------------------------------------ reserva de ISO do Windows
# A Microsoft às vezes recusa o download automático (anti-robô). O admin deixa uma ISO salva
# aqui (baixando no servidor ou enviando); o gravador usa como reserva quando a Microsoft nega.
_WIN_LANGS = {"pt-BR", "en-US", "es-ES"}
_WIN_EDITIONS = {"Pro", "Home", "Education", "Enterprise"}


@app.get("/api/admin/win-iso")
def win_iso_list(_: core.User = Depends(admin_user)):
    import shutil as _sh
    total, _u, free = _sh.disk_usage(core.DATA_DIR)
    return {"isos": winstore.listing(), "disk": {"total": total, "free": free},
            "langs": sorted(_WIN_LANGS), "editions": sorted(_WIN_EDITIONS)}


@app.post("/api/admin/win-iso/fetch")
async def win_iso_fetch(request: Request, _: core.User = Depends(admin_user)):
    d = await body(request)
    lang = str(d.get("lang") or "pt-BR")
    arch = "x64"
    edition = str(d.get("edition") or "Pro")
    if lang not in _WIN_LANGS or edition not in _WIN_EDITIONS:
        raise core.Invalid("Idioma ou edição inválidos.")
    winstore.fetch_from_microsoft(lang, arch, edition)
    return {"ok": True}


@app.post("/api/admin/win-iso/upload")
async def win_iso_upload(request: Request, _: core.User = Depends(admin_user)):
    """Envio da ISO pelo admin (quando o servidor não consegue baixar da Microsoft).
    O arquivo vem no corpo; idioma/edição nos cabeçalhos."""
    lang = request.headers.get("x-uab-lang", "pt-BR")
    edition = request.headers.get("x-uab-edition", "Pro")
    arch = "x64"
    if lang not in _WIN_LANGS or edition not in _WIN_EDITIONS:
        raise core.Invalid("Idioma ou edição inválidos.")
    os.makedirs(winstore.WIN_DIR, exist_ok=True)
    tmp = winstore.iso_path(lang, arch) + ".part"
    size = 0
    with open(tmp, "wb") as fh:
        async for chunk in request.stream():
            size += len(fh.write(chunk)) if chunk else 0
    if size < (1 << 30):
        os.remove(tmp)
        raise core.Invalid("Arquivo pequeno demais para ser uma ISO do Windows.")
    try:
        info = winstore.import_file(tmp, lang, arch, edition, source="upload")
    except core.Invalid:
        raise
    return {"ok": True, **info}


@app.delete("/api/admin/win-iso/{lang}/{edition}")
def win_iso_delete(lang: str, edition: str, _: core.User = Depends(admin_user)):
    winstore.delete(lang, "x64")
    return {"ok": True}


# ------------------------------------------------------------------ builds
def _is_win(r) -> bool:
    return (r["os"] or "ubuntu") == "windows"


def _file_name(r) -> str:
    """Nome do arquivo do build: a ISO do Ubuntu ou o autounattend.xml do Windows."""
    return builder.win_config_name(r["hostname"]) if _is_win(r) else f"{r['hostname']}-autoinstall.iso"


def _build_row(r, u: core.User) -> dict:
    return {"id": r["id"], "hostname": r["hostname"], "series": r["series"], "status": r["status"],
            "os": r["os"] or "ubuntu",
            "step": r["step"], "progress": r["progress"], "message": r["message"],
            "summary": json.loads(r["summary"] or "{}"), "iso_size": r["iso_size"], "sha256": r["sha256"],
            "created_at": r["created_at"], "finished_at": r["finished_at"], "expires_at": r["expires_at"],
            "owner": r["owner_email"] if u.is_admin else None,
            "iso_name": _file_name(r)}


def _get_build(bid: str, u: core.User):
    with core.db() as con:
        r = con.execute("SELECT * FROM builds WHERE id=?", (bid,)).fetchone()
    allowed = u.is_admin or (u.download_only and bid == u.build_id) or (not u.download_only and r and r["user_id"] == u.id)
    if not r or r["status"] == "deleted" or not allowed:
        raise HTTPException(404, "ISO não encontrada")
    return r


@app.get("/api/builds")
def list_builds(u: core.User = Depends(current_user)):
    if u.is_partner:
        return {"builds": [], "quota": None}
    with core.db() as con:
        if u.is_admin:
            rows = con.execute("SELECT * FROM builds ORDER BY created_at DESC LIMIT 100").fetchall()
        elif u.download_only:
            rows = con.execute("SELECT * FROM builds WHERE id=? AND status!='deleted'", (u.build_id or "",)).fetchall()
            return {"builds": [_build_row(r, u) for r in rows], "quota": None}
        else:
            rows = con.execute("SELECT * FROM builds WHERE user_id=? ORDER BY created_at DESC", (u.id,)).fetchall()
    visible = [r for r in rows if r["status"] != "deleted"]
    return {"builds": [_build_row(r, u) for r in visible],
            "quota": None if u.is_admin else {"used": len(rows), "max": u.max_builds}}


@app.post("/api/builds")
async def create_build(request: Request, u: core.User = Depends(current_user)):
    d = await body(request)
    if u.download_only:
        raise HTTPException(403, "Este acesso é somente para baixar a ISO já preparada.")
    if u.is_partner:
        raise HTTPException(403, "Contas de sócio não geram ISOs.")
    if not u.is_admin:
        with core.db() as con:
            n = con.execute("SELECT COUNT(*) FROM builds WHERE user_id=?", (u.id,)).fetchone()[0]
        if n >= u.max_builds:
            raise HTTPException(403, f"Limite de {u.max_builds} ISOs desta conta atingido.")
    with core.db() as con:
        busy = con.execute("SELECT COUNT(*) FROM builds WHERE status IN ('queued','running')").fetchone()[0]
    if busy >= 5:
        raise HTTPException(429, "Muitas ISOs na fila. Tente em alguns minutos.")
    data = core.validate_build(d)
    if data["config"].get("WG_ENABLED") == "yes" and not wg.enabled():
        raise core.Invalid("WireGuard escolhido, mas o hub não está configurado (defina HUB_ENDPOINT no Coolify).")
    if data["os"] == "windows":
        return _create_windows(d, data, u)
    bid = secrets.token_hex(8)
    cfg = data["config"]
    # token com que a máquina instalada se reporta ao painel (só o hash no banco)
    data["report_token"] = secrets.token_urlsafe(32)
    deliver = d.get("deliver") if isinstance(d.get("deliver"), dict) else None
    if deliver and not u.is_admin:
        raise HTTPException(403, "Só o administrador pode entregar uma ISO para outra conta.")
    delivery = None
    with core.db() as con:  # uma transação: se a conta falhar, a ISO não é criada
        con.execute("INSERT INTO builds (id,user_id,owner_email,hostname,series,status,message,summary,created_at,"
                    "report_token_hash,panel_access) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                    (bid, u.id, u.email, cfg["HOSTNAME_"], cfg["UBUNTU_SERIES"], "queued", "Na fila",
                     builder.summary_of(cfg), core.now(), core.token_hash(data["report_token"]),
                     1 if data["panel_access"] else 0))
        if deliver:
            delivery = create_temp_user(con, prefix=str(deliver.get("prefix") or ""),
                                        label=str(deliver.get("label") or cfg["HOSTNAME_"]).strip()[:60],
                                        duration=str(deliver.get("duration") or "7d"), mode="download",
                                        build_id=bid, max_builds=0, allow_pending=True,
                                        remote_agent=bool(deliver.get("remote_agent")))
    try:
        builder.prepare_job(bid, data)
    except Exception as exc:  # noqa: BLE001
        builder.delete_build_files(bid)
        builder.update(bid, status="error", message=str(exc))
        raise HTTPException(500, "Falha ao preparar a ISO.")
    finally:
        data["password"] = data["ts_key"] = data["report_token"] = data["coolify_password"] = ""
        data["ddns_password"] = data["cloudflare_token"] = ""
    builder.enqueue(bid)
    return {"id": bid, "delivery": delivery}


def _create_windows(d: dict, data: dict, u: core.User) -> dict:
    """Build Windows: gera o autounattend.xml na hora (sem fila). Mesmo esquema de
    relatório do Ubuntu: token novo, só o hash no banco."""
    cfg = data["config"]
    if cfg["REMOTE_AGENT"] == "yes" and not remoto.mesh_id():
        raise core.Invalid("O Remoto ainda não está pronto (grupo de servidores não criado): desligue "
                           "“Aparecer no Remoto” ou tente mais tarde.")
    bid = secrets.token_hex(8)
    data["report_token"] = secrets.token_urlsafe(32)
    deliver = d.get("deliver") if isinstance(d.get("deliver"), dict) else None
    if deliver and not u.is_admin:
        raise HTTPException(403, "Só o administrador pode entregar uma instalação para outra conta.")
    delivery = None
    with core.db() as con:  # uma transação: se a conta falhar, o build não é criado
        con.execute("INSERT INTO builds (id,user_id,owner_email,hostname,series,status,message,summary,created_at,"
                    "report_token_hash,panel_access,os) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
                    (bid, u.id, u.email, cfg["HOSTNAME_"], cfg["WIN_VERSION"], "queued", "Gerando a configuração",
                     builder.summary_of(cfg, {"WIN_ACTIVATION": "key" if data["win_key_set"] else "generic"}),
                     core.now(), core.token_hash(data["report_token"]), 1 if data["panel_access"] else 0, "windows"))
        if deliver:
            delivery = create_temp_user(con, prefix=str(deliver.get("prefix") or ""),
                                        label=str(deliver.get("label") or cfg["HOSTNAME_"]).strip()[:60],
                                        duration=str(deliver.get("duration") or "7d"), mode="download",
                                        build_id=bid, max_builds=0, allow_pending=True,
                                        remote_agent=bool(deliver.get("remote_agent")))
    try:
        builder.make_windows(bid, data)
    except Exception as exc:  # noqa: BLE001
        # RuntimeError do builder tem texto próprio (sem segredos); o resto, só o tipo
        why = str(exc) if isinstance(exc, RuntimeError) else type(exc).__name__
        builder.delete_build_files(bid)
        builder.update(bid, status="error", message=f"Falha ao gerar a configuração do Windows: {why}."[:500],
                       finished_at=core.now())
        raise HTTPException(500, f"Falha ao gerar a configuração do Windows: {why}.")
    finally:
        data["secrets"] = {}
        data["report_token"] = ""
    return {"id": bid, "delivery": delivery}


@app.get("/api/builds/{bid}")
def get_build(bid: str, u: core.User = Depends(current_user)):
    return {"build": _build_row(_get_build(bid, u), u)}


@app.get("/api/builds/{bid}/log")
def build_log(bid: str, u: core.User = Depends(current_user)):
    _get_build(bid, u)
    p = os.path.join(builder.JOBS_DIR, bid, "build.log")
    if not os.path.exists(p):
        return PlainTextResponse("")
    return PlainTextResponse("".join(open(p, errors="replace").readlines()[-200:]))


@app.delete("/api/builds/{bid}")
def delete_build(bid: str, u: core.User = Depends(current_user)):
    r = _get_build(bid, u)
    if u.download_only:
        raise HTTPException(403, "Este acesso não pode excluir a ISO.")
    if r["status"] in ("queued", "running"):
        raise HTTPException(409, "Aguarde o fim da geração.")
    builder.delete_build_files(bid)
    with core.db() as con:
        con.execute("DELETE FROM dl_tokens WHERE build_id=?", (bid,))
        # o registro fica (oculto): servidores já instalados com esta ISO continuam
        # aparecendo em Máquinas, e o convidado continua com a ISO contada no limite
        con.execute("UPDATE builds SET status='deleted', iso_path=NULL, message='ISO excluída do servidor' WHERE id=?", (bid,))
    return {"ok": True}


@app.get("/download/iso/{bid}")
def download_iso(bid: str, u: core.User = Depends(current_user)):
    r = _get_build(bid, u)
    if _is_win(r):
        raise HTTPException(404, "Build do Windows: o painel não guarda a ISO (baixe o gravador ou o autounattend.xml)")
    if r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
        raise HTTPException(404, "ISO indisponível (expirou ou ainda não está pronta)")
    _record_download(bid, u.email, "iso")
    return FileResponse(r["iso_path"], media_type="application/octet-stream",
                        filename=f"{r['hostname']}-autoinstall.iso")


@app.get("/download/config/{bid}")
def download_config(bid: str, u: core.User = Depends(current_user)):
    """autounattend.xml do build Windows (avançado: Ventoy, Rufus)."""
    r = _get_build(bid, u)
    if not _is_win(r):
        raise HTTPException(404, "só builds do Windows têm autounattend.xml")
    if r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
        raise HTTPException(404, "Configuração indisponível (expirou ou ainda não está pronta)")
    _record_download(bid, u.email, "config")
    return FileResponse(r["iso_path"], media_type="application/xml", filename=_file_name(r),
                        headers={"Cache-Control": "no-store"})


def _record_download(bid: str, email: str, kind: str) -> None:
    with core.db() as con:
        con.execute("INSERT INTO downloads VALUES (?,?,?,?)", (bid, email, kind, core.now()))


@app.get("/download/sha256/{bid}")
def download_sha(bid: str, u: core.User = Depends(current_user)):
    r = _get_build(bid, u)
    if not r["sha256"]:
        raise HTTPException(404, "indisponível")
    name = _file_name(r)
    return PlainTextResponse(f"{r['sha256']}  {name}\n", headers={
        "Content-Disposition": f'attachment; filename="{name}.sha256"'})


HELPERS = {
    "windows": ("isoBuilder-Windows.exe", "application/vnd.microsoft.portable-executable"),
    "windows-arm64": ("isoBuilder-Windows-ARM64.exe", "application/vnd.microsoft.portable-executable"),
    "macos": ("isoBuilder-macOS.zip", "application/zip"),
    "linux": ("isobuilder-linux-amd64", "application/octet-stream"),
}


@app.get("/download/gravador/{platform}")
def download_helper(platform: str, _: core.User = Depends(current_user)):
    if platform not in HELPERS:
        raise HTTPException(404, "plataforma desconhecida")
    name, mt = HELPERS[platform]
    p = os.path.join(HELPER_DIR, name)
    if not os.path.exists(p):
        raise HTTPException(404, "gravador não compilado nesta instalação")
    return FileResponse(p, media_type=mt, filename=name)


# ------------------------------------------------------------------ formatar à distância
# "Formatar esta máquina": pelo agente do Remoto (roda como SYSTEM/root), o PC baixa o
# isoBuilder deste painel (hash conferido), fica esperando alguém conectar o pendrive
# gravado pelo isoBuilder, avisa aqui e reinicia por ele (entrada UEFI própria + BootNext).
FORMAT_WAIT_H = 24
# estados "em andamento": usb espera o pendrive; disk baixa o instalador e prepara o kexec
FORMAT_LIVE = ("enviado", "aguardando", "encontrado", "baixando", "preparando")
FORMAT_FROM_PC = {"aguardando", "encontrado", "baixando", "preparando", "reiniciando", "erro", "bios",
                  "expirou", "cancelado"}
_sha_cache: dict[str, tuple[float, str]] = {}


def _iso_boot_files(iso_path: str) -> tuple[str, str]:
    """Extrai (uma vez, ao lado da ISO) o kernel e o initrd do /casper/ da ISO, com xorriso
    (sem montar). Devolve os caminhos locais. O kexec no PC usa exatamente estes."""
    import subprocess
    kern, init = iso_path + ".kernel", iso_path + ".initrd"
    if os.path.exists(kern) and os.path.exists(init) and \
       os.path.getmtime(kern) >= os.path.getmtime(iso_path) and os.path.getmtime(init) >= os.path.getmtime(iso_path):
        return kern, init
    for src, dst in (("/casper/vmlinuz", kern), ("/casper/initrd", init)):
        tmp = dst + ".part"
        r = subprocess.run(["xorriso", "-osirrox", "on", "-indev", iso_path, "-extract", src, tmp],
                           capture_output=True, text=True)
        if r.returncode != 0 or not os.path.exists(tmp) or os.path.getsize(tmp) < 1000:
            raise HTTPException(500, f"não consegui extrair {src} da ISO (ISO não suportada para 'sem pendrive')")
        os.replace(tmp, dst)
    return kern, init


def _file_sha256(path: str) -> str:
    import hashlib
    mt = os.path.getmtime(path)
    hit = _sha_cache.get(path)
    if hit and hit[0] == mt:
        return hit[1]
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    _sha_cache[path] = (mt, h.hexdigest())
    return h.hexdigest()


def _format_platform(osdesc: str) -> str:
    o = osdesc.lower()
    if "windows" in o:
        return "windows"
    if "mac" in o or "darwin" in o:
        return "macos"
    return "linux"


def _format_tools(platform: str) -> dict[str, str]:
    """Arquitetura -> nome do isoBuilder usado pelo PC (só os que existem compilados)."""
    names = ({"x64": HELPERS["windows"][0], "arm64": HELPERS["windows-arm64"][0]} if platform == "windows"
             else {"x64": HELPERS["linux"][0]})
    return {a: n for a, n in names.items() if os.path.exists(os.path.join(HELPER_DIR, n))}


def _format_script(platform: str, token: str, method: str = "usb", iso_path: str = "") -> tuple[int, str]:
    """Comandos que o agente roda no PC (2 = PowerShell, 3 = sh), sem nenhum segredo além
    do token deste pedido (que só serve para baixar o isoBuilder/ISO e avisar o andamento)."""
    base = f"{PUBLIC_URL}/f/{token}"
    rep = f"{base}/report"
    tools = _format_tools(platform)
    sums = {a: _file_sha256(os.path.join(HELPER_DIR, n)) for a, n in tools.items()}
    if method == "disk":
        # sem pendrive: kexec no kernel/initrd da ISO servida pelo painel (só Linux)
        kern, init = _iso_boot_files(iso_path)
        args = (f'--install-disk --url "{base}" --kernel-sha {_file_sha256(kern)} '
                f'--initrd-sha {_file_sha256(init)} --wait {FORMAT_WAIT_H}h --report "{rep}"')
    else:
        args = f'--boot-usb --wait {FORMAT_WAIT_H}h --report "{rep}"'
    if platform == "windows":
        hashes = "; ".join(f"{a}='{s}'" for a, s in sums.items())
        steps = [
            "$ErrorActionPreference='Stop'",
            "[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12",
            "$a=if($env:PROCESSOR_ARCHITECTURE -eq 'ARM64'){'arm64'}else{'x64'}",
            f"$h=@{{{hashes}}}",
            "if(-not $h[$a]){throw ('sem isoBuilder para '+$a)}",
            "$d=Join-Path $env:ProgramData 'isoBuilder'",
            "New-Item -ItemType Directory -Force -Path $d | Out-Null",
            "$f=Join-Path $d 'isoBuilder-format.exe'",
            "Stop-ScheduledTask -TaskName 'isoBuilderFormat' -ErrorAction SilentlyContinue",
            f"Invoke-WebRequest -UseBasicParsing -Uri ('{base}/tool?arch='+$a) -OutFile $f",
            "if((Get-FileHash -Algorithm SHA256 $f).Hash -ne $h[$a]){Remove-Item $f -Force; throw 'o arquivo baixado não confere'}",
            f"$act=New-ScheduledTaskAction -Execute $f -Argument '{args}'",
            "$pr=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest",
            f"$st=New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit (New-TimeSpan -Hours {FORMAT_WAIT_H + 1})",
            "Register-ScheduledTask -TaskName 'isoBuilderFormat' -Action $act -Principal $pr -Settings $st -Force | Out-Null",
            "Start-ScheduledTask -TaskName 'isoBuilderFormat'",
        ]
        # uma linha só: o PowerShell lê os comandos pela entrada padrão, linha a linha
        fail = (f"Invoke-RestMethod -Method Post -Uri '{rep}' -ContentType 'application/json; charset=utf-8' "
                "-Body ([Text.Encoding]::UTF8.GetBytes((@{state='erro';message=('não consegui preparar o PC: '+"
                "$_.Exception.Message)}|ConvertTo-Json)))")
        return 2, "try { " + "; ".join(steps) + " } catch { " + fail + " }\n"
    kexec_check = ('command -v kexec >/dev/null 2>&1 || fail "kexec não instalado (apt install kexec-tools) — '
                   'use a opção com pendrive"\n') if method == "disk" else ""
    sh = f"""R='{rep}'
fail() {{ curl -fsS -m 20 -X POST -H 'Content-Type: application/json' -d "{{\\"state\\":\\"erro\\",\\"message\\":\\"não consegui preparar o PC: $1\\"}}" "$R" >/dev/null 2>&1; exit 1; }}
[ "$(uname -m)" = x86_64 ] || fail "só PCs x86_64 (64 bits)"
command -v curl >/dev/null 2>&1 || fail "curl não instalado"
{kexec_check}d=/var/lib/isobuilder; mkdir -p "$d"; f="$d/isobuilder-format"
curl -fsSL -m 900 -o "$f.tmp" '{base}/tool?arch=x64' || fail "não consegui baixar o isoBuilder"
echo "{sums.get('x64', '')}  $f.tmp" | sha256sum -c --status || {{ rm -f "$f.tmp"; fail "o arquivo baixado não confere"; }}
mv -f "$f.tmp" "$f"; chmod 700 "$f"
systemctl stop isobuilder-format >/dev/null 2>&1; systemctl reset-failed isobuilder-format >/dev/null 2>&1
systemd-run --unit=isobuilder-format --collect "$f" {args} >/dev/null 2>&1 || {{ setsid "$f" {args} >/dev/null 2>&1 </dev/null & }}
"""
    return 3, sh


def _format_row(r) -> dict:
    state, msg = r["state"], r["message"]
    method = (r["method"] if "method" in r.keys() else "usb")
    t = core.now()
    if state in FORMAT_LIVE and t > r["expires_at"]:
        state = "expirou"
        msg = "O PC não ficou pronto a tempo." if method == "disk" else "Ninguém conectou o pendrive a tempo."
    elif state == "enviado" and t - r["updated_at"] > 180:
        msg = ("O PC ainda não respondeu. Ele pode estar sem internet, ou o antivírus bloqueou o isoBuilder. "
               "Abra o terminal do PC no Remoto para conferir.")
    return {"id": r["id"], "node": r["node"], "name": r["name"], "platform": r["platform"], "state": state,
            "message": msg, "stick": r["stick"], "method": method, "iso": r["stick"] if method == "disk" else "",
            "created_at": r["created_at"], "updated_at": r["updated_at"],
            "expires_at": r["expires_at"], "live": state in FORMAT_LIVE}


@app.get("/api/admin/format")
def format_list(_: core.User = Depends(admin_user)):
    with core.db() as con:
        rows = con.execute("SELECT * FROM format_jobs WHERE created_at>? ORDER BY created_at DESC LIMIT 50",
                           (core.now() - 3 * 86400,)).fetchall()
    return {"jobs": [_format_row(r) for r in rows], "available": REMOTE_LOCAL and remoto.available()}


@app.post("/api/admin/format")
async def format_start(request: Request, u: core.User = Depends(admin_user)):
    _remoto_ready()
    d = await body(request)
    node = str(d.get("node") or "")
    if not re.fullmatch(r"[A-Za-z0-9@$_\-]{20,120}", node):
        raise core.Invalid("Aparelho inválido.")
    try:
        dev = next((x for x in await remoto.devices() if x["id"] == node), None)
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(502, f"O Remoto não respondeu ({type(exc).__name__}).")
    if not dev:
        raise core.Invalid("Esse PC não está no Remoto.")
    if str(d.get("confirm") or "").strip().lower() != dev["name"].strip().lower():
        raise core.Invalid(f"Digite o nome do PC ({dev['name']}) para confirmar.")
    if not dev["online"]:
        raise core.Invalid("O PC está desligado ou sem internet: o agente do Remoto precisa estar conectado.")
    platform = _format_platform(dev["os"])
    if platform == "macos":
        raise core.Invalid("Mac não permite escolher o próximo boot à distância.")
    if not _format_tools(platform):
        raise core.Invalid("O isoBuilder deste sistema não está compilado neste painel.")
    method = "disk" if str(d.get("method") or "usb") == "disk" else "usb"
    iso_path, iso_label = "", ""
    if method == "disk":
        if platform != "linux":
            raise core.Invalid("Sem pendrive, por enquanto, só para Ubuntu/Linux. Para Windows, use a opção com pendrive.")
        r = _get_build(str(d.get("build_id") or ""), u)
        if _is_win(r) or r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
            raise core.Invalid("Escolha uma ISO de Ubuntu pronta para instalar.")
        iso_path = r["iso_path"]
        iso_label = f"ISO {r['hostname']} ({(r['iso_size'] or 0) / 1e9:.1f} GB)"
        _iso_boot_files(iso_path)  # extrai/valida kernel e initrd antes de disparar
    token = secrets.token_urlsafe(32)
    jid = secrets.token_hex(8)
    t = core.now()
    wait_msg = ("Preparando o PC (baixando o instalador)…" if method == "disk"
                else "Preparando o PC (baixando o isoBuilder)…")
    with core.db() as con:  # um pedido por PC: o novo substitui o anterior
        con.execute("UPDATE format_jobs SET cancel=1, state='cancelado', message='Substituído por um novo pedido.', "
                    "updated_at=? WHERE node=? AND state IN " + str(FORMAT_LIVE), (t, node))
        con.execute("INSERT INTO format_jobs (id,token_hash,node,name,platform,state,message,created_by,created_at,"
                    "updated_at,expires_at,method,build_id,stick) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (jid, core.token_hash(token), node, dev["name"][:80], platform, "enviado", wait_msg,
                     u.email, t, t, t + (FORMAT_WAIT_H + 1) * 3600, method,
                     r["id"] if method == "disk" else "", iso_label))
    ctype, cmds = _format_script(platform, token, method, iso_path)
    try:
        res = await remoto.run_commands(node, ctype, cmds)
    except Exception as exc:  # noqa: BLE001
        res = f"o Remoto não respondeu ({type(exc).__name__})"
    if res != "OK":
        with core.db() as con:
            con.execute("UPDATE format_jobs SET state='erro', message=?, updated_at=? WHERE id=?",
                        (f"O Remoto não conseguiu enviar o comando: {res}", core.now(), jid))
    with core.db() as con:
        return {"job": _format_row(con.execute("SELECT * FROM format_jobs WHERE id=?", (jid,)).fetchone())}


@app.post("/api/admin/format/{jid}/cancel")
def format_cancel(jid: str, _: core.User = Depends(admin_user)):
    with core.db() as con:
        n = con.execute("UPDATE format_jobs SET cancel=1, state='cancelado', message='Cancelado pelo painel.', "
                        "updated_at=? WHERE id=? AND state IN " + str(FORMAT_LIVE),
                        (core.now(), jid)).rowcount
    if not n:
        raise core.Invalid("Esse pedido já terminou.")
    return {"ok": True}


@app.post("/api/admin/format/{jid}/dismiss")
def format_dismiss(jid: str, _: core.User = Depends(admin_user)):
    """Remove um aviso de formatação já terminada (erro, BIOS antiga, concluída…)."""
    with core.db() as con:
        r = con.execute("SELECT state, expires_at FROM format_jobs WHERE id=?", (jid,)).fetchone()
        if r and (r["state"] not in FORMAT_LIVE or core.now() > r["expires_at"]):
            con.execute("DELETE FROM format_jobs WHERE id=?", (jid,))
        elif r:
            raise core.Invalid("Esse pedido ainda está em andamento; use Cancelar.")
    return {"ok": True}


@app.post("/api/admin/format/clear")
def format_clear(_: core.User = Depends(admin_user)):
    """Remove todos os avisos de formatações já terminadas."""
    with core.db() as con:
        con.execute("DELETE FROM format_jobs WHERE state NOT IN " + str(FORMAT_LIVE) + " OR expires_at<?", (core.now(),))
    return {"ok": True}


def _format_job(token: str):
    if not re.fullmatch(r"[A-Za-z0-9_-]{20,100}", token):
        raise HTTPException(404, "pedido inválido")
    with core.db() as con:
        r = con.execute("SELECT * FROM format_jobs WHERE token_hash=?", (core.token_hash(token),)).fetchone()
    if not r or core.now() > r["expires_at"]:
        raise HTTPException(410, "pedido expirado ou inválido")
    return r


@app.get("/f/{token}/tool")
def format_tool(token: str, arch: str = "x64"):
    """isoBuilder para o PC do pedido (só enquanto o pedido está valendo)."""
    r = _format_job(token)
    if r["cancel"] or r["state"] not in FORMAT_LIVE:
        raise HTTPException(410, "pedido encerrado")
    name = _format_tools(r["platform"]).get(arch)
    if not name:
        raise HTTPException(404, "sem isoBuilder para esse sistema")
    return FileResponse(os.path.join(HELPER_DIR, name), media_type="application/octet-stream", filename=name,
                        headers={"Cache-Control": "no-store"})


def _format_disk_build(token: str):
    """Build (ISO) do pedido 'sem pendrive', só enquanto ele está valendo."""
    r = _format_job(token)
    if r["cancel"] or r["state"] not in FORMAT_LIVE or (r["method"] if "method" in r.keys() else "usb") != "disk":
        raise HTTPException(410, "pedido encerrado")
    with core.db() as con:
        b = con.execute("SELECT * FROM builds WHERE id=?", (r["build_id"],)).fetchone()
    if not b or b["status"] != "done" or not b["iso_path"] or not os.path.exists(b["iso_path"]):
        raise HTTPException(410, "a ISO deste pedido não está mais disponível")
    return b


@app.get("/f/{token}/iso")
def format_iso(token: str):
    """A ISO que o PC baixa para a RAM (kexec com url=) e instala. Só no modo 'sem pendrive'."""
    b = _format_disk_build(token)
    return FileResponse(b["iso_path"], media_type="application/octet-stream",
                        filename=f"{b['hostname']}-autoinstall.iso", headers={"Cache-Control": "no-store"})


@app.get("/f/{token}/kernel")
def format_kernel(token: str):
    b = _format_disk_build(token)
    return FileResponse(_iso_boot_files(b["iso_path"])[0], media_type="application/octet-stream",
                        filename="vmlinuz", headers={"Cache-Control": "no-store"})


@app.get("/f/{token}/initrd")
def format_initrd(token: str):
    b = _format_disk_build(token)
    return FileResponse(_iso_boot_files(b["iso_path"])[1], media_type="application/octet-stream",
                        filename="initrd", headers={"Cache-Control": "no-store"})


@app.post("/f/{token}/report")
async def format_report(token: str, request: Request):
    """O isoBuilder no PC avisa o andamento; a resposta diz se o técnico cancelou."""
    r = _format_job(token)
    lk = "fmt:" + r["id"]
    if time.time() - _agent_last.get(lk, 0) < 1:
        raise HTTPException(429, "devagar")
    _agent_last[lk] = time.time()
    try:
        d = json.loads((await request.body())[:4096])
        assert isinstance(d, dict)
    except (ValueError, AssertionError):
        raise HTTPException(400, "JSON inválido")
    state = str(d.get("state") or "")
    if state not in FORMAT_FROM_PC:
        raise HTTPException(400, "estado inválido")
    if r["cancel"]:
        return {"cancel": True}
    clean = lambda v, n: panel.SAFE.sub("", str(v or ""))[:n]  # noqa: E731
    with core.db() as con:
        con.execute("UPDATE format_jobs SET state=?, message=?, stick=CASE WHEN ?<>'' THEN ? ELSE stick END, "
                    "updated_at=? WHERE id=? AND cancel=0",
                    (state, clean(d.get("message"), 400), clean(d.get("stick"), 160), clean(d.get("stick"), 160),
                     core.now(), r["id"]))
    return {"cancel": False}


BUNDLE_MAGIC = b"UABCFG01"
DL_TOKEN_DAYS = int(os.environ.get("DL_TOKEN_DAYS", "7"))


@app.get("/download/bundle/{bid}/{platform}")
def download_bundle(bid: str, platform: str, u: core.User = Depends(current_user)):
    """Gravador personalizado: já contém um link temporário para esta ISO e a
    baixa sozinho. Um único download para o usuário."""
    r = _get_build(bid, u)
    if r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
        raise HTTPException(404, ("Configuração" if _is_win(r) else "ISO") + " indisponível (expirou ou ainda não está pronta)")
    if platform not in HELPERS:
        raise HTTPException(404, "plataforma desconhecida")
    name, mt = HELPERS[platform]
    base = os.path.join(HELPER_DIR, name)
    if not os.path.exists(base):
        raise HTTPException(404, "gravador não compilado nesta instalação")
    exp = min(r["expires_at"] or core.now() + 86400, core.now() + DL_TOKEN_DAYS * 86400)
    token = core.create_dl_token(bid, exp, u.id)
    _record_download(bid, u.email, f"gravador-{platform}")
    if _is_win(r):  # o gravador baixa o Windows da Microsoft e aplica esta configuração
        summ = json.loads(r["summary"] or "{}")
        info = {"kind": "windows", "server": PUBLIC_URL, "token": token, "config_name": _file_name(r),
                "sha256": r["sha256"], "size": r["iso_size"], "hostname": r["hostname"], "expires_at": exp,
                "windows": {"version": summ.get("WIN_VERSION") or "11", "edition": summ.get("WIN_EDITION") or "Pro",
                            "lang": summ.get("WIN_LANG") or "pt-BR", "arch": "x64"}}
        if winstore.have(summ.get("WIN_LANG") or "pt-BR", "x64"):  # reserva no servidor (Microsoft recusou?)
            info["win_fallback"] = f"{PUBLIC_URL}/t/{token}/win-iso"
    else:
        info = {"kind": "ubuntu", "server": PUBLIC_URL, "token": token, "iso_name": _file_name(r),
                "sha256": r["sha256"], "size": r["iso_size"], "hostname": r["hostname"], "expires_at": exp}
    info.update(_bundle_agent(u))   # conta do cliente com "acesso remoto junto"
    cfg = json.dumps(info, separators=(",", ":")).encode()
    headers = {"Content-Disposition": f'attachment; filename="{name}"', "Cache-Control": "no-store"}
    if platform == "macos":
        buf = io.BytesIO()
        with zipfile.ZipFile(base) as zin, zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zout:
            for info in zin.infolist():
                zout.writestr(info, zin.read(info.filename))  # preserva permissões de execução
            ri = zipfile.ZipInfo("isoBuilder.app/Contents/Resources/uab-download.json")
            ri.external_attr = 0o100644 << 16
            ri.compress_type = zipfile.ZIP_DEFLATED
            zout.writestr(ri, cfg)
        return StreamingResponse(iter([buf.getvalue()]), media_type=mt,
                                 headers={**headers, "Content-Length": str(buf.tell())})
    overlay = cfg + struct.pack(">Q", len(cfg)) + BUNDLE_MAGIC

    def gen():
        with open(base, "rb") as fh:
            while chunk := fh.read(1 << 20):
                yield chunk
        yield overlay
    return StreamingResponse(gen(), media_type=mt,
                             headers={**headers, "Content-Length": str(os.path.getsize(base) + len(overlay))})


@app.api_route("/t/{token}/iso", methods=["GET", "HEAD"])
def token_iso(token: str):
    """Download da ISO pelo gravador (link temporário, com retomada via Range)."""
    bid = core.dl_token_build(token)
    if not bid:
        raise HTTPException(410, "link expirado ou inválido")
    with core.db() as con:
        r = con.execute("SELECT * FROM builds WHERE id=?", (bid,)).fetchone()
    if r and _is_win(r):
        raise HTTPException(404, "build do Windows: use /config (a ISO vem da Microsoft)")
    if not r or r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
        raise HTTPException(410, "ISO removida do servidor")
    return FileResponse(r["iso_path"], media_type="application/octet-stream",
                        filename=_file_name(r), headers={"Cache-Control": "no-store"})


@app.api_route("/t/{token}/config", methods=["GET", "HEAD"])
def token_config(token: str):
    """autounattend.xml do build Windows para o gravador (mesmo link temporário)."""
    bid = core.dl_token_build(token)
    if not bid:
        raise HTTPException(410, "link expirado ou inválido")
    with core.db() as con:
        r = con.execute("SELECT * FROM builds WHERE id=?", (bid,)).fetchone()
    if r and not _is_win(r):
        raise HTTPException(404, "só builds do Windows têm configuração")
    if not r or r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
        raise HTTPException(410, "configuração removida do servidor")
    return FileResponse(r["iso_path"], media_type="application/xml", filename=_file_name(r),
                        headers={"Cache-Control": "no-store"})


def _win_build_langarch(token: str):
    """Build do Windows do token + (lang, arch), ou (None, None, None)."""
    bid = core.dl_token_build(token)
    if not bid:
        raise HTTPException(410, "link expirado ou inválido")
    with core.db() as con:
        r = con.execute("SELECT * FROM builds WHERE id=?", (bid,)).fetchone()
    if not r or not _is_win(r):
        raise HTTPException(404, "não é um build do Windows")
    summ = json.loads(r["summary"] or "{}")
    return r, (summ.get("WIN_LANG") or "pt-BR"), "x64"


@app.api_route("/t/{token}/win-iso", methods=["GET", "HEAD"])
def token_win_iso(token: str):
    """Reserva: a ISO do Windows guardada no servidor, para o gravador quando a Microsoft
    recusa o download. Só existe se o admin tiver deixado uma salva para este idioma/arch."""
    _, lang, arch = _win_build_langarch(token)
    if not winstore.have(lang, arch):
        raise HTTPException(404, "sem ISO do Windows de reserva neste painel")
    return FileResponse(winstore.iso_path(lang, arch), media_type="application/octet-stream",
                        filename=f"Windows11_{lang}_{arch}.iso", headers={"Cache-Control": "no-store"})


@app.get("/t/{token}/win-iso-sha")
def token_win_iso_sha(token: str):
    _, lang, arch = _win_build_langarch(token)
    sha = winstore.sha_of(lang, arch)
    if not sha:
        raise HTTPException(404, "sem ISO de reserva")
    return PlainTextResponse(sha)


@app.get("/t/{token}/tool/{platform}")
def token_tool(token: str, platform: str):
    """isoBuilder (sem personalização) para outro sistema: o gravador põe os três na
    partição INSTALAR do pendrive. Mesmo link temporário da ISO."""
    if platform not in ("windows", "macos", "linux"):
        raise HTTPException(404, "sistema inválido")
    if not core.dl_token_build(token):
        raise HTTPException(410, "link expirado ou inválido")
    name, mt = HELPERS[platform]
    path = os.path.join(HELPER_DIR, name)
    if not os.path.exists(path):
        raise HTTPException(404, "gravador não compilado nesta instalação")
    return FileResponse(path, media_type=mt, filename=name, headers={"Cache-Control": "no-store"})


# ------------------------------------------------------------------ máquinas instaladas
AGENT_MAX_BODY = 64 * 1024
_agent_last: dict[str, float] = {}


@app.post("/api/agent/report")
async def agent_report(request: Request):
    """Recebe o "estou vivo" de um servidor instalado (token gravado na ISO)."""
    auth = request.headers.get("authorization", "")
    token = auth[7:].strip() if auth.lower().startswith("bearer ") else ""
    if not re.match(r"^[A-Za-z0-9_-]{32,64}$", token):
        raise HTTPException(401, "token inválido")
    th = core.token_hash(token)
    import time as _t
    shutdown = request.headers.get("x-uab-event", "") == "shutdown"
    lk = th + (":off" if shutdown else "")  # o aviso de desligamento não disputa o limite com o relatório
    if _t.time() - _agent_last.get(lk, 0) < 5:  # no máximo 1 relatório a cada 5 s por ISO
        raise HTTPException(429, "devagar")
    _agent_last[lk] = _t.time()
    raw = await request.body()
    if len(raw) > AGENT_MAX_BODY:
        raise HTTPException(413, "relatório grande demais")
    with core.db() as con:
        b = con.execute("SELECT id, summary FROM builds WHERE report_token_hash=?", (th,)).fetchone()
    if not b:
        raise HTTPException(401, "token inválido")
    try:
        data = json.loads(raw)
        assert isinstance(data, dict)
    except (ValueError, AssertionError):
        raise HTTPException(400, "JSON inválido")
    if shutdown:  # "estou desligando/reiniciando": marca offline na hora, sem mexer no relatório
        mid = str(data.get("machine_id") or "")
        if not re.match(r"^[0-9a-f]{32}$", mid):
            raise HTTPException(400, "machine_id inválido")
        reason = "reboot" if data.get("reason") == "reboot" else "poweroff"
        with core.db() as con:
            con.execute("UPDATE machines SET off_at=?, off_reason=? WHERE id=?",
                        (core.now(), reason, core.token_hash(f"{b['id']}:{mid}")[:16]))
        return {"ok": True}
    rep_ = panel.clean(data)
    mid = str(rep_.get("machine_id") or "")
    host = str(rep_.get("hostname") or "")
    if not re.match(r"^[0-9a-f]{32}$", mid) or not core.HOSTNAME_RE.match(host):
        raise HTTPException(400, "machine_id/hostname inválidos")
    key = core.token_hash(f"{b['id']}:{mid}")[:16]
    with core.db() as con:
        if con.execute("SELECT 1 FROM forgotten_machines WHERE id=?", (key,)).fetchone():
            raise HTTPException(410, "máquina esquecida no painel")
    hw = str(rep_.get("hw_id") or "")
    hw = hw if re.fullmatch(r"[0-9a-f]{64}", hw) else ""
    now_ = core.now()
    with core.db() as con:
        con.execute(
            "INSERT INTO machines (id,build_id,machine_id,hostname,first_seen,last_seen,report,source_ip,hw_id) "
            "VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET hostname=excluded.hostname, "
            "last_seen=excluded.last_seen, report=excluded.report, source_ip=excluded.source_ip, "
            "hw_id=excluded.hw_id, off_at=NULL, off_reason=''",
            (key, b["id"], mid, host, now_, now_, json.dumps(rep_), client_ip(request), hw))
        moved = _adopt_reinstalled(con, key, b["id"], hw) if hw else False
        replaced = _archive_replaced(con, key, b["id"], hw, host) if hw and not moved else []
        archived = bool(con.execute("SELECT archived_at FROM machines WHERE id=?", (key,)).fetchone()["archived_at"])
    if moved or replaced:
        wg.write_desired()
    for old_id in replaced:
        await _resync_partners_of(old_id)
    resp = {"ok": True, "interval": 600}
    if archived:  # arquivada: relatório aceito, mas o túnel fica pausado
        if json.loads(b["summary"] or "{}").get("WG_ENABLED") == "yes":
            resp["wg"] = {"pending": "máquina arquivada no painel: túnel pausado"}
        return resp
    wg_pub = str((data.get("wg") or {}).get("pubkey") or "") if isinstance(data.get("wg"), dict) else ""
    if wg_pub and json.loads(b["summary"] or "{}").get("WG_ENABLED") == "yes":
        if wg.enabled():
            _fix_lan_iface(key, rep_)
        resp["wg"] = wg.register_server(key, wg_pub) if wg.enabled() else {"pending": "hub não configurado no painel"}
    return resp


REINSTALL_QUIET = 900    # a entrada antiga precisa estar parada há 15 min (ou ter avisado que desligou)
REINSTALL_WINDOW = 7200  # só tenta enquanto a entrada nova tem até 2 h (evita juntar VMs clonadas depois)


def _adopt_reinstalled(con, key: str, build_id: str, hw: str) -> bool:
    """Mesmo PC reinstalado com a MESMA ISO (mesmo hardware, machine-id novo): a entrada
    nova herda nome, anotações, SSH, permissões de sócio e o túnel WireGuard (mesmo IP e
    acessos de dispositivos) da antiga, que é removida. Outra ISO ou outro PC nunca se misturam.
    Roda a cada relatório nas primeiras 2 h da entrada nova: se a antiga ainda não estava
    parada no 1º relatório, junta no seguinte. O que já foi editado na nova é mantido."""
    cur = con.execute("SELECT * FROM machines WHERE id=?", (key,)).fetchone()
    if not cur or cur["first_seen"] < core.now() - REINSTALL_WINDOW:
        return False
    old = con.execute(
        "SELECT * FROM machines WHERE build_id=? AND hw_id=? AND id!=? AND first_seen<=? "
        "AND (last_seen<? OR off_at IS NOT NULL) ORDER BY last_seen DESC LIMIT 1",
        (build_id, hw, key, cur["first_seen"], core.now() - REINSTALL_QUIET)).fetchone()
    if not old:
        return False
    con.execute("UPDATE machines SET label=?, notes=?, archived_at=COALESCE(archived_at, ?) WHERE id=?",
                (cur["label"] or old["label"], cur["notes"] or old["notes"], old["archived_at"], key))
    if not cur["ssh_host"] and cur["ssh_port"] == 22:
        con.execute("UPDATE machines SET ssh_host=?, ssh_port=? WHERE id=?", (old["ssh_host"], old["ssh_port"], key))
    # permissões de sócio: as da antiga passam para a nova (a nova vence se já tiver)
    con.execute("UPDATE OR IGNORE partner_perms SET ref=? WHERE kind='machine' AND ref=?", (key, old["id"]))
    con.execute("DELETE FROM partner_perms WHERE kind='machine' AND ref=?", (old["id"],))
    # túnel: fica o da antiga (mesmo IP e acessos de dispositivos), com a chave nova do servidor
    wold = con.execute("SELECT * FROM wg_machines WHERE machine_key=?", (old["id"],)).fetchone()
    if wold:
        wnew = con.execute("SELECT * FROM wg_machines WHERE machine_key=?", (key,)).fetchone()
        if wnew:
            con.execute("DELETE FROM wg_clients WHERE machine_key=?", (key,))
            con.execute("DELETE FROM wg_machines WHERE machine_key=?", (key,))
        con.execute("UPDATE wg_machines SET machine_key=?, server_pubkey=? WHERE machine_key=?",
                    (key, wnew["server_pubkey"] if wnew else wold["server_pubkey"], old["id"]))
        con.execute("UPDATE wg_clients SET machine_key=? WHERE machine_key=?", (key, old["id"]))
        # "acesso à rede local" só continua se a rede ainda existir no PC reinstalado
        # (outra rede ou outro nome de placa: desliga, senão o servidor recusaria o túnel)
        if wold["lan"] and not _lan_still_there(cur["report"], wold["lan_cidr"], wold["lan_if"]):
            con.execute("UPDATE wg_machines SET lan=0, lan_cidr='', lan_if='' WHERE machine_key=?", (key,))
    con.execute("DELETE FROM machines WHERE id=?", (old["id"],))
    return True


def _archive_replaced(con, key: str, build_id: str, hw: str, host: str) -> list[str]:
    """Mesmo PC reinstalado com OUTRA ISO: a entrada antiga (parada) é ARQUIVADA sozinha,
    sem juntar dados (outra ISO pode ter outro nome e outra finalidade). Arquivar é
    reversível. Só nas primeiras 2 h da entrada nova e contra entradas mais antigas."""
    cur = con.execute("SELECT first_seen FROM machines WHERE id=?", (key,)).fetchone()
    if not cur or cur["first_seen"] < core.now() - REINSTALL_WINDOW:
        return []
    olds = con.execute(
        "SELECT id, notes FROM machines WHERE hw_id=? AND build_id!=? AND id!=? AND archived_at IS NULL "
        "AND first_seen<=? AND (last_seen<? OR off_at IS NOT NULL)",
        (hw, build_id, key, cur["first_seen"], core.now() - REINSTALL_QUIET)).fetchall()
    note = f"Arquivada sozinha: o mesmo PC foi reinstalado com outra ISO ({host})."
    for o in olds:
        con.execute("UPDATE machines SET archived_at=?, notes=? WHERE id=?",
                    (core.now(), ((o["notes"] + "\n") if o["notes"] else "") + note, o["id"]))
    return [o["id"] for o in olds]


def _fix_lan_iface(key: str, rep: dict) -> None:
    """"Acesso à rede local" apontando para uma placa que não tem mais a rede (ex.: a
    ponte br0 da Virtualização pegou o IP da enp3s0): corrige para a placa certa, ou
    desliga a opção se a rede sumiu. Servidores antigos recusariam o túnel inteiro."""
    import ipaddress
    w = wg.machine_wg(key)
    if not w or not w["lan"] or not w["lan_cidr"]:
        return
    try:
        net = ipaddress.ip_network(w["lan_cidr"])
        holders = []
        for item in rep.get("local_ips") or []:
            ifn, _, addr = str(item).partition(" ")
            if ipaddress.ip_interface(addr).network == net and re.match(r"^[A-Za-z0-9._-]{1,15}$", ifn):
                holders.append(ifn)
    except ValueError:
        return
    if w["lan_if"] in holders:
        return
    with core.db() as con:
        if holders:
            con.execute("UPDATE wg_machines SET lan_if=?, updated_at=? WHERE machine_key=?", (holders[0], core.now(), key))
        else:
            con.execute("UPDATE wg_machines SET lan=0, lan_cidr='', lan_if='', updated_at=? WHERE machine_key=?",
                        (core.now(), key))
    wg.write_desired()


def _lan_still_there(report: str, cidr: str, iface: str) -> bool:
    import ipaddress
    try:
        net = ipaddress.ip_network(cidr)
        for item in json.loads(report or "{}").get("local_ips") or []:
            ifn, _, addr = str(item).partition(" ")
            if ifn == iface and ipaddress.ip_interface(addr).network == net:
                return True
    except ValueError:
        pass
    return False


def _vpn_reason(m: dict, hub: dict) -> tuple[str, str]:
    """Por que a VPN desta máquina não conecta (texto para o painel) e avisos do túnel.
    Compara com as outras máquinas: se elas conectam e esta não, o problema está na
    rede do cliente (UDP bloqueado); se nenhuma conecta, está do lado do hub/roteador."""
    w, rep = m["wg"], (m.get("report") or {}).get("wg") or {}
    rep = rep if isinstance(rep, dict) else {}
    win = m.get("platform") == "windows"
    note = str(rep.get("message") or "") if rep.get("state") in ("ativo", "conectando") else ""
    if w.get("connected"):
        return "", note
    port = wg.HUB_ENDPOINT.rsplit(":", 1)[-1]
    if m.get("archived_at"):
        return "Máquina arquivada: o túnel fica pausado até restaurar.", ""
    if not hub.get("online"):
        return ("O hub WireGuard do seu servidor está fora do ar (serviço wg-hub no Coolify): "
                "todas as VPNs ficam desconectadas até ele voltar."), ""
    if not m.get("online"):
        return "A máquina está desligada ou sem internet: o túnel volta sozinho quando ela ligar.", ""
    if rep.get("state") == "erro":
        return f"{'O PC' if win else 'O servidor'} recusou a configuração do túnel: {rep.get('message') or 'erro desconhecido'}.", ""
    if rep.get("state") == "aguardando":
        return f"O túnel ainda não foi configurado {'no PC' if win else 'no servidor'}: {rep.get('message') or 'aguardando o painel'}.", ""
    if time.time() - (m.get("first_seen") or 0) < 300:
        return "Conectando: a máquina acabou de aparecer; o túnel sobe em alguns minutos.", ""
    mine = w.get("last_handshake") or 0
    others = any(time.time() - (p.get("last_handshake") or 0) < wg.HANDSHAKE_OK
                 for p in (hub.get("peers") or {}).values() if (p.get("last_handshake") or 0) != mine)
    if others:
        return (f"A máquina está online (o relatório chega por HTTPS), mas o túnel não tem resposta do hub. "
                f"Como outras máquinas conectam normalmente, o mais provável é a rede do cliente bloquear UDP "
                f"de saída (porta {port}); acontece em redes de empresa e de hotel. Peça a liberação ou use "
                f"o Tailscale nessas {'instalações' if win else 'ISOs'} (ele contorna o bloqueio)."), note
    return (f"Nenhuma máquina consegue conectar ao hub agora: confira se a porta UDP {port} está liberada "
            f"no seu roteador e se {wg.HUB_ENDPOINT.rsplit(':', 1)[0]} (DDNS) aponta para o IP atual da sua casa."), note


def _machines(where: str = "", args: tuple = ()) -> list[dict]:
    with core.db() as con:
        rows = con.execute(f"SELECT * FROM machines {where} ORDER BY last_seen DESC", args).fetchall()
        builds = {r["id"]: dict(r) for r in con.execute(
            "SELECT id, hostname, panel_access, owner_email, summary, os FROM builds").fetchall()}
        linked: dict[str, list] = {}
        for r in con.execute("SELECT email, build_id, expires_at, revoked FROM users WHERE mode='download'"):
            linked.setdefault(r["build_id"], []).append(r["email"])
        dls: dict[str, list] = {}
        for r in con.execute("SELECT build_id, email, kind, at FROM downloads ORDER BY at"):
            dls.setdefault(r["build_id"], []).append({"email": r["email"], "kind": r["kind"], "at": r["at"]})
    hub = wg.hub_status() if wg.enabled() else {}
    out = []
    for r in rows:
        m = panel.machine_row(r, builds)
        b = builds.get(r["build_id"]) or {}
        guests = linked.get(r["build_id"], [])
        downloaders = [x["email"] for x in dls.get(r["build_id"], [])]
        # Quem é o "dono" da máquina: a conta que recebeu a ISO, senão quem baixou, senão quem gerou.
        m["account"] = (guests[:1] or [e for e in downloaders if e != core.ADMIN_EMAIL][:1]
                        or [b.get("owner_email", "")])[0]
        m["delivered_to"] = guests
        m["downloads"] = dls.get(r["build_id"], [])[-20:]
        m["owner_email"] = b.get("owner_email", "")
        summ = json.loads(b.get("summary") or "{}")
        m["apps"] = {"docker": summ.get("DOCKER_ENABLED") == "yes", "coolify": summ.get("COOLIFY_ENABLED") == "yes",
                     "cockpit": summ.get("COCKPIT_ENABLED") == "yes"}
        m["build_wg"] = summ.get("WG_ENABLED") == "yes"  # a ISO/configuração pediu o túnel WireGuard
        m["build_ts"] = summ.get("TAILSCALE_ENABLED") == "yes"
        m["wg"] = wg.machine_info(r["id"]) if wg.enabled() else None
        m["archived_at"] = r["archived_at"]
        m["wg_reason"], m["wg_note"] = _vpn_reason(m, hub) if m["wg"] else ("", "")
        out.append(m)
    return out


def _get_machine(mid: str):
    with core.db() as con:
        r = con.execute("SELECT * FROM machines WHERE id=?", (mid,)).fetchone()
    if not r:
        raise HTTPException(404, "máquina não encontrada")
    return r


def _for_user(ms: list[dict], u: core.User) -> list[dict]:
    if u.is_admin:
        for m in ms:  # arquivada: só ver e configurar (terminal, VPN e tela pausados)
            m["perms"] = core.P_VIEW | core.P_CONFIG if m.get("archived_at") else core.P_ALL
        return ms
    if not u.is_partner:
        raise HTTPException(403, "sem acesso")
    pp = core.partner_perms(u.id)
    out = []
    for m in ms:
        p = 0 if m.get("archived_at") else pp.get(("machine", m["id"]), 0)
        if p & core.P_VIEW:
            for k in ("downloads", "delivered_to", "owner_email"):  # dados de contas: só o admin
                m.pop(k, None)
            m["perms"] = p
            out.append(m)
    return out


@app.get("/api/machines")
def list_machines(u: core.User = Depends(current_user)):
    ms = _for_user(_machines(), u)
    tsec = terminalsec.agents_status()  # {} se não configurado/indisponível (cache de 30 s)
    for m in ms:
        m["terminalsec"] = tsec.get(m.get("hostname") or "")
    return {"machines": ms, "panel": panel.ts_status() if u.is_admin else None,
            "hub": wg.hub_status() if wg.enabled() else None}


@app.get("/api/machines/{mid}")
def get_machine(mid: str, u: core.User = Depends(current_user)):
    _get_machine(mid)
    _need(u, mid, core.P_VIEW)
    return {"machine": _for_user(_machines("WHERE id=?", (mid,)), u)[0], "panel": panel.ts_status()}


@app.post("/api/machines/{mid}")
async def update_machine(mid: str, request: Request, u: core.User = Depends(current_user)):
    _get_machine(mid)
    _need(u, mid, core.P_CONFIG)
    d = await body(request)
    label = str(d.get("label") or "").strip()[:60]
    notes = str(d.get("notes") or "")[:2000]
    host = str(d.get("ssh_host") or "").strip()
    if host and not re.match(r"^[A-Za-z0-9.:_-]{1,253}$", host):
        raise core.Invalid("Endereço SSH inválido.")
    try:
        port = int(d.get("ssh_port") or 22)
        assert 1 <= port <= 65535
    except (TypeError, ValueError, AssertionError):
        raise core.Invalid("Porta SSH inválida.")
    with core.db() as con:
        con.execute("UPDATE machines SET label=?, notes=?, ssh_host=?, ssh_port=? WHERE id=?",
                    (label, notes, host, port, mid))
    return {"ok": True}


async def _resync_partners_of(mid: str) -> None:
    """Sócios com permissão nesta máquina: reaplica no Remoto (arquivar/restaurar)."""
    with core.db() as con:
        uids = [r[0] for r in con.execute(
            "SELECT DISTINCT user_id FROM partner_perms WHERE kind='machine' AND ref=?", (mid,))]
    for uid in uids:
        await _sync_partner(uid)


@app.post("/api/machines/{mid}/archive")
async def archive_machine(mid: str, _: core.User = Depends(admin_user)):
    """Arquivar: sai da lista; se ligar, o relatório é aceito mas ela continua arquivada.
    Terminal, VPN (túnel pausado, sem apagar) e sócios ficam bloqueados até restaurar."""
    _get_machine(mid)
    with core.db() as con:
        con.execute("UPDATE machines SET archived_at=COALESCE(archived_at, ?) WHERE id=?", (core.now(), mid))
    wg.write_desired()
    await _resync_partners_of(mid)
    return {"ok": True}


@app.post("/api/machines/{mid}/restore")
async def restore_machine(mid: str, _: core.User = Depends(admin_user)):
    _get_machine(mid)
    with core.db() as con:
        con.execute("UPDATE machines SET archived_at=NULL WHERE id=?", (mid,))
    wg.write_desired()  # túnel volta com o mesmo IP e os mesmos acessos
    await _resync_partners_of(mid)
    return {"ok": True}


@app.delete("/api/machines/{mid}")
async def forget_machine(mid: str, _: core.User = Depends(admin_user)):
    """Esquecer de vez: apaga dados e túnel e recusa os relatórios desta instalação.
    (O mesmo PC reinstalado de propósito ganha outra identidade e aparece normalmente.)"""
    m = _get_machine(mid)
    with core.db() as con:
        con.execute("INSERT OR REPLACE INTO forgotten_machines (id, build_id, hostname, forgotten_at) VALUES (?,?,?,?)",
                    (mid, m["build_id"], m["hostname"], core.now()))
        uids = [r[0] for r in con.execute(
            "SELECT DISTINCT user_id FROM partner_perms WHERE kind='machine' AND ref=?", (mid,))]
        con.execute("DELETE FROM partner_perms WHERE kind='machine' AND ref=?", (mid,))
        con.execute("DELETE FROM machines WHERE id=?", (mid,))
    wg.forget_machine(mid)  # revoga o túnel e todos os acessos de dispositivos
    for uid in uids:
        await _sync_partner(uid)
    return {"ok": True}


# ------------------------------------------------------------------ VPN WireGuard por máquina
@app.get("/api/machines/{mid}/wg")
def machine_wg(mid: str, u: core.User = Depends(current_user)):
    _get_machine(mid)
    _need(u, mid, core.P_VPN)
    return {"enabled": wg.enabled(), "hub": wg.hub_status(), "endpoint": wg.HUB_ENDPOINT,
            "machine": wg.machine_info(mid), "clients": wg.list_clients(mid)}


@app.post("/api/machines/{mid}/wg/clients")
async def machine_wg_client(mid: str, request: Request, u: core.User = Depends(current_user)):
    _get_machine(mid)
    _need(u, mid, core.P_VPN)
    d = await body(request)
    name = re.sub(r"[^\w .@()-]", "", str(d.get("name") or "")).strip()[:40] or "Dispositivo"
    return wg.create_client(mid, name, u.email)


@app.delete("/api/machines/{mid}/wg/clients/{cid}")
def machine_wg_client_delete(mid: str, cid: int, u: core.User = Depends(current_user)):
    _need(u, mid, core.P_VPN)
    wg.delete_client(mid, cid)
    return {"ok": True}


@app.post("/api/machines/{mid}/wg/lan")
async def machine_wg_lan(mid: str, request: Request, u: core.User = Depends(current_user)):
    m = _get_machine(mid)
    _need(u, mid, core.P_VPN)
    d = await body(request)
    if d.get("enabled") and panel.platform_of(json.loads(m["report"] or "{}")) == "windows":
        raise core.Invalid("O acesso à rede local do cliente ainda não existe no Windows: o túnel dá acesso só ao próprio PC.")
    wg.set_lan(mid, bool(d.get("enabled")), str(d.get("cidr") or ""), str(d.get("iface") or ""))
    return {"ok": True, "machine": wg.machine_info(mid)}


@app.get("/api/panel")
def panel_info(_: core.User = Depends(admin_user)):
    return {"tailscale": panel.ts_status(), "public_key": panel.ensure_panel_key(),
            "key_from": panel.PANEL_KEY_FROM, "wg": {"enabled": wg.enabled(), "endpoint": wg.HUB_ENDPOINT,
                                                    "hub": wg.hub_status()}}


@app.websocket("/api/machines/{mid}/terminal")
async def machine_terminal(ws: WebSocket, mid: str):
    """Terminal SSH no navegador: WebSocket <-> SSH (chave do painel)."""
    origin = ws.headers.get("origin", "")
    host = ws.headers.get("x-forwarded-host") or ws.headers.get("host", "")
    u = core.session_user(ws.cookies.get(COOKIE))
    if not u or (origin and origin.split("://", 1)[-1] != host) or not (core.machine_perms(u, mid) & core.P_TERMINAL):
        await ws.close(code=4403)
        return
    await ws.accept()

    async def say(msg: str):
        await ws.send_text(json.dumps({"t": "s", "d": msg}))

    try:
        m = _get_machine(mid)
    except HTTPException:
        await say("Máquina não encontrada.")
        await ws.close()
        return
    user = json.loads(m["report"] or "{}").get("admin_user") or ""
    if not core.USER_RE.match(user):
        await say("A máquina não informou o usuário administrador.")
        await ws.close()
        return
    try:
        cols, rows = 100, 30
        first = json.loads(await asyncio.wait_for(ws.receive_text(), 10))
        if first.get("t") == "r":
            cols, rows = int(first.get("c", 100)), int(first.get("r", 30))
    except Exception:  # noqa: BLE001
        pass
    try:
        await say("Conectando…")
        conn, where = await panel.open_ssh(m, user)
    except Exception as exc:  # noqa: BLE001
        await say(f"Não foi possível conectar: {exc}")
        await ws.close()
        return
    await say(f"Conectado a {where} como {user}.")
    try:
        async with conn:
            proc = await conn.create_process(term_type="xterm-256color", term_size=(cols, rows), encoding=None)

            async def pump_out():
                while True:
                    data = await proc.stdout.read(65536)
                    if not data:
                        break
                    await ws.send_bytes(data)

            async def pump_in():
                while True:
                    msg = json.loads(await ws.receive_text())
                    if msg.get("t") == "i":
                        proc.stdin.write(str(msg.get("d", "")).encode())
                    elif msg.get("t") == "r":
                        proc.change_terminal_size(int(msg.get("c", 100)), int(msg.get("r", 30)))

            tasks = [asyncio.create_task(pump_out()), asyncio.create_task(pump_in())]
            done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
            for t in pending:
                t.cancel()
    except (WebSocketDisconnect, ConnectionError):
        pass
    except Exception as exc:  # noqa: BLE001
        try:
            await say(f"Conexão encerrada: {exc}")
        except Exception:  # noqa: BLE001
            pass
    finally:
        try:
            await say("Sessão encerrada.")
            await ws.close()
        except Exception:  # noqa: BLE001
            pass


@app.get("/health")
def health():
    return {"ok": True}


# ------------------------------------------------------------------ front-end (SPA)
app.mount("/static", StaticFiles(directory=STATIC_DIR), name="static")


def _asset_version(name: str) -> str:
    import hashlib
    with open(os.path.join(STATIC_DIR, name), "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()[:12]


# A página referencia app.js/style.css com ?v=<hash do conteúdo>: cada versão
# nova tem um endereço novo, então CDNs (Cloudflare) e navegadores nunca servem
# uma cópia antiga depois de uma atualização.
_INDEX_HTML = open(os.path.join(STATIC_DIR, "index.html"), encoding="utf-8").read() \
    .replace("/static/app.js", f"/static/app.js?v={_asset_version('app.js')}") \
    .replace("/static/pkgicons.js", f"/static/pkgicons.js?v={_asset_version('pkgicons.js')}") \
    .replace("/static/style.css", f"/static/style.css?v={_asset_version('style.css')}")
# página inicial pública (visitantes): animação 3D + como funciona; o script é externo (CSP)
_LANDING_HTML = open(os.path.join(STATIC_DIR, "landing", "index.html"), encoding="utf-8").read() \
    .replace("/static/landing/landing.js", f"/static/landing/landing.js?v={_asset_version('landing/landing.js')}")


# ------------------------------------------------------------------ PWA (instalar como app)
MANIFEST = {
    "name": "ISO Builder", "short_name": "ISO Builder", "id": "/", "lang": "pt-BR",
    "description": "Gera ISOs do Ubuntu Server com instalação automática e acompanha os servidores.",
    "start_url": "/", "scope": "/", "display": "standalone", "orientation": "any",
    "background_color": "#0f1115", "theme_color": "#0f1115",
    "icons": [
        {"src": "/static/pwa/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
        {"src": "/static/pwa/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
        {"src": "/static/pwa/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
    ],
}


@app.get("/manifest.webmanifest")
def manifest():
    return JSONResponse(MANIFEST, media_type="application/manifest+json", headers={"Cache-Control": "no-cache"})


@app.get("/sw.js")
def service_worker():
    # na raiz para valer para o site todo; sempre revalidado (atualiza o app)
    return FileResponse(os.path.join(STATIC_DIR, "sw.js"), media_type="text/javascript",
                        headers={"Cache-Control": "no-cache", "Service-Worker-Allowed": "/"})


@app.get("/login")
@app.get("/isos")
@app.get("/iso")
@app.get("/iso/{bid}")
@app.get("/maquinas")
@app.get("/acessos")
@app.get("/sistema")
@app.get("/acesso-remoto")  # /remoto é do MeshCentral (proxy)
def index(bid: str = ""):
    # endereços limpos: toda tela do painel responde a mesma página; o app.js
    # olha o caminho e mostra a tela certa (F5 e links diretos funcionam)
    from fastapi.responses import HTMLResponse
    return HTMLResponse(_INDEX_HTML, headers={"Cache-Control": "no-cache, no-store, must-revalidate"})


@app.get("/")
def home(request: Request):
    """Quem já está logado cai direto no painel; visitante vê a página inicial pública."""
    from fastapi.responses import HTMLResponse
    if core.session_user(request.cookies.get(COOKIE)):
        return HTMLResponse(_INDEX_HTML, headers={"Cache-Control": "no-cache, no-store, must-revalidate"})
    return HTMLResponse(_LANDING_HTML, headers={"Cache-Control": "no-cache"})


@app.get("/sobre")
def about():
    """Página inicial pública, também para quem está logado."""
    from fastapi.responses import HTMLResponse
    return HTMLResponse(_LANDING_HTML, headers={"Cache-Control": "no-cache"})
