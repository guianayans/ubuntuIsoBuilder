"""Gerador do autounattend.xml do Windows 11 (parte A — docs/windows/CONTRACT.md §2).

O arquivo gerado é AUTOCONTIDO: os scripts (windows/scripts/*) vão dentro dele, num
elemento <Extensions> que o Setup ignora, e são extraídos durante a instalação:

- windowsPE: comandos do cmd copiam as linhas "UABPE|..." do próprio arquivo (achado na
  raiz das unidades ou pelo registro, caso do Ventoy) para X:\\uab\\pe.js e o rodam no
  cscript (o WinPE do Setup não tem PowerShell). O pe.js escolhe o maior disco interno
  não-USB, particiona, aplica a imagem com o dism, cria o boot e reinicia;
- specialize: o PowerShell lê C:\\Windows\\Panther\\unattend.xml e grava os arquivos em
  C:\\ProgramData\\uab (scripts, configuração, token), e roda specialize.ps1, que cria as
  tarefas agendadas de pós-instalação e de relatório.

Só biblioteca padrão (Python 3.12).
"""
from __future__ import annotations

import base64
import json
import os
import re
from xml.sax.saxutils import escape, quoteattr

ROOT = os.environ.get("UAB_ROOT") or os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
WIN_DIR = os.path.join(ROOT, "windows")
SCRIPTS_DIR = os.path.join(WIN_DIR, "scripts")
APPS_FILE = os.path.join(WIN_DIR, "apps.json")

EXT_NS = "urn:uab:windows"
PE_PREFIX = "UABPE|"
UAB_DIR = r"C:\ProgramData\uab"
GENERIC_PRO_KEY = "VK7JG-NPHTM-C97JM-9MPGT-3V66T"
MIN_DISK_GB = 24

HOST_RE = re.compile(r"^[a-z0-9]([a-z0-9-]{0,13}[a-z0-9])?$")          # NetBIOS: até 15
USER_RE = re.compile(r"^[a-z_][a-z0-9_-]{0,19}$")                         # core.USER_RE, até 20
KEY_RE = re.compile(r"^[BCDFGHJKMNPQRTVWXY2346789]{5}(-[BCDFGHJKMNPQRTVWXY2346789]{5}){4}$")
WINGET_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.+_-]{0,99}$")
TZ_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9 .()+-]{0,63}$")
LOCALE_RE = re.compile(r"^[0-9A-Fa-f]{4}:[0-9A-Fa-f]{8}$")
LANG_RE = re.compile(r"^[a-z]{2,3}-[A-Z]{2}$")
BUILD_RE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")
TOKEN_RE = re.compile(r"^[A-Za-z0-9_-]{32,64}$")
URL_RE = re.compile(r"^https?://[A-Za-z0-9.-]+(:\d{1,5})?(/[A-Za-z0-9._~/%-]*)?$")
PUBKEY_RE = re.compile(r"^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|"
                       r"sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+={0,3}( [^\r\n]{0,200})?$")
MESHID_RE = re.compile(r"^[A-Za-z0-9@$_-]{16,128}$")
TS_KEY_RE = re.compile(r"^tskey-(auth-)?[A-Za-z0-9_-]{10,200}$")
TS_HOST_RE = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
RESERVED_USERS = {"administrator", "administrador", "guest", "convidado", "defaultaccount", "wdagutilityaccount",
                  "defaultuser0", "system", "sistema", "none", "nenhum", "users", "usuarios", "admin",
                  "administrators", "administradores", "everyone", "todos", "sshd", "network", "local"}
_CTRL = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")

# arquivos extraídos em C:\ProgramData\uab no passo specialize
PS_SCRIPTS = ("common.ps1", "specialize.ps1", "provision.ps1", "report.ps1", "vpn.ps1")

