"""API e páginas da interface web do ubuntu-autoinstall-builder."""
from __future__ import annotations

import asyncio
import io
import json
import os
import re
import secrets
import struct
import zipfile

from fastapi import Depends, FastAPI, HTTPException, Request, Response, WebSocket, WebSocketDisconnect
from fastapi.responses import FileResponse, JSONResponse, PlainTextResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles

from . import builder, core, panel, wg

STATIC_DIR = os.path.join(os.path.dirname(__file__), "..", "static")
HELPER_DIR = os.environ.get("HELPER_DIST", os.path.join(builder.UAB_ROOT, "helper", "dist"))
PUBLIC_URL = os.environ.get("PUBLIC_URL", "https://iso.gvtserver.online").rstrip("/")
# domínio dos e-mails gerados para convidados (vem de PUBLIC_URL)
# Domínio FIXO dos e-mails das contas temporárias (o admin escolhe só o prefixo).
GUEST_DOMAIN = (os.environ.get("GUEST_EMAIL_DOMAIN") or "gvtserver.online").strip().lower()
PREFIX_RE = re.compile(r"^[a-z0-9]([a-z0-9._-]{0,38}[a-z0-9])?$")
COOKIE = "uab_session"
MAX_BODY = 256 * 1024

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
    if request.method in ("POST", "PUT", "PATCH", "DELETE") and not request.url.path.startswith("/api/agent/"):
        if request.headers.get("x-requested-with") != "uab":
            return JSONResponse({"error": "requisição inválida"}, 403)
        origin = request.headers.get("origin")
        host = request.headers.get("x-forwarded-host") or request.headers.get("host")
        if origin and origin.split("://", 1)[-1] != host:
            return JSONResponse({"error": "origem inválida"}, 403)
        if int(request.headers.get("content-length") or 0) > MAX_BODY:
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
        "connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
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


@app.get("/api/me")
def me(u: core.User = Depends(current_user)):
    return {"user": u.public(), "admin_email": core.ADMIN_EMAIL if u.is_admin else None,
            "features": {"wireguard": wg.enabled()}}


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
            "build_status": linked["status"] if linked else None}


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
                     max_builds: int, allow_pending: bool = False) -> dict:
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
    con.execute("INSERT INTO users (email,label,pw_hash,created_at,expires_at,max_builds,mode,build_id) "
                "VALUES (?,?,?,?,?,?,?,?)",
                (email, label, core.hash_password(password), core.now(), until, max_builds, mode,
                 build_id if mode == "download" else None))
    return {"email": email, "password": password, "expires_at": until, "url": PUBLIC_URL,
            "mode": mode, "iso": hostname}


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
    mode = "download" if d.get("mode") == "download" else "build"
    with core.db() as con:
        return create_temp_user(con, prefix=str(d.get("prefix") or d.get("email") or ""), label=label,
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


@app.delete("/api/admin/users/{uid}")
def delete_user(uid: int, _: core.User = Depends(admin_user)):
    with core.db() as con:
        for r in con.execute("SELECT id FROM builds WHERE user_id=?", (uid,)).fetchall():
            builder.delete_build_files(r["id"])
        con.execute("DELETE FROM builds WHERE user_id=?", (uid,))
        con.execute("DELETE FROM sessions WHERE user_id=?", (uid,))
        con.execute("DELETE FROM dl_tokens WHERE created_by=?", (uid,))
        con.execute("DELETE FROM users WHERE id=?", (uid,))
    return {"ok": True}


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


# ------------------------------------------------------------------ builds
def _build_row(r, u: core.User) -> dict:
    return {"id": r["id"], "hostname": r["hostname"], "series": r["series"], "status": r["status"],
            "step": r["step"], "progress": r["progress"], "message": r["message"],
            "summary": json.loads(r["summary"] or "{}"), "iso_size": r["iso_size"], "sha256": r["sha256"],
            "created_at": r["created_at"], "finished_at": r["finished_at"], "expires_at": r["expires_at"],
            "owner": r["owner_email"] if u.is_admin else None,
            "iso_name": f"{r['hostname']}-autoinstall.iso"}


def _get_build(bid: str, u: core.User):
    with core.db() as con:
        r = con.execute("SELECT * FROM builds WHERE id=?", (bid,)).fetchone()
    allowed = u.is_admin or (u.download_only and bid == u.build_id) or (not u.download_only and r and r["user_id"] == u.id)
    if not r or r["status"] == "deleted" or not allowed:
        raise HTTPException(404, "ISO não encontrada")
    return r


@app.get("/api/builds")
def list_builds(u: core.User = Depends(current_user)):
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
                                        build_id=bid, max_builds=0, allow_pending=True)
    try:
        builder.prepare_job(bid, data)
    except Exception as exc:  # noqa: BLE001
        builder.delete_build_files(bid)
        builder.update(bid, status="error", message=str(exc))
        raise HTTPException(500, "Falha ao preparar a ISO.")
    finally:
        data["password"] = data["ts_key"] = data["report_token"] = data["coolify_password"] = ""
    builder.enqueue(bid)
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
        if u.is_admin:
            con.execute("DELETE FROM builds WHERE id=?", (bid,))
        else:  # continua contando no limite do convidado
            con.execute("UPDATE builds SET status='deleted', iso_path=NULL WHERE id=?", (bid,))
    return {"ok": True}


