"""Fila de builds: baixa a ISO oficial (com progresso) e executa o build.sh
do projeto num diretório isolado por job. Segredos são apagados ao final."""
from __future__ import annotations

import json
import os
import queue
import re
import shutil
import subprocess
import threading
import time
import urllib.request

from . import core

UAB_ROOT = os.environ.get("UAB_ROOT", os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..")))
JOBS_DIR = os.path.join(core.DATA_DIR, "jobs")
CACHE_DIR = os.environ.get("UAB_CACHE_DIR", os.path.join(core.DATA_DIR, "cache"))
# false = apaga a ISO oficial do Ubuntu depois de cada geração (economiza ~3 GB por versão;
# a próxima geração baixa de novo)
KEEP_BASE_ISO = os.environ.get("KEEP_BASE_ISO", "true").strip().lower() not in ("0", "false", "no", "nao", "não")
ISO_TTL_HOURS = int(os.environ.get("ISO_TTL_HOURS", "48"))
MAX_STORED_ISOS = int(os.environ.get("MAX_STORED_ISOS", "4"))
RELEASES = "https://releases.ubuntu.com"
# Endereço que as máquinas instaladas usam para se reportar ao painel.
PANEL_URL = (os.environ.get("AGENT_URL") or os.environ.get("PUBLIC_URL", "https://iso.gvtserver.online")).rstrip("/")

_q: "queue.Queue[str]" = queue.Queue()
base_status: dict[str, dict] = {}  # série -> {state, percent, message, name, size}
_base_lock = threading.Lock()


def update(build_id: str, **kw) -> None:
    sets = ", ".join(f"{k}=?" for k in kw)
    with core.db() as con:
        con.execute(f"UPDATE builds SET {sets} WHERE id=?", (*kw.values(), build_id))


def shred_tree(path: str) -> None:
    if not os.path.exists(path):
        return
    for root, _, files in os.walk(path):
        for f in files:
            p = os.path.join(root, f)
            try:
                size = os.path.getsize(p)
                with open(p, "r+b") as fh:
                    fh.write(b"\0" * min(size, 1 << 20))
            except OSError:
                pass
    shutil.rmtree(path, ignore_errors=True)


# ------------------------------------------------------------------ ISO oficial
def latest_iso(series: str) -> tuple[str, str]:
    with urllib.request.urlopen(f"{RELEASES}/{series}/SHA256SUMS", timeout=30) as r:
        sums = r.read().decode()
    best = None
    for line in sums.splitlines():
        m = re.match(r"^([0-9a-f]{64}) \*?(ubuntu-" + re.escape(series) + r"(\.\d+)*-live-server-amd64\.iso)$", line.strip())
        if m:
            ver = [int(x) for x in re.findall(r"\d+", m.group(2).split("-")[1])]
            if best is None or ver > best[0]:
                best = (ver, m.group(2), m.group(1))
    if not best:
        raise RuntimeError(f"nenhuma ISO live-server para {series}")
    return best[1], best[2]


def cached_iso(series: str) -> dict | None:
    if not os.path.isdir(CACHE_DIR):
        return None
    names = sorted((n for n in os.listdir(CACHE_DIR)
                    if re.match(r"^ubuntu-" + re.escape(series) + r"(\.\d+)*-live-server-amd64\.iso$", n)),
                   key=lambda n: [int(x) for x in re.findall(r"\d+", n.split("-")[1])])
    if not names:
        return None
    p = os.path.join(CACHE_DIR, names[-1])
    return {"name": names[-1], "size": os.path.getsize(p), "mtime": int(os.path.getmtime(p))}


def base_in_use(series: str) -> bool:
    with core.db() as con:
        busy = con.execute("SELECT 1 FROM builds WHERE series=? AND status IN ('queued','running') LIMIT 1",
                           (series,)).fetchone()
    return bool(busy) or base_status.get(series, {}).get("state") in ("queued", "downloading", "verifying")


def delete_base_iso(series: str) -> int:
    """Apaga do servidor a ISO oficial da série (e downloads parciais). Retorna bytes liberados."""
    freed = 0
    if os.path.isdir(CACHE_DIR):
        pat = re.compile(r"^ubuntu-" + re.escape(series) + r"(\.\d+)*-live-server-amd64\.iso(\.part)?$")
        for n in os.listdir(CACHE_DIR):
            if pat.match(n):
                p = os.path.join(CACHE_DIR, n)
                freed += os.path.getsize(p)
                os.remove(p)
    with _base_lock:
        base_status.pop(series, None)
    return freed


def ensure_base_iso(series: str, on_progress=None) -> None:
    """Baixa a última ISO da série para o cache, com retomada e progresso.
    A verificação GPG + SHA256 é feita depois pelo download-ubuntu.sh."""
    os.makedirs(CACHE_DIR, exist_ok=True)
    name, _ = latest_iso(series)
    dest = os.path.join(CACHE_DIR, name)
    if os.path.exists(dest):
        return
    part = dest + ".part"
    url = f"{RELEASES}/{series}/{name}"
    have = os.path.getsize(part) if os.path.exists(part) else 0
    req = urllib.request.Request(url, headers={"Range": f"bytes={have}-"} if have else {})
    with urllib.request.urlopen(req, timeout=60) as r:
        if have and r.status != 206:
            have = 0
        total = have + int(r.headers.get("Content-Length", 0))
        mode = "ab" if have else "wb"
        with open(part, mode) as fh:
            done, last = have, 0.0
            while True:
                chunk = r.read(1 << 20)
                if not chunk:
                    break
                fh.write(chunk)
                done += len(chunk)
                if on_progress and time.time() - last > 1:
                    last = time.time()
                    on_progress(done, total, name)
    if total and os.path.getsize(part) != total:
        raise RuntimeError("download incompleto; tente novamente")
    os.replace(part, dest)


def verify_base_iso(series: str) -> str:
    env = dict(os.environ, UAB_CACHE_DIR=CACHE_DIR, UAB_STATE_DIR=os.path.join(core.DATA_DIR, "base-state"), NO_COLOR="1")
    r = subprocess.run([os.path.join(UAB_ROOT, "scripts", "download-ubuntu.sh"), series],
                       capture_output=True, text=True, env=env)
    if r.returncode != 0:
        raise RuntimeError((r.stderr or r.stdout).strip().splitlines()[-1] if (r.stderr or r.stdout).strip() else "falha na verificação")
    return r.stdout.strip().splitlines()[-1]


def base_iso_task(series: str) -> None:
    def prog(done, total, name):
        with _base_lock:
            base_status[series] = {"state": "downloading", "percent": done * 100 / total if total else 0,
                                   "message": f"Baixando {name}", "done": done, "total": total}
    try:
        with _base_lock:
            base_status[series] = {"state": "downloading", "percent": 0, "message": "Consultando releases.ubuntu.com"}
        ensure_base_iso(series, prog)
        with _base_lock:
            base_status[series] = {"state": "verifying", "percent": 100, "message": "Verificando assinatura GPG e SHA256"}
        verify_base_iso(series)
        with _base_lock:
            base_status[series] = {"state": "ready", "percent": 100, "message": "ISO oficial verificada"}
    except Exception as exc:  # noqa: BLE001
        with _base_lock:
            base_status[series] = {"state": "error", "percent": 0, "message": str(exc)}


def start_base_download(series: str) -> None:
    with _base_lock:
        if base_status.get(series, {}).get("state") in ("downloading", "verifying"):
            return
        base_status[series] = {"state": "queued", "percent": 0, "message": "Na fila"}
    threading.Thread(target=base_iso_task, args=(series,), daemon=True).start()


# ------------------------------------------------------------------ jobs
def q(v: str) -> str:
    return "'" + v.replace("'", "'\\''") + "'"


def prepare_job(build_id: str, data: dict) -> str:
    """Grava config e segredos do job (permissões restritas)."""
    jd = os.path.join(JOBS_DIR, build_id)
    for sub in ("config", "secrets", "output", "work"):
        os.makedirs(os.path.join(jd, sub), mode=0o700, exist_ok=True)
    os.chmod(jd, 0o700)
    cfg = data["config"]
    if cfg.get("REMOTE_AGENT") == "yes":
        cfg["REMOTE_AGENT_URL"] = PANEL_URL.rstrip("/") + "/remoto"
    tsec_token = None
    if cfg.get("TSEC_AGENT") == "yes":
        from . import terminalsec
        t = terminalsec.iso_token(f"{cfg.get('HOSTNAME_') or 'servidor'} · build {build_id[:8]}")
        cfg["TSEC_PANEL_URL"], tsec_token = t["panel"].rstrip("/"), t["token"]
    lines = ["# Gerado pela interface web (build %s). Sem segredos." % build_id]
    lines += [f"{k}={q(v)}" for k, v in cfg.items()]
    _write(os.path.join(jd, "config", "install.env"), "\n".join(lines) + "\n")
    _write(os.path.join(jd, "config", "authorized_keys"), "".join(k + "\n" for k in data["keys"]), 0o644)
    # hash SHA-512 da senha via stdin (nunca como argumento)
    r = subprocess.run(["openssl", "passwd", "-6", "-stdin"], input=data["password"] + "\n",
                       capture_output=True, text=True)
    h = r.stdout.strip()
    if r.returncode != 0 or not h.startswith("$6$"):
        raise RuntimeError("falha ao gerar o hash da senha")
    _write(os.path.join(jd, "secrets", "password.hash"), h + "\n")
    if data.get("ts_key"):
        _write(os.path.join(jd, "secrets", "tailscale-authkey"), data["ts_key"] + "\n")
    if data.get("coolify_password"):
        _write(os.path.join(jd, "secrets", "coolify-password"), data["coolify_password"] + "\n")
    if data.get("ddns_password"):
        _write(os.path.join(jd, "secrets", "ddns-password"), data["ddns_password"] + "\n")
    if cfg.get("REMOTE_AGENT") == "yes":
        from . import remoto
        mid = remoto.mesh_id()
        if not mid:
            raise RuntimeError("o Remoto ainda não está pronto (grupo de servidores não criado)")
        _write(os.path.join(jd, "secrets", "remote-meshid"), mid + "\n")
    if data.get("cloudflare_token"):
        _write(os.path.join(jd, "secrets", "cloudflare-token"), data["cloudflare_token"] + "\n")
    if tsec_token:
        _write(os.path.join(jd, "secrets", "tsec-token"), tsec_token + "\n")
    # relatório ao painel + (opcional) chave do painel restrita ao Tailscale
    _write(os.path.join(jd, "secrets", "panel-token"), data["report_token"] + "\n")
    if data.get("panel_access"):
        from . import panel, wg
        # a chave do painel também é aceita vinda do hub WireGuard (terminal pelo túnel)
        extra = [f"{wg.HUB_IP}/32"] if data["config"].get("WG_ENABLED") == "yes" else []
        _write(os.path.join(jd, "config", "panel_key"), panel.panel_key_line(extra) + "\n", 0o644)
    return jd


def _write(path: str, content: str, mode: int = 0o600) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    with os.fdopen(fd, "w") as fh:
        fh.write(content)
    os.chmod(path, mode)


STEP_RE = re.compile(r"^== (\d)/7 (.*) ==$")


def run_job(build_id: str) -> None:
    jd = os.path.join(JOBS_DIR, build_id)
    with core.db() as con:
        b = con.execute("SELECT * FROM builds WHERE id=?", (build_id,)).fetchone()
    if not b or b["status"] != "queued":
        return
    series = b["series"]
    log_path = os.path.join(jd, "build.log")
    try:
        update(build_id, status="running", step="ISO oficial", progress=2, message="Preparando a ISO oficial do Ubuntu")

        def prog(done, total, name):
            pct = done * 100 / total if total else 0
            update(build_id, progress=2 + pct * 0.5,
                   message=f"Baixando {name}: {pct:.0f}% ({done/1e9:.1f} de {total/1e9:.1f} GB)")
        ensure_base_iso(series, prog)

        env = dict(os.environ, UAB_STATE_DIR=jd, UAB_CACHE_DIR=CACHE_DIR, NO_COLOR="1",
                   UAB_PANEL_URL=PANEL_URL, UAB_PANEL_TOKEN_FILE=os.path.join(jd, "secrets", "panel-token"))
        if os.path.exists(os.path.join(jd, "config", "panel_key")):
            env["UAB_PANEL_KEY_FILE"] = os.path.join(jd, "config", "panel_key")
        with open(log_path, "w") as log:
            p = subprocess.Popen([os.path.join(UAB_ROOT, "build.sh"), "--forget-secrets"], cwd=UAB_ROOT, env=env,
                                 stdout=subprocess.PIPE, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL, text=True)
            assert p.stdout
            for line in p.stdout:
                log.write(line)
                log.flush()
                m = STEP_RE.match(line.strip())
                if m:
                    n = int(m.group(1))
                    update(build_id, step=m.group(2), progress=52 + (n - 1) * 7, message=m.group(2))
            rc = p.wait()
        if rc != 0:
            tail = [l.strip() for l in open(log_path) if l.strip()]
            errs = [l for l in tail if l.startswith(("[ERRO]", "[FALHA]"))]
            raise RuntimeError((errs or tail or ["falha no build"])[-1])

        out = os.path.join(jd, "output")
        iso = next((os.path.join(out, f) for f in os.listdir(out) if f.endswith("-autoinstall.iso")), None)
        if not iso:
            raise RuntimeError("ISO não encontrada após o build")
        sha = open(iso + ".sha256").read().split()[0]
        # ISO entregue a um acesso "somente download": fica até o fim do acesso
        exp = core.now() + ISO_TTL_HOURS * 3600
        with core.db() as con:
            r = con.execute("SELECT MAX(expires_at) FROM users WHERE mode='download' AND build_id=? AND revoked=0",
                            (build_id,)).fetchone()
        exp = max(exp, r[0] or 0)
        update(build_id, status="done", progress=100, step="Pronto", message="ISO pronta para download",
               iso_path=iso, iso_size=os.path.getsize(iso), sha256=sha, finished_at=core.now(), expires_at=exp)
    except Exception as exc:  # noqa: BLE001
        update(build_id, status="error", message=str(exc)[:500], finished_at=core.now())
    finally:
        # segredos e arquivos de trabalho nunca ficam no servidor
        shred_tree(os.path.join(jd, "secrets"))
        shutil.rmtree(os.path.join(jd, "work"), ignore_errors=True)
        enforce_storage_limits()
        if not KEEP_BASE_ISO and not base_in_use(series):
            delete_base_iso(series)


# ------------------------------------------------------------------ Windows (só a configuração)
def win_config_name(hostname: str) -> str:
    return f"{hostname}-autounattend.xml"


def make_windows(build_id: str, data: dict) -> None:
    """Gera o autounattend.xml do build Windows na hora (segundos, sem fila). A ISO do
    Windows NUNCA passa pelo painel: o gravador baixa da Microsoft e aplica este arquivo.
    O arquivo contém a senha e a chave: 0600, no diretório do job, apagado com a validade."""
    import hashlib
    wc = core.winconfig()
    if not wc:
        raise RuntimeError("gerador do Windows indisponível")
    cfg = data["config"]
    jd = os.path.join(JOBS_DIR, build_id)
    os.makedirs(os.path.join(jd, "output"), mode=0o700, exist_ok=True)
    os.chmod(jd, 0o700)
    ctx = {"build_id": build_id, "panel_url": PANEL_URL, "report_token": data["report_token"],
           "panel_pubkey": "", "remote_agent_url": "", "remote_meshid": ""}
    if cfg.get("PANEL_ACCESS") == "yes":
        from . import panel
        # sem from=: além do túnel (WireGuard/Tailscale), o painel pode entrar pelo endereço
        # definido em Máquinas → Configurações (o OpenSSH do Windows só aceita chave)
        ctx["panel_pubkey"] = panel.ensure_panel_key()
    if cfg.get("REMOTE_AGENT") == "yes":
        from . import remoto
        mid = remoto.mesh_id()
        if not mid:
            raise RuntimeError("o Remoto ainda não está pronto (grupo de servidores não criado)")
        ctx["remote_agent_url"], ctx["remote_meshid"] = PANEL_URL.rstrip("/") + "/remoto", mid
    update(build_id, status="running", step="Configuração", progress=50, message="Gerando a configuração do Windows")
    try:
        xml = wc.render_autounattend(dict(cfg), dict(data["secrets"]), ctx)
    except Exception as exc:  # noqa: BLE001  (a mensagem do gerador pode citar dados: só o tipo)
        raise RuntimeError(f"o gerador do Windows falhou ({type(exc).__name__})") from None
    if not isinstance(xml, str) or "<unattend" not in xml:
        raise RuntimeError("o gerador do Windows devolveu um arquivo inválido")
    raw = xml.encode("utf-8")
    path = os.path.join(jd, "output", win_config_name(cfg["HOSTNAME_"]))
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "wb") as fh:
        fh.write(raw)
    os.chmod(path, 0o600)
    # mesma validade das ISOs (entregue a um acesso "somente download": até o fim do acesso)
    exp = core.now() + ISO_TTL_HOURS * 3600
    with core.db() as con:
        r = con.execute("SELECT MAX(expires_at) FROM users WHERE mode='download' AND build_id=? AND revoked=0",
                        (build_id,)).fetchone()
    exp = max(exp, r[0] or 0)
    update(build_id, status="done", progress=100, step="Pronto", message="Configuração pronta para o gravador",
           iso_path=path, iso_size=len(raw), sha256=hashlib.sha256(raw).hexdigest(), finished_at=core.now(),
           expires_at=exp)