# Roda no specialize (via iex, com $x = o unattend.xml carregado): grava cada <File> e
# chama o specialize.ps1. .ps1 em UTF-8 COM BOM (o PowerShell 5.1 precisa para acentos).
EXTRACT_PS = r"""foreach ($f in $x.unattend.Extensions.File) {
  $p = $f.path
  [void](New-Item -ItemType Directory -Force -Path (Split-Path $p))
  [IO.File]::WriteAllText($p, $f.InnerText.Trim() + [char]10, (New-Object Text.UTF8Encoding ($p -like '*.ps1')))
}
& (Join-Path $env:ProgramData 'uab\specialize.ps1')"""


# ------------------------------------------------------------------ catálogo
def load_apps() -> list[dict]:
    """Catálogo de apps do Windows (windows/apps.json)."""
    with open(APPS_FILE, encoding="utf-8") as fh:
        apps = json.load(fh)
    if not isinstance(apps, list):
        raise ValueError("windows/apps.json deve ser uma lista")
    out = []
    for a in apps:
        if isinstance(a, dict) and WINGET_ID_RE.match(str(a.get("id") or "")):
            out.append({k: str(a.get(k) or "") for k in ("id", "name", "desc", "cat", "icon")})
    return out


# ------------------------------------------------------------------ utilidades
def _read_script(name: str) -> str:
    with open(os.path.join(SCRIPTS_DIR, name), encoding="utf-8") as fh:
        return fh.read()


def _x(v) -> str:
    """Texto seguro dentro de um elemento XML."""
    s = str(v)
    if _CTRL.search(s):
        raise ValueError("Valor com caracteres de controle não é aceito no arquivo de respostas.")
    return escape(s)


def _cdata(text: str) -> str:
    if _CTRL.search(text):
        raise ValueError("Script com caracteres de controle.")
    return "<![CDATA[" + text.replace("]]>", "]]]]><![CDATA[>") + "]]>"


def _pw(password: str, kind: str) -> str:
    """Senha no formato do unattend com PlainText=false (base64 de UTF-16LE senha+tipo)."""
    return base64.b64encode((password + kind).encode("utf-16-le")).decode("ascii")


def _js_ascii(src: str) -> str:
    """Troca caracteres não ASCII por \\uXXXX (o pe.js passa pelo find/echo do cmd)."""
    return "".join(c if 32 <= ord(c) < 127 or c in "\t" else f"\\u{ord(c):04x}" for c in src)


def _pe_lines(build_id: str, image: str) -> list[str]:
    src = _read_script("pe.js")
    head = "var UAB = " + json.dumps({"build": build_id, "image": image, "minGB": MIN_DISK_GB}) + ";"
    lines = []
    for ln in [head] + src.splitlines():
        s = ln.rstrip()
        if not s.strip() or s.strip().startswith("//"):
            continue  # linhas vazias somem no for /f do cmd; comentários não fazem falta
        s = _js_ascii(s)
        if s.lstrip().startswith("|") or "]]>" in s or PE_PREFIX in s:
            raise ValueError("pe.js tem uma linha que o extrator do WinPE não suporta: " + s[:60])
        lines.append(PE_PREFIX + s)
    return lines


def _comp(name: str, body: str) -> str:
    return (f'    <component name="{name}" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" '
            f'language="neutral" versionScope="nonSxS">\n{body}    </component>\n')


def _runsync(tag: str, cmds: list[tuple[str, str]], indent: str = "        ") -> str:
    """<RunSynchronous> com (descrição, comando). Comandos já em texto puro (escapados aqui)."""
    out = f"{indent}<RunSynchronous>\n"
    for i, (desc, cmd) in enumerate(cmds, 1):
        if len(cmd) > 259:  # limite do Setup: acima disso o arquivo inteiro é recusado
            raise ValueError(f"comando longo demais para o arquivo de respostas: {desc}")
        out += (f"{indent}  <{tag} wcm:action=\"add\"><Order>{i}</Order>"
                f"<Description>{_x(desc)}</Description><Path>{_pe_escape(cmd)}</Path></{tag}>\n")
    return out + f"{indent}</RunSynchronous>\n"


def _pe_escape(cmd: str) -> str:
    # o "|" do marcador vira referência de caractere: assim o texto cru do arquivo (lido
    # pelo find no WinPE) não tem o marcador fora das linhas do pe.js
    return _x(cmd).replace(PE_PREFIX, "UABPE&#124;")