@app.get("/download/iso/{bid}")
def download_iso(bid: str, u: core.User = Depends(current_user)):
    r = _get_build(bid, u)
    if r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
        raise HTTPException(404, "ISO indisponível (expirou ou ainda não está pronta)")
    _record_download(bid, u.email, "iso")
    return FileResponse(r["iso_path"], media_type="application/octet-stream",
                        filename=f"{r['hostname']}-autoinstall.iso")


def _record_download(bid: str, email: str, kind: str) -> None:
    with core.db() as con:
        con.execute("INSERT INTO downloads VALUES (?,?,?,?)", (bid, email, kind, core.now()))


@app.get("/download/sha256/{bid}")
def download_sha(bid: str, u: core.User = Depends(current_user)):
    r = _get_build(bid, u)
    if not r["sha256"]:
        raise HTTPException(404, "indisponível")
    name = f"{r['hostname']}-autoinstall.iso"
    return PlainTextResponse(f"{r['sha256']}  {name}\n", headers={
        "Content-Disposition": f'attachment; filename="{name}.sha256"'})


HELPERS = {
    "windows": ("Gravador-Pendrive-Windows.exe", "application/vnd.microsoft.portable-executable"),
    "windows-arm64": ("Gravador-Pendrive-Windows-ARM64.exe", "application/vnd.microsoft.portable-executable"),
    "macos": ("Gravador-Pendrive-macOS.zip", "application/zip"),
    "linux": ("gravador-linux-amd64", "application/octet-stream"),
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


BUNDLE_MAGIC = b"UABCFG01"
DL_TOKEN_DAYS = int(os.environ.get("DL_TOKEN_DAYS", "7"))


@app.get("/download/bundle/{bid}/{platform}")
def download_bundle(bid: str, platform: str, u: core.User = Depends(current_user)):
    """Gravador personalizado: já contém um link temporário para esta ISO e a
    baixa sozinho. Um único download para o usuário."""
    r = _get_build(bid, u)
    if r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
        raise HTTPException(404, "ISO indisponível (expirou ou ainda não está pronta)")
    if platform not in HELPERS:
        raise HTTPException(404, "plataforma desconhecida")
    name, mt = HELPERS[platform]
    base = os.path.join(HELPER_DIR, name)
    if not os.path.exists(base):
        raise HTTPException(404, "gravador não compilado nesta instalação")
    exp = min(r["expires_at"] or core.now() + 86400, core.now() + DL_TOKEN_DAYS * 86400)
    token = core.create_dl_token(bid, exp, u.id)
    _record_download(bid, u.email, f"gravador-{platform}")
    cfg = json.dumps({"server": PUBLIC_URL, "token": token, "iso_name": f"{r['hostname']}-autoinstall.iso",
                      "sha256": r["sha256"], "size": r["iso_size"], "hostname": r["hostname"],
                      "expires_at": exp}, separators=(",", ":")).encode()
    headers = {"Content-Disposition": f'attachment; filename="{name}"', "Cache-Control": "no-store"}
    if platform == "macos":
        buf = io.BytesIO()
        with zipfile.ZipFile(base) as zin, zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zout:
            for info in zin.infolist():
                zout.writestr(info, zin.read(info.filename))  # preserva permissões de execução
            ri = zipfile.ZipInfo("Gravador de Pendrive.app/Contents/Resources/uab-download.json")
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
    if not r or r["status"] != "done" or not r["iso_path"] or not os.path.exists(r["iso_path"]):
        raise HTTPException(410, "ISO removida do servidor")
    return FileResponse(r["iso_path"], media_type="application/octet-stream",
                        filename=f"{r['hostname']}-autoinstall.iso", headers={"Cache-Control": "no-store"})


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
    if _t.time() - _agent_last.get(th, 0) < 5:  # no máximo 1 relatório a cada 5 s por ISO
        raise HTTPException(429, "devagar")
    _agent_last[th] = _t.time()
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
    rep_ = panel.clean(data)
    mid = str(rep_.get("machine_id") or "")
    host = str(rep_.get("hostname") or "")
    if not re.match(r"^[0-9a-f]{32}$", mid) or not core.HOSTNAME_RE.match(host):
        raise HTTPException(400, "machine_id/hostname inválidos")
    key = core.token_hash(f"{b['id']}:{mid}")[:16]
    now_ = core.now()
    with core.db() as con:
        con.execute(
            "INSERT INTO machines (id,build_id,machine_id,hostname,first_seen,last_seen,report,source_ip) "
            "VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET hostname=excluded.hostname, "
            "last_seen=excluded.last_seen, report=excluded.report, source_ip=excluded.source_ip",
            (key, b["id"], mid, host, now_, now_, json.dumps(rep_), client_ip(request)))
    resp = {"ok": True, "interval": 600}
    wg_pub = str((data.get("wg") or {}).get("pubkey") or "") if isinstance(data.get("wg"), dict) else ""
    if wg_pub and json.loads(b["summary"] or "{}").get("WG_ENABLED") == "yes":
        resp["wg"] = wg.register_server(key, wg_pub) if wg.enabled() else {"pending": "hub não configurado no painel"}
    return resp


def _machines(where: str = "", args: tuple = ()) -> list[dict]:
    with core.db() as con:
        rows = con.execute(f"SELECT * FROM machines {where} ORDER BY last_seen DESC", args).fetchall()
        builds = {r["id"]: dict(r) for r in con.execute(
            "SELECT id, hostname, panel_access, owner_email, summary FROM builds").fetchall()}
        linked: dict[str, list] = {}
        for r in con.execute("SELECT email, build_id, expires_at, revoked FROM users WHERE mode='download'"):
            linked.setdefault(r["build_id"], []).append(r["email"])
        dls: dict[str, list] = {}
        for r in con.execute("SELECT build_id, email, kind, at FROM downloads ORDER BY at"):
            dls.setdefault(r["build_id"], []).append({"email": r["email"], "kind": r["kind"], "at": r["at"]})
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
        m["wg"] = wg.machine_info(r["id"]) if wg.enabled() else None
        out.append(m)
    return out


def _get_machine(mid: str):
    with core.db() as con:
        r = con.execute("SELECT * FROM machines WHERE id=?", (mid,)).fetchone()
    if not r:
        raise HTTPException(404, "máquina não encontrada")
    return r


@app.get("/api/machines")
def list_machines(_: core.User = Depends(admin_user)):
    return {"machines": _machines(), "panel": panel.ts_status(), "hub": wg.hub_status() if wg.enabled() else None}


@app.get("/api/machines/{mid}")
def get_machine(mid: str, _: core.User = Depends(admin_user)):
    return {"machine": _machines("WHERE id=?", (mid,))[0] if _get_machine(mid) else None, "panel": panel.ts_status()}


@app.post("/api/machines/{mid}")
async def update_machine(mid: str, request: Request, _: core.User = Depends(admin_user)):
    _get_machine(mid)
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


@app.delete("/api/machines/{mid}")
def delete_machine(mid: str, _: core.User = Depends(admin_user)):
    with core.db() as con:
        con.execute("DELETE FROM machines WHERE id=?", (mid,))
    wg.forget_machine(mid)  # revoga o túnel e todos os acessos de dispositivos
    return {"ok": True}


# ------------------------------------------------------------------ VPN WireGuard por máquina
@app.get("/api/machines/{mid}/wg")
def machine_wg(mid: str, _: core.User = Depends(admin_user)):
    _get_machine(mid)
    return {"enabled": wg.enabled(), "hub": wg.hub_status(), "endpoint": wg.HUB_ENDPOINT,
            "machine": wg.machine_info(mid), "clients": wg.list_clients(mid)}


@app.post("/api/machines/{mid}/wg/clients")
async def machine_wg_client(mid: str, request: Request, u: core.User = Depends(admin_user)):
    _get_machine(mid)
    d = await body(request)
    name = re.sub(r"[^\w .@()-]", "", str(d.get("name") or "")).strip()[:40] or "Dispositivo"
    return wg.create_client(mid, name, u.email)


@app.delete("/api/machines/{mid}/wg/clients/{cid}")
def machine_wg_client_delete(mid: str, cid: int, _: core.User = Depends(admin_user)):
    wg.delete_client(mid, cid)
    return {"ok": True}


@app.post("/api/machines/{mid}/wg/lan")
async def machine_wg_lan(mid: str, request: Request, _: core.User = Depends(admin_user)):
    _get_machine(mid)
    d = await body(request)
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
    if not u or not u.is_admin or (origin and origin.split("://", 1)[-1] != host):
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
    .replace("/static/style.css", f"/static/style.css?v={_asset_version('style.css')}")


@app.get("/")
def index():
    from fastapi.responses import HTMLResponse
    return HTMLResponse(_INDEX_HTML, headers={"Cache-Control": "no-cache, no-store, must-revalidate"})