def worker() -> None:
    while True:
        bid = _q.get()
        try:
            run_job(bid)
        finally:
            _q.task_done()


def enqueue(build_id: str) -> None:
    _q.put(build_id)


def delete_build_files(build_id: str) -> None:
    shred_tree(os.path.join(JOBS_DIR, build_id))


def enforce_storage_limits() -> None:
    """Apaga ISOs expiradas e mantém no máximo MAX_STORED_ISOS no disco."""
    with core.db() as con:
        # ISOs vinculadas a um acesso "somente download" ativo nunca são removidas
        protected = {r["build_id"] for r in con.execute(
            "SELECT build_id FROM users WHERE mode='download' AND revoked=0 AND expires_at>? AND build_id IS NOT NULL",
            (core.now(),)).fetchall()}
        rows = con.execute("SELECT id, expires_at, os FROM builds WHERE status='done' ORDER BY finished_at DESC").fetchall()
        rows = [r for r in rows if r["id"] not in protected]
        isos = [r["id"] for r in rows if r["os"] != "windows"]  # configurações do Windows (KB) não contam no limite
        for r in rows:
            if (r["expires_at"] and r["expires_at"] <= core.now()) or \
                    (r["os"] != "windows" and isos.index(r["id"]) >= MAX_STORED_ISOS):
                delete_build_files(r["id"])
                what = "Configuração" if r["os"] == "windows" else "ISO"
                con.execute("UPDATE builds SET status='expired', message=?, iso_path=NULL WHERE id=?",
                            (f"{what} removida do servidor (expirou)", r["id"]))