# ------------------------------------------------------------------ validação
def _validate(cfg: dict, secrets: dict, ctx: dict) -> dict:
    v: dict = {}
    if str(cfg.get("OS", "windows")) != "windows":
        raise ValueError("Configuração não é de Windows.")
    ver, ed = str(cfg.get("WIN_VERSION") or "11"), str(cfg.get("WIN_EDITION") or "Pro")
    if ver != "11" or ed not in ("Pro", "Home", "Education", "Enterprise"):
        raise ValueError("Versão/edição do Windows não suportada (só Windows 11).")
    v["image"] = f"Windows {ver} {ed}"
    host = str(cfg.get("HOSTNAME_") or "").strip().lower()
    if not HOST_RE.match(host) or host.isdigit():
        raise ValueError("Nome do computador inválido: 1 a 15 caracteres (a-z, 0-9 e '-'), sem começar/terminar com '-'.")
    v["host"] = host
    user = str(cfg.get("ADMIN_USER") or "").strip()
    if not USER_RE.match(user) or user.lower() in RESERVED_USERS:
        raise ValueError("Usuário inválido ou reservado: minúsculas, números, _ ou - (até 20), começando com letra.")
    if user.lower() == host:
        raise ValueError("O usuário não pode ter o mesmo nome do computador.")
    v["user"] = user
    real = re.sub(r"[\x00-\x1f\x7f<>&\"'\\]", "", str(cfg.get("REALNAME") or "")).strip()[:60]
    v["realname"] = real or user
    pw = secrets.get("password")
    if not isinstance(pw, str) or not pw or len(pw) > 127 or _CTRL.search(pw) or "\n" in pw or "\r" in pw:
        raise ValueError("Senha do Windows inválida (1 a 127 caracteres, sem caracteres de controle).")
    v["password"] = pw
    key = re.sub(r"[\s-]", "", str(secrets.get("win_key") or "")).upper()
    if key:
        key = "-".join(key[i:i + 5] for i in range(0, len(key), 5))
        if not KEY_RE.match(key):
            raise ValueError("Chave do Windows inválida: use o formato XXXXX-XXXXX-XXXXX-XXXXX-XXXXX.")
    v["key"] = key or GENERIC_PRO_KEY
    tz = str(cfg.get("TIMEZONE_WIN") or "E. South America Standard Time")
    if not TZ_RE.match(tz):
        raise ValueError("Fuso horário do Windows inválido.")
    v["tz"] = tz
    il = str(cfg.get("INPUT_LOCALE") or "0416:00010416")
    if not LOCALE_RE.match(il):
        raise ValueError("Teclado inválido (formato 0416:00010416).")
    v["input"] = il
    lang = str(cfg.get("UI_LANG") or cfg.get("WIN_LANG") or "pt-BR")
    if not LANG_RE.match(lang):
        raise ValueError("Idioma inválido (ex.: pt-BR).")
    v["lang"] = lang
    dm = str(cfg.get("DISK_MODE") or "largest")
    if dm != "largest":
        raise ValueError("Modo de disco não suportado (só 'largest': maior disco interno).")
    apps = [a for a in str(cfg.get("WIN_APPS") or "").split() if a]
    apps = list(dict.fromkeys(apps))
    if len(apps) > 60:
        raise ValueError("Programas demais (máximo 60).")
    for a in apps:
        if not WINGET_ID_RE.match(a):
            raise ValueError(f"ID do winget inválido: {a[:40]}")
    v["apps"] = apps

    bid = str(ctx.get("build_id") or "")
    if not BUILD_RE.match(bid):
        raise ValueError("Identificador do build inválido.")
    v["build"] = bid
    purl = str(ctx.get("panel_url") or "").rstrip("/")
    if not URL_RE.match(purl):
        raise ValueError("Endereço do painel inválido (http:// ou https://).")
    v["panel_url"] = purl
    tok = str(ctx.get("report_token") or "")
    if not TOKEN_RE.match(tok):
        raise ValueError("Token de relatório inválido.")
    v["token"] = tok
    v["panel_access"] = str(cfg.get("PANEL_ACCESS") or "no") == "yes"
    pub = str(ctx.get("panel_pubkey") or "").strip()
    if pub and not PUBKEY_RE.match(pub):
        raise ValueError("Chave pública do painel inválida.")
    if v["panel_access"] and not pub:
        raise ValueError("Acesso pelo painel ligado, mas sem a chave pública do painel.")
    v["pubkey"] = pub if v["panel_access"] else ""
    v["remote_agent"] = str(cfg.get("REMOTE_AGENT") or "no") == "yes"
    rurl = str(ctx.get("remote_agent_url") or "").rstrip("/")
    mid = str(ctx.get("remote_meshid") or "").strip()
    if v["remote_agent"]:
        if not URL_RE.match(rurl):
            raise ValueError("Endereço do Remoto inválido.")
        if not MESHID_RE.match(mid):
            raise ValueError("Id do grupo do Remoto inválido.")
    v["remote_url"], v["meshid"] = (rurl, mid) if v["remote_agent"] else ("", "")
    # WireGuard pelo hub do painel: nada de segredo aqui (a chave privada é gerada no PC)
    v["wireguard"] = str(cfg.get("WG_ENABLED") or "no") == "yes"
    # Tailscale: a auth key é segredo (vai só no arquivo tailscale-authkey, apagado após o uso)
    v["tailscale"] = str(cfg.get("TAILSCALE_ENABLED") or "no") == "yes"
    v["ts_host"], v["ts_key"] = "", ""
    if v["tailscale"]:
        th = str(cfg.get("TAILSCALE_HOSTNAME") or host).strip().lower()
        if not TS_HOST_RE.match(th):
            raise ValueError("Nome na Tailnet inválido.")
        tk = re.sub(r"\s", "", str(secrets.get("tailscale_key") or ""))
        if not TS_KEY_RE.match(tk):
            raise ValueError("Auth key do Tailscale inválida (deve começar com tskey-auth-).")
        v["ts_host"], v["ts_key"] = th, tk
    return v


