"""Núcleo da interface web: banco (SQLite), contas, sessões e validação."""
from __future__ import annotations

import hashlib
import hmac
import ipaddress
import os
import re
import secrets
import sqlite3
import subprocess
import tempfile
import threading
import time
from dataclasses import dataclass

DATA_DIR = os.environ.get("DATA_DIR", "/data")
DB_PATH = os.path.join(DATA_DIR, "app.db")
ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "yanguimaraesviana@gmail.com").strip().lower()
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "")
SESSION_HOURS = int(os.environ.get("SESSION_HOURS", "12"))
COOKIE_SECURE = os.environ.get("COOKIE_SECURE", "true").lower() != "false"
TEMP_MAX_BUILDS = int(os.environ.get("TEMP_MAX_BUILDS", "3"))
ADMIN_ID = 0

_lock = threading.RLock()


def now() -> int:
    return int(time.time())


def db() -> sqlite3.Connection:
    con = sqlite3.connect(DB_PATH, timeout=30, check_same_thread=False)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA journal_mode=WAL")
    con.execute("PRAGMA foreign_keys=ON")
    return con


def init_db() -> None:
    os.makedirs(DATA_DIR, exist_ok=True)
    os.chmod(DATA_DIR, 0o700)
    with db() as con:
        con.executescript(
            """
            CREATE TABLE IF NOT EXISTS users (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                email TEXT UNIQUE NOT NULL,
                label TEXT NOT NULL DEFAULT '',
                pw_hash TEXT NOT NULL,
                created_at INTEGER NOT NULL,
                expires_at INTEGER NOT NULL,
                revoked INTEGER NOT NULL DEFAULT 0,
                max_builds INTEGER NOT NULL DEFAULT 3,
                last_login INTEGER
            );
            CREATE TABLE IF NOT EXISTS sessions (
                token_hash TEXT PRIMARY KEY,
                user_id INTEGER NOT NULL,
                created_at INTEGER NOT NULL,
                expires_at INTEGER NOT NULL,
                ip TEXT
            );
            CREATE TABLE IF NOT EXISTS builds (
                id TEXT PRIMARY KEY,
                user_id INTEGER NOT NULL,
                owner_email TEXT NOT NULL,
                hostname TEXT NOT NULL,
                series TEXT NOT NULL,
                status TEXT NOT NULL,
                step TEXT NOT NULL DEFAULT '',
                progress REAL NOT NULL DEFAULT 0,
                message TEXT NOT NULL DEFAULT '',
                summary TEXT NOT NULL DEFAULT '{}',
                iso_path TEXT,
                iso_size INTEGER,
                sha256 TEXT,
                created_at INTEGER NOT NULL,
                finished_at INTEGER,
                expires_at INTEGER
            );
            CREATE TABLE IF NOT EXISTS dl_tokens (
                token_hash TEXT PRIMARY KEY,
                build_id TEXT NOT NULL,
                created_at INTEGER NOT NULL,
                expires_at INTEGER NOT NULL,
                created_by INTEGER NOT NULL
            );
            """
        )
        # migrações: contas "somente download" vinculadas a uma ISO
        cols = {r[1] for r in con.execute("PRAGMA table_info(users)")}
        if "mode" not in cols:
            con.execute("ALTER TABLE users ADD COLUMN mode TEXT NOT NULL DEFAULT 'build'")
        if "build_id" not in cols:
            con.execute("ALTER TABLE users ADD COLUMN build_id TEXT")
        # máquinas instaladas que se reportam ao painel
        bcols = {r[1] for r in con.execute("PRAGMA table_info(builds)")}
        if "report_token_hash" not in bcols:
            con.execute("ALTER TABLE builds ADD COLUMN report_token_hash TEXT")
        if "panel_access" not in bcols:
            con.execute("ALTER TABLE builds ADD COLUMN panel_access INTEGER NOT NULL DEFAULT 0")
        con.executescript(
            """
            CREATE TABLE IF NOT EXISTS machines (
                id TEXT PRIMARY KEY,
                build_id TEXT NOT NULL,
                machine_id TEXT NOT NULL,
                hostname TEXT NOT NULL,
                first_seen INTEGER NOT NULL,
                last_seen INTEGER NOT NULL,
                report TEXT NOT NULL DEFAULT '{}',
                label TEXT NOT NULL DEFAULT '',
                notes TEXT NOT NULL DEFAULT '',
                ssh_host TEXT NOT NULL DEFAULT '',
                ssh_port INTEGER NOT NULL DEFAULT 22,
                source_ip TEXT NOT NULL DEFAULT '',
                UNIQUE (build_id, machine_id)
            );
            CREATE INDEX IF NOT EXISTS builds_report_token ON builds (report_token_hash);
            CREATE TABLE IF NOT EXISTS downloads (
                build_id TEXT NOT NULL,
                email TEXT NOT NULL,
                kind TEXT NOT NULL,
                at INTEGER NOT NULL
            );
            """
        )
        # sócios: permissões por máquina do painel ("machine") ou aparelho só do Remoto ("node")
        con.execute("""CREATE TABLE IF NOT EXISTS partner_perms (
            user_id INTEGER NOT NULL, kind TEXT NOT NULL, ref TEXT NOT NULL, perms INTEGER NOT NULL,
            PRIMARY KEY (user_id, kind, ref))""")
        # aviso de desligamento/reinício enviado pela máquina (bolinha vermelha na hora)
        mcols = {r[1] for r in con.execute("PRAGMA table_info(machines)")}
        if "off_at" not in mcols:
            con.execute("ALTER TABLE machines ADD COLUMN off_at INTEGER")
        if "off_reason" not in mcols:
            con.execute("ALTER TABLE machines ADD COLUMN off_reason TEXT NOT NULL DEFAULT ''")


