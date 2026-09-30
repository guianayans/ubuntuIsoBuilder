"""Reserva de ISOs do Windows no servidor.

A Microsoft às vezes recusa o download automático (anti-robô) da conexão do cliente.
Quando isso acontece, o gravador baixa a ISO daqui (do painel), como faz com o Ubuntu.
O admin deixa uma ISO salva por idioma/arquitetura (enviando pelo painel ou baixando da
Microsoft no servidor). Guarda também o SHA256, para o gravador conferir.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import subprocess
import threading

from . import builder, core

WIN_DIR = os.path.join(builder.CACHE_DIR, "windows")
HELPER_DIR = os.environ.get("HELPER_DIST", os.path.join(builder.UAB_ROOT, "helper", "dist"))
_lock = threading.Lock()
fetch_status: dict[str, dict] = {}  # key -> {state, percent, message}


def _key(lang: str, arch: str) -> str:
    return re.sub(r"[^a-z0-9]", "", f"{lang}{arch}".lower())  # ex.: pt-BR + x64 -> ptbrx64


def iso_path(lang: str, arch: str) -> str:
    return os.path.join(WIN_DIR, _key(lang, arch) + ".iso")


def _meta_path(lang: str, arch: str) -> str:
    return os.path.join(WIN_DIR, _key(lang, arch) + ".json")


def sha_of(lang: str, arch: str) -> str:
    m = read_meta(lang, arch)
    return str(m.get("sha256") or "") if m else ""


def read_meta(lang: str, arch: str) -> dict | None:
    try:
        with open(_meta_path(lang, arch), encoding="utf-8") as fh:
            return json.load(fh)
    except OSError:
        return None


def have(lang: str, arch: str) -> bool:
    p = iso_path(lang, arch)
    return os.path.exists(p) and os.path.getsize(p) > (1 << 30)


def _sha256(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def save_meta(lang: str, arch: str, name: str, sha: str, source: str) -> None:
    os.makedirs(WIN_DIR, exist_ok=True)
    with open(_meta_path(lang, arch), "w", encoding="utf-8") as fh:
        json.dump({"lang": lang, "arch": arch, "name": name, "sha256": sha,
                   "size": os.path.getsize(iso_path(lang, arch)), "at": core.now(), "source": source}, fh)


def listing() -> list[dict]:
    out = []
    if not os.path.isdir(WIN_DIR):
        return out
    for n in sorted(os.listdir(WIN_DIR)):
        if not n.endswith(".json"):
            continue
        try:
            with open(os.path.join(WIN_DIR, n), encoding="utf-8") as fh:
                m = json.load(fh)
        except (OSError, ValueError):
            continue
        p = iso_path(m.get("lang", ""), m.get("arch", ""))
        m["present"] = os.path.exists(p)
        m["key"] = _key(m.get("lang", ""), m.get("arch", ""))
        st = fetch_status.get(m["key"])
        if st:
            m["fetch"] = st
        out.append(m)
    # em andamento sem meta ainda
    for k, st in fetch_status.items():
        if not any(o["key"] == k for o in out):
            out.append({"key": k, "present": False, "fetch": st})
    return out


def delete(lang: str, arch: str) -> None:
    for p in (iso_path(lang, arch), _meta_path(lang, arch), iso_path(lang, arch) + ".part"):
        try:
            os.remove(p)
        except OSError:
            pass


def inspect(src: str, lang: str, arch: str, edition: str) -> dict:
    """Confere que é Windows 11 do idioma/arquitetura/edição pedidos (via gravador).
    Levanta core.Invalid com o motivo se não bater."""
    helper = os.path.join(HELPER_DIR, "isobuilder-linux-amd64")
    if not os.path.exists(helper):
        raise core.Invalid("gravador Linux não compilado neste painel")
    r = subprocess.run([helper, "--inspect-windows", "--iso", src, "--win-lang", lang,
                        "--win-arch", arch, "--win-edition", edition], capture_output=True, text=True, timeout=600)
    if r.returncode != 0:
        raise core.Invalid("ISO recusada: " + (r.stderr.strip() or "não é Windows 11 " + edition + " " + lang))
    try:
        return json.loads(r.stdout.strip().splitlines()[-1])
    except (ValueError, IndexError):
        raise core.Invalid("não consegui ler a ISO do Windows")


def import_file(src: str, lang: str, arch: str, edition: str, source: str = "upload") -> dict:
    """Confere e guarda a ISO no cache."""
    info = inspect(src, lang, arch, edition)
    os.makedirs(WIN_DIR, exist_ok=True)
    dst = iso_path(lang, arch)
    os.replace(src, dst)
    sha = _sha256(dst)
    save_meta(lang, arch, info.get("name") or os.path.basename(dst), sha, source)
    return {"lang": lang, "arch": arch, "sha256": sha, "edition": info.get("edition"), "build": info.get("build")}


def fetch_from_microsoft(lang: str, arch: str, edition: str) -> None:
    """Baixa a ISO da Microsoft NO SERVIDOR (pode ser recusado se o IP estiver bloqueado).
    Roda em thread; o andamento fica em fetch_status[key]."""
    key = _key(lang, arch)
    with _lock:
        if fetch_status.get(key, {}).get("state") == "running":
            return
        fetch_status[key] = {"state": "running", "percent": 0, "message": "iniciando…"}

    def work():
        os.makedirs(WIN_DIR, exist_ok=True)
        helper = os.path.join(HELPER_DIR, "isobuilder-linux-amd64")
        tmp = iso_path(lang, arch) + ".part"
        try:
            if not os.path.exists(helper):
                raise RuntimeError("gravador Linux não compilado neste painel")
            proc = subprocess.Popen([helper, "--fetch-windows", "--win-lang", lang, "--win-arch", arch,
                                     "--win-edition", edition, "--out", tmp],
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            for line in proc.stdout:  # o helper imprime "PROGRESS <pct>" e mensagens
                line = line.strip()
                m = re.match(r"PROGRESS (\d+)", line)
                if m:
                    fetch_status[key] = {"state": "running", "percent": int(m.group(1)), "message": "baixando da Microsoft…"}
                elif line:
                    fetch_status[key]["message"] = line[:200]
            proc.wait()
            if proc.returncode != 0 or not os.path.exists(tmp):
                raise RuntimeError(fetch_status[key].get("message") or "download falhou")
            info = import_file(tmp, lang, arch, edition, source="microsoft")
            fetch_status[key] = {"state": "done", "percent": 100, "message": "ISO salva no servidor.", "sha256": info["sha256"]}
        except Exception as exc:  # noqa: BLE001
            try:
                os.remove(tmp)
            except OSError:
                pass
            fetch_status[key] = {"state": "error", "percent": 0,
                                 "message": "não foi possível baixar no servidor: " + str(exc)}

    threading.Thread(target=work, daemon=True).start()