# ------------------------------------------------------------------ gerador
def render_autounattend(cfg: dict, secrets: dict, ctx: dict) -> str:
    """Devolve o autounattend.xml completo (UTF-8), autocontido."""
    v = _validate(cfg, secrets, ctx)
    lang, il = v["lang"], v["input"]

    intl = (f"      <InputLocale>{_x(il)}</InputLocale>\n      <SystemLocale>{_x(lang)}</SystemLocale>\n"
            f"      <UILanguage>{_x(lang)}</UILanguage>\n      <UserLocale>{_x(lang)}</UserLocale>\n")

    # ---- windowsPE: requisitos, extração do pe.js e execução
    drives = "C D E F G H I J K L M N O P Q R S T U V W Y Z"
    grab = r'(find "UABPE|" <{src} >X:\uab\t.txt && copy /y X:\uab\t.txt X:\uab\raw.txt >nul)'
    pe_cmds = [(f"LabConfig {k}", rf"reg.exe add HKLM\SYSTEM\Setup\LabConfig /v {k} /t REG_DWORD /d 1 /f")
               for k in ("BypassTPMCheck", "BypassSecureBootCheck", "BypassRAMCheck", "BypassStorageCheck", "BypassCPUCheck")]
    pe_cmds += [
        ("pasta de trabalho", r"cmd.exe /c md X:\uab"),
        ("respostas na raiz (autounattend.xml)",
         f"cmd.exe /c for %d in ({drives}) do @if exist %d:\\autounattend.xml " + grab.format(src=r"%d:\autounattend.xml")),
        ("respostas na raiz (unattend.xml)",
         f"cmd.exe /c for %d in ({drives}) do @if exist %d:\\unattend.xml " + grab.format(src=r"%d:\unattend.xml")),
        ("respostas pelo registro (Ventoy)",
         "cmd.exe /c for /f \"tokens=2*\" %a in ('reg query HKLM\\SYSTEM\\Setup /v UnattendFile 2^>nul') do @if exist \"%b\" "
         + grab.format(src='"%b"')),
        ("respostas no Panther do WinPE (boot por ramdisk, sem pendrive)",
         r"cmd.exe /c if exist X:\Windows\Panther\unattend.xml " + grab.format(src=r"X:\Windows\Panther\unattend.xml")),
        ("monta o pe.js",
         r'cmd.exe /c (for /f "usebackq tokens=1* delims=|" %a in ("X:\uab\raw.txt") do @echo.%b) >X:\uab\pe.js & exit 0'),
        ("instala no maior disco interno", r"cmd.exe /c if exist X:\uab\pe.js cscript.exe //nologo //e:jscript X:\uab\pe.js"),
    ]
    # O Setup novo (24H2+) ABORTA com erro 0x80070001 se um comando sair com código != 0
    # (ex.: "reg query" sem o valor). Todos os comandos do cmd terminam com código 0.
    pe_cmds = [(d, f"cmd.exe /c ({c[len('cmd.exe /c '):]}) & exit 0" if c.startswith("cmd.exe /c ") and not c.endswith("& exit 0") else c)
               for d, c in pe_cmds]
    for _, c in pe_cmds:
        if len(c) > 259:
            raise ValueError("comando do WinPE longo demais")  # limite do Setup

    windows_pe = (
        "  <settings pass=\"windowsPE\">\n"
        + _comp("Microsoft-Windows-International-Core-WinPE",
                f"      <SetupUILanguage><UILanguage>{_x(lang)}</UILanguage></SetupUILanguage>\n" + intl)
        + _comp("Microsoft-Windows-Setup",
                "      <UserData>\n"
                f"        <ProductKey><Key>{_x(v['key'])}</Key><WillShowUI>OnError</WillShowUI></ProductKey>\n"
                "        <AcceptEula>true</AcceptEula>\n"
                "      </UserData>\n"
                "      <UseConfigurationSet>false</UseConfigurationSet>\n"
                + _runsync("RunSynchronousCommand", pe_cmds, "      "))
        + "  </settings>\n")

    # ---- specialize: extração dos arquivos + tarefas
    # O Path de RunSynchronous tem limite de 259 caracteres (o Setup recusa o arquivo todo
    # se passar): o comando só carrega o XML e roda o código do elemento <Extract>.
    extract = ("powershell.exe -NoProfile -ExecutionPolicy Bypass -Command \"$x=New-Object xml;"
               "$x.Load($env:windir+'\\Panther\\unattend.xml');iex $x.unattend.Extensions['Extract'].InnerText;exit 0\"")
    specialize = (
        "  <settings pass=\"specialize\">\n"
        + _comp("Microsoft-Windows-Shell-Setup",
                f"      <ComputerName>{_x(v['host'].upper())}</ComputerName>\n"
                f"      <ProductKey>{_x(v['key'])}</ProductKey>\n"
                f"      <TimeZone>{_x(v['tz'])}</TimeZone>\n"
                f"      <RegisteredOwner>{_x(v['realname'])}</RegisteredOwner>\n")
        + _comp("Microsoft-Windows-International-Core", intl)
        + _comp("Microsoft-Windows-Deployment",
                _runsync("RunSynchronousCommand", [
                    ("BypassNRO", r"reg.exe add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\OOBE /v BypassNRO /t REG_DWORD /d 1 /f"),
                    ("extrai os scripts do ISO Builder e cria as tarefas", extract),
                ], "      "))
        + "  </settings>\n")

    # ---- oobeSystem: sem telas, usuário local administrador
    oobe = (
        "  <settings pass=\"oobeSystem\">\n"
        + _comp("Microsoft-Windows-International-Core", intl)
        + _comp("Microsoft-Windows-Shell-Setup",
                # AutoLogon uma vez: o PC loga sozinho no admin, o OOBE "preparando tudo para
                # você" finaliza e as tarefas de pós-instalação (SYSTEM) progridem sem ninguém no
                # local — essencial para o "formatar sem pendrive". Depois volta a pedir senha.
                "      <AutoLogon>\n"
                f"        <Username>{_x(v['user'])}</Username>\n"
                "        <Enabled>true</Enabled>\n"
                "        <LogonCount>1</LogonCount>\n"
                f"        <Password><Value>{_pw(v['password'], 'Password')}</Value><PlainText>false</PlainText></Password>\n"
                "      </AutoLogon>\n"
                "      <OOBE>\n"
                "        <HideEULAPage>true</HideEULAPage>\n"
                "        <HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>\n"
                "        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>\n"
                "        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>\n"
                "        <HideLocalAccountScreen>true</HideLocalAccountScreen>\n"
                "        <ProtectYourPC>3</ProtectYourPC>\n"
                "      </OOBE>\n"
                "      <UserAccounts>\n"
                "        <LocalAccounts>\n"
                "          <LocalAccount wcm:action=\"add\">\n"
                f"            <Name>{_x(v['user'])}</Name>\n"
                f"            <DisplayName>{_x(v['realname'])}</DisplayName>\n"
                "            <Group>Administrators</Group>\n"
                f"            <Password><Value>{_pw(v['password'], 'Password')}</Value><PlainText>false</PlainText></Password>\n"
                "          </LocalAccount>\n"
                "        </LocalAccounts>\n"
                "      </UserAccounts>\n"
                f"      <TimeZone>{_x(v['tz'])}</TimeZone>\n"
                f"      <RegisteredOwner>{_x(v['realname'])}</RegisteredOwner>\n")
        + "  </settings>\n")

    # ---- arquivos embutidos
    conf = {
        "build_id": v["build"], "panel_url": v["panel_url"], "hostname": v["host"], "admin_user": v["user"],
        "panel_access": v["panel_access"], "remote_agent": v["remote_agent"], "remote_url": v["remote_url"],
        "apps": v["apps"], "activate": v["key"] != GENERIC_PRO_KEY, "wireguard": v["wireguard"], "tailscale": v["tailscale"], "tailscale_hostname": v["ts_host"],
        "win_version": str(cfg.get("WIN_VERSION") or "11"),
        "win_edition": str(cfg.get("WIN_EDITION") or "Pro"),
    }
    files: list[tuple[str, str]] = [(f"{UAB_DIR}\\{n}", _read_script(n)) for n in PS_SCRIPTS]
    files.append((f"{UAB_DIR}\\uab.json", json.dumps(conf, ensure_ascii=False, indent=1)))
    files.append((f"{UAB_DIR}\\panel-token", v["token"]))
    if v["pubkey"]:
        files.append((f"{UAB_DIR}\\panel.pub", v["pubkey"]))
    if v["meshid"]:
        files.append((f"{UAB_DIR}\\remote-meshid", v["meshid"]))
    if v["ts_key"]:
        files.append((f"{UAB_DIR}\\tailscale-authkey", v["ts_key"]))
    ext = f"  <Extensions xmlns=\"{EXT_NS}\" build=\"uab-build:{v['build']}\">\n"
    for path, text in files:
        ext += f"    <File path={quoteattr(path)}>{_cdata(chr(10) + text)}</File>\n"
    ext += "    <Extract>" + _cdata("\n" + EXTRACT_PS + "\n") + "</Extract>\n"
    ext += "    <PE>" + _cdata("\n" + "\n".join(_pe_lines(v["build"], v["image"])) + "\n") + "</PE>\n"
    ext += "  </Extensions>\n"

    return (
        '<?xml version="1.0" encoding="utf-8"?>\n'
        f"<!-- ISO Builder: Windows {_x(str(cfg.get('WIN_VERSION') or '11'))} {_x(str(cfg.get('WIN_EDITION') or 'Pro'))}"
        f" para {_x(v['host'])}. APAGA o maior disco interno (nunca USB). -->\n"
        '<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">\n'
        + windows_pe + specialize + oobe + ext
        + "</unattend>\n")