# --------------------------------------------------------------------- senhas
def hash_password(pw: str) -> str:
    salt = secrets.token_bytes(16)
    dk = hashlib.scrypt(pw.encode(), salt=salt, n=2**14, r=8, p=1, dklen=32)
    return f"scrypt${salt.hex()}${dk.hex()}"


def verify_password(pw: str, stored: str) -> bool:
    try:
        _, salt, dk = stored.split("$")
        got = hashlib.scrypt(pw.encode(), salt=bytes.fromhex(salt), n=2**14, r=8, p=1, dklen=32)
        return hmac.compare_digest(got.hex(), dk)
    except Exception:
        return False


_ADMIN_HASH = hash_password(ADMIN_PASSWORD) if len(ADMIN_PASSWORD) >= 12 else ""
_DUMMY_HASH = hash_password(secrets.token_hex(8))

ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"


def generate_password() -> str:
    return "-".join("".join(secrets.choice(ALPHABET) for _ in range(4)) for _ in range(2))  # xxxx-xxxx


# --------------------------------------------------------------------- contas
@dataclass
class User:
    id: int
    email: str
    role: str  # admin | temp
    label: str = ""
    expires_at: int | None = None
    max_builds: int = 0
    mode: str = "build"  # build | download
    build_id: str | None = None

    @property
    def is_admin(self) -> bool:
        return self.role == "admin"

    @property
    def download_only(self) -> bool:
        return self.role == "temp" and self.mode == "download"

    @property
    def is_partner(self) -> bool:  # sócio: conta permanente, só as máquinas liberadas
        return self.role == "temp" and self.mode == "partner"

    def public(self) -> dict:
        return {"email": self.email, "role": self.role, "label": self.label, "expires_at": self.expires_at,
                "mode": self.mode, "build_id": self.build_id}


def user_from_row(r) -> "User":
    return User(r["id"], r["email"], "temp", r["label"], r["expires_at"], r["max_builds"],
                r["mode"] or "build", r["build_id"])


ADMIN_USER = User(ADMIN_ID, ADMIN_EMAIL, "admin", "Administrador")


class RateLimiter:
    """Limita tentativas de login por IP e por e-mail (memória)."""

    def __init__(self):
        self.hits: dict[str, list[int]] = {}

    def blocked(self, *keys: str, limit: int, window: int) -> bool:
        t = now()
        with _lock:
            for k in keys:
                self.hits[k] = [x for x in self.hits.get(k, []) if t - x < window]
                if len(self.hits[k]) >= limit:
                    return True
        return False

    def hit(self, *keys: str) -> None:
        with _lock:
            for k in keys:
                self.hits.setdefault(k, []).append(now())

    def clear(self, key: str) -> None:
        with _lock:
            self.hits.pop(key, None)