def janitor() -> None:
    while True:
        try:
            enforce_storage_limits()
            core.cleanup_sessions()
        except Exception:  # noqa: BLE001
            pass
        time.sleep(600)


def start() -> None:
    os.makedirs(JOBS_DIR, mode=0o700, exist_ok=True)
    os.makedirs(os.path.join(core.DATA_DIR, "tmp"), mode=0o700, exist_ok=True)
    # jobs interrompidos por reinício do container
    with core.db() as con:
        for r in con.execute("SELECT id FROM builds WHERE status IN ('queued','running')").fetchall():
            con.execute("UPDATE builds SET status='error', message='Interrompido (o servidor reiniciou). Gere novamente.' WHERE id=?", (r["id"],))
            shred_tree(os.path.join(JOBS_DIR, r["id"], "secrets"))
    threading.Thread(target=worker, daemon=True).start()
    threading.Thread(target=janitor, daemon=True).start()


def summary_of(cfg: dict, extra: dict | None = None) -> str:
    if cfg.get("OS") == "windows":
        keep = ["OS", "WIN_VERSION", "WIN_EDITION", "WIN_LANG", "HOSTNAME_", "ADMIN_USER", "TIMEZONE_WIN",
                "INPUT_LOCALE", "UI_LANG", "PANEL_ACCESS", "REMOTE_AGENT", "WIN_APPS", "DISK_MODE",
                "WG_ENABLED", "TAILSCALE_ENABLED", "TAILSCALE_HOSTNAME", "WIN_SSH", "WIN_SSH_AUTH", "WIN_CLAUDE_CODE"]
        return json.dumps({**{k: cfg.get(k, "") for k in keep}, **(extra or {})})
    keep = ["UBUNTU_SERIES", "HOSTNAME_", "ADMIN_USER", "TIMEZONE", "LOCALE", "KEYBOARD_LAYOUT", "NET_MODE",
            "NET_ADDRESS", "STORAGE_MATCH_TYPE", "STORAGE_MATCH_VALUE", "STORAGE_LAYOUT",
            "TAILSCALE_ENABLED", "TAILSCALE_HOSTNAME", "DOCKER_ENABLED", "EXTRA_PACKAGES", "SSH_AUTH", "SSH_ROOT", "SSH_PORT",
            "COOLIFY_ENABLED", "COCKPIT_ENABLED", "COOLIFY_ADMIN_EMAIL", "WG_ENABLED",
            "DDNS_ENABLED", "DDNS_HOSTNAME", "CLOUDFLARE_ENABLED", "GPU_NVIDIA", "VIRT_ENABLED", "VIRT_BRIDGE",
            "SERVER_TWEAKS", "REMOTE_AGENT", "CLAUDE_CODE"]
    return json.dumps({k: cfg.get(k, "") for k in keep})

