"""Integração com o TerminalSec (monitoramento de segurança).

Configuração: variável TERMINALSEC_CODE com o "código de conexão" gerado no TerminalSec
(Servidores → Integrações → Gerar código de conexão). O código traz a URL do painel e a
chave da integração. Com ele, cada ISO Ubuntu pode instalar o agente no primeiro boot:
o builder pede ao TerminalSec um token de ISO (reutilizável, com prazo e com aprovação
obrigatória no painel) e grava na ISO como segredo.

Se o TerminalSec roda no mesmo servidor (rede "coolify"), a conversa com a API vai pela
rede interna (http://terminalsec:8088), sem passar pela Cloudflare. Os servidores
instalados usam sempre a URL pública que o próprio TerminalSec informa.
"""

from __future__ import annotations

import base64
import json
import os
import time
import urllib.error
import urllib.request

INTERNAL_URL = os.environ.get("TERMINALSEC_INTERNAL_URL", "http://terminalsec:8088")
TOKEN_DAYS = int(os.environ.get("TERMINALSEC_TOKEN_DAYS", "30"))

_cache: dict = {"base": None, "status": None, "status_at": 0.0}


def _code() -> dict | None:
    raw = os.environ.get("TERMINALSEC_CODE", "").strip()
    if not raw.startswith("tsec1:"):
        return None
    try:
        b = raw[6:]
        data = json.loads(base64.urlsafe_b64decode(b + "=" * (-len(b) % 4)))
    except (ValueError, TypeError):
        return None
    if not str(data.get("url", "")).startswith(("https://", "http://")) or not data.get("key"):
        return None
    return data


def configured() -> bool:
    return _code() is not None


def origin() -> str:
    """Origem do painel (https://host) — liberada no frame-src para o overlay do header."""
    c = _code()
    return "/".join(c["url"].split("/")[:3]) if c else ""


def _call(base: str, path: str, body: dict | None = None, timeout: int = 10) -> dict:
    c = _code()
    req = urllib.request.Request(base.rstrip("/") + path, method="POST" if body is not None else "GET",
                                 data=json.dumps(body).encode() if body is not None else None)
    req.add_header("Authorization", "Bearer " + c["key"])
    req.add_header("Content-Type", "application/json")
    req.add_header("User-Agent", "iso-builder/terminalsec")
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read() or b"{}")


def _base() -> str:
    """Rede interna se o TerminalSec estiver no mesmo servidor; senão a URL do código."""
    if _cache["base"]:
        return _cache["base"]
    c = _code()
    for base in (INTERNAL_URL, c["url"]):
        try:
            if _call(base, "/api/integration/ping", timeout=5).get("ok"):
                _cache["base"] = base
                return base
        except (urllib.error.URLError, OSError, ValueError):
            continue
    raise RuntimeError("TerminalSec inacessível (confira o código de conexão e se o painel está no ar)")


def iso_token(label: str) -> dict:
    """{token, panel, expires}. Levanta RuntimeError com mensagem legível se não der."""
    if not configured():
        raise RuntimeError("TerminalSec não configurado (TERMINALSEC_CODE)")
    try:
        r = _call(_base(), "/api/integration/iso-token", {"label": label[:80], "days": TOKEN_DAYS})
    except urllib.error.HTTPError as e:
        _cache["base"] = None
        if e.code == 401:
            raise RuntimeError("o TerminalSec recusou a chave da integração (revogada?): gere um código novo") from None
        raise RuntimeError(f"o TerminalSec respondeu {e.code} ao pedir o token da ISO") from None
    except (urllib.error.URLError, OSError) as e:
        _cache["base"] = None
        raise RuntimeError(f"TerminalSec inacessível: {e}") from None
    if not str(r.get("token", "")).startswith("tsec_") or not str(r.get("panel", "")).startswith(("https://", "http://")):
        raise RuntimeError("resposta inesperada do TerminalSec")
    return r


def status() -> dict:
    """Situação da conexão para o atalho no header (cache de 30 s): {connected, url}."""
    c = _code()
    if not c:
        return {"configured": False}
    if _cache.get("conn") is not None and time.time() - _cache.get("conn_at", 0) < 30:
        return _cache["conn"]
    # o painel fica em /painel ("/" é a página pública de apresentação). O TerminalSec informa o
    # endereço no ping ("app"); sem essa informação vale o padrão /painel, nunca a página pública.
    url = _cache.get("app") or c["url"].rstrip("/") + "/painel"
    try:
        r = _call(_base(), "/api/integration/ping", timeout=5)
        ok = bool(r.get("ok"))
        if str(r.get("app", "")).startswith(("https://", "http://")):
            url = _cache["app"] = r["app"]
    except Exception:  # noqa: BLE001  (chave revogada, painel fora do ar, rede)
        _cache["base"] = None
        ok = False
    _cache["conn"], _cache["conn_at"] = {"configured": True, "connected": ok, "url": url}, time.time()
    return _cache["conn"]


def agents_status() -> dict[str, dict]:
    """hostname → {status, url} dos servidores vindos das ISOs (cache de 30 s; vazio se indisponível)."""
    if not configured():
        return {}
    if _cache["status"] is not None and time.time() - _cache["status_at"] < 30:
        return _cache["status"]
    try:
        rows = _call(_base(), "/api/integration/agents")
        st = {r["hostname"]: {"status": r["status"], "url": r["url"]} for r in rows if r.get("hostname")}
    except Exception:  # noqa: BLE001  (indicador é opcional: nunca derruba a lista de máquinas)
        st = {}
    _cache["status"], _cache["status_at"] = st, time.time()
    return st