limiter = RateLimiter()


def authenticate(email: str, pw: str) -> User | None:
    email = email.strip().lower()
    if email == ADMIN_EMAIL:
        if _ADMIN_HASH and verify_password(pw, _ADMIN_HASH):
            return ADMIN_USER
        verify_password(pw, _DUMMY_HASH)
        return None
    with db() as con:
        row = con.execute("SELECT * FROM users WHERE email=?", (email,)).fetchone()
    if not row:
        verify_password(pw, _DUMMY_HASH)  # tempo constante
        return None
    if not verify_password(pw, row["pw_hash"]) or row["revoked"] or row["expires_at"] <= now():
        return None
    with db() as con:
        con.execute("UPDATE users SET last_login=? WHERE id=?", (now(), row["id"]))
    return user_from_row(row)


def create_session(user: User, ip: str) -> str:
    token = secrets.token_urlsafe(32)
    exp = now() + SESSION_HOURS * 3600
    if user.expires_at:
        exp = min(exp, user.expires_at)
    with db() as con:
        con.execute("INSERT INTO sessions VALUES (?,?,?,?,?)",
                    (hashlib.sha256(token.encode()).hexdigest(), user.id, now(), exp, ip))
    return token


def session_user(token: str | None) -> User | None:
    if not token:
        return None
    th = hashlib.sha256(token.encode()).hexdigest()
    with db() as con:
        s = con.execute("SELECT * FROM sessions WHERE token_hash=?", (th,)).fetchone()
        if not s or s["expires_at"] <= now():
            return None
        if s["user_id"] == ADMIN_ID:
            return ADMIN_USER if _ADMIN_HASH else None
        u = con.execute("SELECT * FROM users WHERE id=?", (s["user_id"],)).fetchone()
    if not u or u["revoked"] or u["expires_at"] <= now():
        return None
    return user_from_row(u)


def delete_session(token: str | None) -> None:
    if token:
        with db() as con:
            con.execute("DELETE FROM sessions WHERE token_hash=?", (hashlib.sha256(token.encode()).hexdigest(),))


def cleanup_sessions() -> None:
    with db() as con:
        con.execute("DELETE FROM sessions WHERE expires_at<=?", (now(),))


# --------------------------------------------------------------------- links de download
def token_hash(token: str) -> str:
    return hashlib.sha256(token.encode()).hexdigest()


def create_dl_token(build_id: str, expires_at: int, created_by: int) -> str:
    token = secrets.token_urlsafe(32)
    with db() as con:
        con.execute("INSERT INTO dl_tokens VALUES (?,?,?,?,?)",
                    (hashlib.sha256(token.encode()).hexdigest(), build_id, now(), expires_at, created_by))
    return token


def dl_token_build(token: str) -> str | None:
    if not re.match(r"^[A-Za-z0-9_-]{32,64}$", token or ""):
        return None
    with db() as con:
        r = con.execute("SELECT build_id, expires_at FROM dl_tokens WHERE token_hash=?",
                        (hashlib.sha256(token.encode()).hexdigest(),)).fetchone()
        con.execute("DELETE FROM dl_tokens WHERE expires_at<=?", (now(),))
    if not r or r["expires_at"] <= now():
        return None
    return r["build_id"]


# --------------------------------------------------------------------- validação
HOSTNAME_RE = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
USER_RE = re.compile(r"^[a-z_][a-z0-9_-]{0,31}$")
RESERVED_USERS = {"root", "daemon", "bin", "sys", "sync", "games", "man", "lp", "mail", "news", "uucp",
                  "proxy", "www-data", "backup", "list", "irc", "nobody", "ubuntu", "docker", "tailscale",
                  "syslog", "messagebus", "sshd"}
PUBKEY_RE = re.compile(r"^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|"
                       r"sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/=]+( .*)?$")
EMAIL_RE = re.compile(r"^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$")


def password_leaked(pw: str) -> bool | None:
    """Mesma checagem que o Coolify faz (Have I Been Pwned, k-anonimato: só os 5
    primeiros caracteres do SHA-1 saem daqui). None = não foi possível consultar."""
    import urllib.request
    h = hashlib.sha1(pw.encode()).hexdigest().upper()
    try:
        req = urllib.request.Request(f"https://api.pwnedpasswords.com/range/{h[:5]}",
                                     headers={"Add-Padding": "true", "User-Agent": "iso-builder"})
        body = urllib.request.urlopen(req, timeout=8).read().decode()
    except Exception:  # noqa: BLE001
        return None
    return any(l.split(":")[0] == h[5:] and l.split(":")[1].strip() != "0" for l in body.splitlines() if ":" in l)


class Invalid(ValueError):
    pass


def _req(d: dict, k: str) -> str:
    v = d.get(k)
    return "" if v is None else str(v).strip()


def validate_pubkeys(text: str) -> list[str]:
    if "PRIVATE KEY" in text or "-----BEGIN" in text:
        raise Invalid("Isso é uma CHAVE PRIVADA. Nunca envie a chave privada: use o arquivo .pub.")
    keys = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if not PUBKEY_RE.match(line):
            raise Invalid(f"Linha não é uma chave pública SSH válida: {line[:40]}…")
        with tempfile.NamedTemporaryFile("w", delete=True, dir=os.path.join(DATA_DIR, "tmp")) as fh:
            fh.write(line + "\n")
            fh.flush()
            r = subprocess.run(["ssh-keygen", "-l", "-f", fh.name], capture_output=True, text=True)
        if r.returncode != 0:
            raise Invalid(f"Chave rejeitada pelo ssh-keygen: {line[:40]}…")
        if line.startswith("ssh-rsa") and int(r.stdout.split()[0]) < 3072:
            raise Invalid("Chave RSA com menos de 3072 bits é fraca. Prefira ed25519.")
        keys.append(line)
    if not keys:
        raise Invalid("Informe ao menos uma chave pública SSH.")
    return list(dict.fromkeys(keys))


def validate_build(d: dict) -> dict:
    """Valida o formulário do assistente e devolve a configuração normalizada."""
    c: dict = {}
    c["UBUNTU_SERIES"] = _req(d, "series") or "24.04"
    if c["UBUNTU_SERIES"] not in ("24.04", "26.04"):
        raise Invalid("Versão do Ubuntu inválida.")
    c["HOSTNAME_"] = _req(d, "hostname").lower()
    if not HOSTNAME_RE.match(c["HOSTNAME_"]):
        raise Invalid("Hostname inválido: use a-z, 0-9 e '-' (até 63).")
    c["ADMIN_USER"] = _req(d, "username")
    if not USER_RE.match(c["ADMIN_USER"]) or c["ADMIN_USER"] in RESERVED_USERS:
        raise Invalid("Usuário inválido ou reservado.")
    c["ADMIN_REALNAME"] = re.sub(r"[\"'\\:]", "", _req(d, "realname"))[:60] or c["ADMIN_USER"]
    c["TIMEZONE"] = _req(d, "timezone") or "America/Sao_Paulo"
    if not re.match(r"^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$", c["TIMEZONE"]) or \
            not os.path.isfile(os.path.join("/usr/share/zoneinfo", c["TIMEZONE"])):
        raise Invalid("Timezone inválida.")
    c["LOCALE"] = _req(d, "locale") or "pt_BR.UTF-8"
    if not re.match(r"^[a-z]{2,3}(_[A-Z]{2})?\.UTF-8$|^C\.UTF-8$", c["LOCALE"]):
        raise Invalid("Locale inválido.")
    c["KEYBOARD_LAYOUT"] = _req(d, "keyboard") or "br"
    if not re.match(r"^[a-z]{2,8}$", c["KEYBOARD_LAYOUT"]):
        raise Invalid("Layout de teclado inválido.")
    c["KEYBOARD_VARIANT"] = _req(d, "keyboard_variant")
    if not re.match(r"^[a-z0-9_-]*$", c["KEYBOARD_VARIANT"]):
        raise Invalid("Variante de teclado inválida.")

    ssh_auth = _req(d, "ssh_auth") or "key"
    if ssh_auth not in ("key", "password"):
        raise Invalid("Modo de acesso SSH inválido.")
    c["SSH_AUTH"] = ssh_auth
    port = str(d.get("ssh_port") or "22").strip()
    if not (port.isdigit() and (int(port) == 22 or (1024 <= int(port) <= 65535
                                                    and int(port) not in (6001, 6002, 8000, 8080, 9090)))):
        raise Invalid("Porta SSH inválida: use 22 (padrão) ou de 1024 a 65535 "
                      "(exceto 6001, 6002, 8000, 8080 e 9090, usadas pelos programas).")
    c["SSH_PORT"] = str(int(port))
    pw = str(d.get("password") or "")
    min_pw = 12 if ssh_auth == "password" else 10
    if len(pw) < min_pw:
        raise Invalid(f"A senha do servidor precisa ter ao menos {min_pw} caracteres.")

    # rede
    net = _req(d, "net_mode") or "dhcp"
    c.update(NET_MODE=net, NET_MATCH_TYPE="glob", NET_MATCH_VALUE="en*", NET_ADDRESS="", NET_GATEWAY="",
             NET_DNS="", NET_SEARCH="")
    if net == "static":
        mt = _req(d, "net_match_type")
        mv = _req(d, "net_match_value")
        if mt == "mac":
            mv = mv.lower().replace("-", ":")
            if not re.match(r"^([0-9a-f]{2}:){5}[0-9a-f]{2}$", mv):
                raise Invalid("Endereço MAC inválido.")
        elif mt == "name":
            if not re.match(r"^[a-zA-Z0-9._-]{1,15}$", mv):
                raise Invalid("Nome de interface inválido.")
        elif mt == "glob":
            mv = "en*"
        else:
            raise Invalid("Escolha como identificar a interface de rede.")
        try:
            iface = ipaddress.ip_interface(_req(d, "net_address"))
            gw = ipaddress.ip_address(_req(d, "net_gateway"))
            dns = [str(ipaddress.ip_address(x.strip())) for x in _req(d, "net_dns").split(",") if x.strip()]
        except ValueError:
            raise Invalid("IP, gateway ou DNS inválido.")
        if "/" not in _req(d, "net_address") or iface.version != 4 or gw not in iface.network or not dns:
            raise Invalid("IP/CIDR, gateway (na mesma rede) e DNS são obrigatórios.")
        search = _req(d, "net_search")
        if search and not re.match(r"^[a-z0-9-]+(\.[a-z0-9-]+)*$", search):
            raise Invalid("Domínio de busca inválido.")
        c.update(NET_MATCH_TYPE=mt, NET_MATCH_VALUE=mv, NET_ADDRESS=_req(d, "net_address"),
                 NET_GATEWAY=str(gw), NET_DNS=",".join(dns), NET_SEARCH=search)
    elif net != "dhcp":
        raise Invalid("Modo de rede inválido.")

    # disco
    st = _req(d, "storage_type") or "single"
    sv = _req(d, "storage_value")
    if st == "single":
        sv = "single"
    elif st == "serial":
        if not re.match(r"^[A-Za-z0-9_.*:-]{3,}$", sv):
            raise Invalid("Número de série inválido.")
    elif st == "path":
        if not re.match(r"^/dev/(sd[a-z]+|nvme[0-9]+n[0-9]+|vd[a-z]+|hd[a-z]+|mmcblk[0-9]+|xvd[a-z]+|disk/by-id/[A-Za-z0-9_.:-]+)$", sv):
            raise Invalid("Informe o disco inteiro, ex.: /dev/nvme0n1.")
    elif st == "model":
        sv = sv.replace(" ", "_")
        if not re.match(r"^[A-Za-z0-9_.*-]{3,}$", sv):
            raise Invalid("Modelo inválido.")
    else:
        raise Invalid("Critério de disco inválido.")
    try:
        min_gb = int(d.get("storage_min_gb") or 20)
    except (TypeError, ValueError):
        raise Invalid("Tamanho mínimo inválido.")
    if min_gb < 8:
        raise Invalid("Tamanho mínimo: 8 GB.")
    layout = _req(d, "storage_layout") or "lvm"
    if layout not in ("lvm", "direct"):
        raise Invalid("Layout inválido.")
    allow_usb = "yes" if d.get("storage_allow_usb") and st != "single" else "no"
    if _req(d, "confirm_erase") != "APAGAR":
        raise Invalid("Digite APAGAR para confirmar que o disco de destino será apagado.")
    c.update(STORAGE_MATCH_TYPE=st, STORAGE_MATCH_VALUE=sv, STORAGE_MIN_GB=str(min_gb),
             STORAGE_LAYOUT=layout, STORAGE_ALLOW_USB=allow_usb, STORAGE_CONFIRMED=f"APAGAR:{st}:{sv}")

    keys_text = str(d.get("ssh_keys") or "")
    if ssh_auth == "password" and not keys_text.strip():
        keys = []  # modo senha: chave SSH opcional
    else:
        keys = validate_pubkeys(keys_text)

    # WireGuard pelo seu servidor (ponto de encontro)
    c["WG_ENABLED"] = "yes" if d.get("wireguard") else "no"

    # tailscale
    ts_key = ""
    if d.get("tailscale"):
        c["TAILSCALE_ENABLED"] = "yes"
        c["TAILSCALE_HOSTNAME"] = (_req(d, "tailscale_hostname") or c["HOSTNAME_"]).lower()
        if not HOSTNAME_RE.match(c["TAILSCALE_HOSTNAME"]):
            raise Invalid("Nome na Tailnet inválido.")
        extra = _req(d, "tailscale_extra")
        if not re.match(r"^(\s*--[a-z0-9-]+(=[A-Za-z0-9:,./_@-]+)?)*\s*$", extra) or \
                re.search(r"--(auth-?key|hostname)", extra):
            raise Invalid("Argumentos extras do Tailscale inválidos.")
        c["TAILSCALE_EXTRA_ARGS"] = extra
        ts_key = re.sub(r"\s", "", str(d.get("tailscale_key") or ""))
        if not re.match(r"^tskey-(auth-)?[A-Za-z0-9_-]{10,}$", ts_key):
            raise Invalid("Auth key do Tailscale inválida (deve começar com tskey-auth-).")
    else:
        c.update(TAILSCALE_ENABLED="no", TAILSCALE_HOSTNAME="", TAILSCALE_EXTRA_ARGS="")

    # Programas: none | docker | coolify (inclui Docker) | coolify_cockpit
    apps = _req(d, "apps") or ("docker" if d.get("docker") else "none")
    if apps not in ("none", "docker", "coolify", "coolify_cockpit"):
        raise Invalid("Opção de programas inválida.")
    coolify = apps in ("coolify", "coolify_cockpit")
    c["DOCKER_ENABLED"] = "yes" if apps != "none" else "no"
    c["COOLIFY_ENABLED"] = "yes" if coolify else "no"
    c["COCKPIT_ENABLED"] = "yes" if apps == "coolify_cockpit" else "no"
    coolify_pw = ""
    if coolify:
        coolify_pw = str(d.get("coolify_password") or "")
        if len(coolify_pw) < 12 or not (re.search(r"[a-z]", coolify_pw) and re.search(r"[A-Z]", coolify_pw)
                                        and re.search(r"\d", coolify_pw) and re.search(r"[^A-Za-z0-9]", coolify_pw)):
            raise Invalid("Senha do Coolify: mínimo 12 caracteres, com maiúscula, minúscula, número e símbolo.")
        if "'" in coolify_pw or "\n" in coolify_pw:
            raise Invalid("A senha do Coolify não pode conter aspas simples nem quebra de linha.")
        if password_leaked(coolify_pw):
            raise Invalid("Essa senha do Coolify aparece em vazamentos de dados conhecidos, e o Coolify "
                          "recusaria criar o admin com ela. Escolha outra.")
        cemail = _req(d, "coolify_email").lower() or ADMIN_EMAIL
        if not EMAIL_RE.match(cemail):
            raise Invalid("E-mail do admin do Coolify inválido.")
        c["COOLIFY_ADMIN_EMAIL"] = cemail
    panel_access = d.get("panel_access", True) is not False
    pk = " ".join(_req(d, "extra_packages").split())
    if pk and not all(re.match(r"^[a-z0-9][a-z0-9+.-]+$", p) for p in pk.split()):
        raise Invalid("Nome de pacote inválido.")
    c["EXTRA_PACKAGES"] = pk

    # Extras (os mesmos recursos do host-mirror; todos opcionais)
    ddns_pw = cf_token = ""
    c["DDNS_ENABLED"] = "yes" if d.get("ddns") else "no"
    c["DDNS_HOSTNAME"] = c["DDNS_LOGIN"] = ""
    if d.get("ddns"):
        c["DDNS_HOSTNAME"] = _req(d, "ddns_hostname").lower()
        if not re.match(r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$", c["DDNS_HOSTNAME"]):
            raise Invalid("DDNS: hostname do No-IP inválido (ex.: meuservidor.ddns.net).")
        c["DDNS_LOGIN"] = _req(d, "ddns_login")
        if not re.match(r"^[^\s'\\]{1,128}$", c["DDNS_LOGIN"]):
            raise Invalid("DDNS: informe o usuário (ou e-mail) do No-IP.")
        ddns_pw = str(d.get("ddns_password") or "")
        if not ddns_pw or "'" in ddns_pw or "\\" in ddns_pw or "\n" in ddns_pw:
            raise Invalid("DDNS: informe a senha do No-IP (sem aspas simples nem barra invertida).")
    c["CLOUDFLARE_ENABLED"] = "yes" if d.get("cloudflare") else "no"
    if d.get("cloudflare"):
        # aceita o token puro ou o comando inteiro copiado da Cloudflare
        # ("cloudflared service install <token>" / "... run --token <token>")
        found = re.findall(r"[A-Za-z0-9_=+/-]{80,4096}", str(d.get("cloudflare_token") or ""))
        cf_token = found[-1] if found else ""
        if not cf_token:
            raise Invalid("Cloudflare: cole o token do túnel (o texto longo depois de --token).")
    c["GPU_NVIDIA"] = "yes" if d.get("gpu") else "no"
    c["VIRT_ENABLED"] = "yes" if d.get("virt") else "no"
    c["VIRT_BRIDGE"] = "yes" if d.get("virt") and d.get("virt_bridge", True) else "no"
    c["SERVER_TWEAKS"] = "yes" if d.get("tweaks") else "no"
    c["REMOTE_AGENT"] = "yes" if d.get("remote_agent") else "no"  # o builder completa URL e id do grupo
    return {"config": c, "keys": keys, "password": pw, "ts_key": ts_key, "panel_access": panel_access,
            "coolify_password": coolify_pw, "ddns_password": ddns_pw, "cloudflare_token": cf_token}


def validate_email_or_login(s: str) -> str:
    s = s.strip().lower()
    if not EMAIL_RE.match(s) or len(s) > 120:
        raise Invalid("E-mail inválido.")
    if s == ADMIN_EMAIL:
        raise Invalid("Esse e-mail é da conta administradora.")
    return s


# --------------------------------------------------------------------- sócios
# Permissões por máquina (bits). "Admin" na tela = todas.
P_VIEW, P_TERMINAL, P_DESKTOP, P_FILES, P_VPN, P_CONFIG = 1, 2, 4, 8, 16, 32
P_ALL = P_VIEW | P_TERMINAL | P_DESKTOP | P_FILES | P_VPN | P_CONFIG
P_NODE_ALL = P_VIEW | P_TERMINAL | P_DESKTOP | P_FILES  # aparelho só do Remoto: sem VPN/config
PARTNER_UNTIL = 253402300799  # 9999-12-31: sócio não expira (revogar/excluir é manual)


def partner_perms(user_id: int) -> dict[tuple[str, str], int]:
    with db() as con:
        return {(r["kind"], r["ref"]): r["perms"] for r in
                con.execute("SELECT kind, ref, perms FROM partner_perms WHERE user_id=?", (user_id,))}


def set_partner_perms(user_id: int, items: list[dict]) -> None:
    rows = []
    for it in items:
        kind, ref = str(it.get("kind")), str(it.get("ref") or "")
        if kind not in ("machine", "node") or not re.fullmatch(r"[A-Za-z0-9@$_\-]{4,120}", ref):
            raise Invalid("Máquina inválida nas permissões.")
        perms = int(it.get("perms") or 0) & (P_ALL if kind == "machine" else P_NODE_ALL)
        if perms:
            rows.append((user_id, kind, ref, perms | P_VIEW))  # qualquer permissão inclui ver
    with db() as con:
        con.execute("DELETE FROM partner_perms WHERE user_id=?", (user_id,))
        con.executemany("INSERT INTO partner_perms (user_id, kind, ref, perms) VALUES (?,?,?,?)", rows)


def machine_perms(u: "User", machine_id: str) -> int:
    if u.is_admin:
        return P_ALL
    if not u.is_partner:
        return 0
    return partner_perms(u.id).get(("machine", machine_id), 0)
