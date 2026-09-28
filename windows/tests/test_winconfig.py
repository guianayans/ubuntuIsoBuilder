"""Testes do gerador do autounattend.xml (web/app/winconfig.py).

Rodar (na raiz do projeto):  python3 -m unittest discover -s windows/tests -v
"""
import base64
import html
import json
import os
import re
import shutil
import subprocess
import sys
import unittest
import xml.etree.ElementTree as ET

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "..", "web"))
from app import winconfig as w  # noqa: E402

U = "{urn:schemas-microsoft-com:unattend}"
X = "{urn:uab:windows}"
WCM = "{http://schemas.microsoft.com/WMIConfig/2002/State}"


def cfg(**kw):
    c = {"OS": "windows", "WIN_VERSION": "11", "WIN_EDITION": "Pro", "WIN_LANG": "pt-BR", "UI_LANG": "pt-BR",
         "HOSTNAME_": "recepcao-01", "ADMIN_USER": "suporte", "REALNAME": "Suporte",
         "TIMEZONE_WIN": "E. South America Standard Time", "INPUT_LOCALE": "0416:00010416",
         "PANEL_ACCESS": "yes", "REMOTE_AGENT": "yes", "WIN_APPS": "Google.Chrome 7zip.7zip Notepad++.Notepad++",
         "DISK_MODE": "largest"}
    c.update(kw)
    return c


PUB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq7l8tJ1c0Zb1pX0mYlT1y0n0m2VZ3N8b4C1Q2w3e4r painel@iso"


def ctx(**kw):
    c = {"build_id": "abc123", "panel_url": "https://iso.exemplo.com.br", "report_token": "T" * 40,
         "panel_pubkey": PUB, "remote_agent_url": "https://iso.exemplo.com.br/remoto",
         "remote_meshid": "tzK@Eg$abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOP"}
    c.update(kw)
    return c


def sec(**kw):
    s = {"password": "Senha-Forte-123", "win_key": "VK7JG-NPHTM-C97JM-9MPGT-3V66T"}
    s.update(kw)
    return s


def render(c=None, s=None, x=None):
    return w.render_autounattend(c or cfg(), s or sec(), x or ctx())


def comp(root, pass_, name):
    for st in root.findall(f"{U}settings"):
        if st.get("pass") == pass_:
            for c in st.findall(f"{U}component"):
                if c.get("name") == name:
                    return c
    return None


class TestXml(unittest.TestCase):
    def test_xml_valido_e_passos(self):
        root = ET.fromstring(render().encode("utf-8"))
        self.assertEqual(root.tag, f"{U}unattend")
        passes = [s.get("pass") for s in root.findall(f"{U}settings")]
        self.assertEqual(passes, ["windowsPE", "specialize", "oobeSystem"])
        for st in root.findall(f"{U}settings"):
            for c in st.findall(f"{U}component"):
                self.assertEqual(c.get("processorArchitecture"), "amd64")
                self.assertEqual(c.get("publicKeyToken"), "31bf3856ad364e35")

    def test_valores_principais(self):
        root = ET.fromstring(render().encode())
        sh = comp(root, "specialize", "Microsoft-Windows-Shell-Setup")
        self.assertEqual(sh.find(f"{U}ComputerName").text, "RECEPCAO-01")
        self.assertEqual(sh.find(f"{U}TimeZone").text, "E. South America Standard Time")
        self.assertEqual(sh.find(f"{U}ProductKey").text, "VK7JG-NPHTM-C97JM-9MPGT-3V66T")
        pe = comp(root, "windowsPE", "Microsoft-Windows-International-Core-WinPE")
        self.assertEqual(pe.find(f"{U}InputLocale").text, "0416:00010416")
        self.assertEqual(pe.find(f"{U}UILanguage").text, "pt-BR")
        oobe = comp(root, "oobeSystem", "Microsoft-Windows-Shell-Setup")
        o = oobe.find(f"{U}OOBE")
        for k in ("HideEULAPage", "HideOnlineAccountScreens", "HideWirelessSetupInOOBE", "HideLocalAccountScreen"):
            self.assertEqual(o.find(f"{U}{k}").text, "true")
        acc = oobe.find(f"{U}UserAccounts/{U}LocalAccounts/{U}LocalAccount")
        self.assertEqual(acc.find(f"{U}Name").text, "suporte")
        self.assertEqual(acc.find(f"{U}Group").text, "Administrators")
        pw = acc.find(f"{U}Password")
        self.assertEqual(pw.find(f"{U}PlainText").text, "false")
        self.assertEqual(base64.b64decode(pw.find(f"{U}Value").text).decode("utf-16-le"), "Senha-Forte-123Password")

    def test_senha_nao_aparece_em_texto(self):
        xml = render(s=sec(password="Minha$Senha<&>'\"123"))
        self.assertNotIn("Minha$Senha", xml)
        ET.fromstring(xml.encode())

    def test_escape_xml(self):
        xml = render(c=cfg(REALNAME='Ana & <Bia> "x"'))
        root = ET.fromstring(xml.encode())
        sh = comp(root, "oobeSystem", "Microsoft-Windows-Shell-Setup")
        self.assertNotIn("<Bia>", xml)
        self.assertEqual(w._x('a<&>"b'), 'a&lt;&amp;&gt;"b')
        self.assertEqual(w._cdata("x]]>y"), "<![CDATA[x]]]]><![CDATA[>y]]>")
        with self.assertRaises(ValueError):
            w._x("a\x01b")
        # o gerador tira os caracteres perigosos do nome exibido
        self.assertEqual(sh.find(f"{U}RegisteredOwner").text, "Ana  Bia x")

    def test_todos_os_comandos_ate_259(self):
        root = ET.fromstring(render().encode())
        paths = [p.text for p in root.iter(f"{U}Path")]
        self.assertGreaterEqual(len(paths), 12)
        for p in paths:
            self.assertLessEqual(len(p), 259, p)

    def test_sem_variavel_do_ventoy(self):
        # o Ventoy trata $$NOME$$ como variável e para pedindo valor
        self.assertNotIn("$$", render())

    def test_bypass_e_nro(self):
        xml = html.unescape(render())
        for k in ("BypassTPMCheck", "BypassSecureBootCheck", "BypassRAMCheck"):
            self.assertIn(rf"HKLM\SYSTEM\Setup\LabConfig /v {k}", xml)
        self.assertIn("BypassNRO", xml)

    def test_comandos_pe_curtos_e_saem_com_zero(self):
        root = ET.fromstring(render().encode())
        setup = comp(root, "windowsPE", "Microsoft-Windows-Setup")
        cmds = [c.find(f"{U}Path").text for c in setup.find(f"{U}RunSynchronous")]
        orders = [int(c.find(f"{U}Order").text) for c in setup.find(f"{U}RunSynchronous")]
        self.assertEqual(orders, list(range(1, len(orders) + 1)))
        for c in cmds:
            self.assertLessEqual(len(c), 259)
            if c.startswith("cmd.exe"):
                self.assertTrue(c.endswith("& exit 0"), c)  # o Setup novo aborta com código != 0
        self.assertTrue(any("cscript.exe" in c and "pe.js" in c for c in cmds))
        # o marcador cru só aparece nas linhas do pe.js (o find do WinPE depende disso)
        raw = render()
        lines = [ln for ln in raw.splitlines() if w.PE_PREFIX in ln]
        self.assertTrue(lines and all(ln.startswith(w.PE_PREFIX) for ln in lines))

    def test_pe_js_extraido_e_ascii(self):
        raw = render()
        lines = [ln for ln in raw.splitlines() if ln.startswith(w.PE_PREFIX)]
        # simula o for /f "tokens=1* delims=|": tudo depois do 1º grupo de "|"
        js = "\n".join(ln.split("|", 1)[1].lstrip("|") for ln in lines)
        self.assertTrue(all(ord(ch) < 128 for ch in js))
        head = json.loads(re.match(r"var UAB = (\{.*\});", js).group(1))
        self.assertEqual(head, {"build": "abc123", "image": "Windows 11 Pro", "minGB": w.MIN_DISK_GB})
        self.assertIn("uab-build:abc123", raw)  # marca usada pelo pe.js para achar o arquivo
        if shutil.which("node"):
            import tempfile
            with tempfile.NamedTemporaryFile("w", suffix=".js", delete=False) as fh:
                fh.write(js)
            try:
                r = subprocess.run(["node", "--check", fh.name], capture_output=True, text=True)
                self.assertEqual(r.returncode, 0, r.stderr)
            finally:
                os.unlink(fh.name)

    def test_arquivos_embutidos(self):
        root = ET.fromstring(render().encode())
        ext = root.find(f"{X}Extensions")
        files = {f.get("path"): f.text.strip() for f in ext.findall(f"{X}File")}
        base = "C:\\ProgramData\\uab\\"
        for n in ("common.ps1", "specialize.ps1", "provision.ps1", "report.ps1", "vpn.ps1", "uab.json", "panel-token",
                  "panel.pub", "remote-meshid"):
            self.assertIn(base + n, files)
        conf = json.loads(files[base + "uab.json"])
        self.assertEqual(conf["apps"], ["Google.Chrome", "7zip.7zip", "Notepad++.Notepad++"])
        self.assertTrue(conf["panel_access"] and conf["remote_agent"])
        self.assertEqual(conf["panel_url"], "https://iso.exemplo.com.br")
        self.assertEqual(files[base + "panel-token"], "T" * 40)
        self.assertEqual(files[base + "panel.pub"], PUB)
        with open(os.path.join(w.SCRIPTS_DIR, "provision.ps1"), encoding="utf-8") as fh:
            self.assertEqual(files[base + "provision.ps1"], fh.read().strip())

    def test_sem_painel_sem_remoto(self):
        xml = render(c=cfg(PANEL_ACCESS="no", REMOTE_AGENT="no", WIN_APPS=""),
                     x=ctx(panel_pubkey="", remote_agent_url="", remote_meshid=""))
        root = ET.fromstring(xml.encode())
        paths = [f.get("path") for f in root.find(f"{X}Extensions").findall(f"{X}File")]
        self.assertFalse(any(p.endswith(("panel.pub", "remote-meshid")) for p in paths))

    def test_chave_vazia_usa_generica(self):
        root = ET.fromstring(render(s=sec(win_key="")).encode())
        self.assertEqual(comp(root, "specialize", "Microsoft-Windows-Shell-Setup").find(f"{U}ProductKey").text,
                         w.GENERIC_PRO_KEY)
        root = ET.fromstring(render(s=sec(win_key="vk7jgnphtmc97jm9mpgt3v66t")).encode())
        self.assertEqual(comp(root, "specialize", "Microsoft-Windows-Shell-Setup").find(f"{U}ProductKey").text,
                         "VK7JG-NPHTM-C97JM-9MPGT-3V66T")

    def test_teclados_e_fusos_do_painel(self):
        for il in ("0416:00010416", "0416:00000416", "0416:00020409"):
            render(c=cfg(INPUT_LOCALE=il))
        for tz in ("E. South America Standard Time", "Bahia Standard Time", "SA Eastern Standard Time",
                   "Tocantins Standard Time", "Central Brazilian Standard Time", "SA Western Standard Time",
                   "SA Pacific Standard Time", "UTC-02", "GMT Standard Time", "UTC"):
            render(c=cfg(TIMEZONE_WIN=tz))


class TestValidacao(unittest.TestCase):
    def bad(self, c=None, s=None, x=None, msg=""):
        with self.assertRaises(ValueError) as cm:
            render(c, s, x)
        if msg:
            self.assertIn(msg, str(cm.exception))

    def test_hostname(self):
        for h in ("", "a" * 16, "-abc", "abc-", "ABC_1", "123", "pc teste", "pç"):
            self.bad(c=cfg(HOSTNAME_=h), msg="Nome do computador")
        render(c=cfg(HOSTNAME_="a" * 15))

    def test_usuario(self):
        for u in ("", "Administrador", "administrator", "1abc", "a" * 21, "user name", "sshd"):
            self.bad(c=cfg(ADMIN_USER=u), msg="Usuário")
        self.bad(c=cfg(ADMIN_USER="recepcao-01"), msg="mesmo nome")

    def test_senha(self):
        for p in ("", "x" * 128, "abc\x00def", "linha\nnova"):
            self.bad(s=sec(password=p), msg="Senha")

    def test_chave(self):
        for k in ("ABCDE-12345", "VK7JG-NPHTM-C97JM-9MPGT-3V66", "AAAAA-AAAAA-AAAAA-AAAAA-AAAAA",
                  "VK7JG-NPHTM-C97JM-9MPGT-3V66T-XXXXX"):
            self.bad(s=sec(win_key=k), msg="Chave do Windows")

    def test_apps(self):
        self.bad(c=cfg(WIN_APPS="Google.Chrome ; rm"), msg="winget")
        self.bad(c=cfg(WIN_APPS=" ".join(f"A.B{i}" for i in range(61))), msg="máximo")

    def test_ctx(self):
        self.bad(x=ctx(report_token="curto"), msg="Token")
        self.bad(x=ctx(panel_url="ftp://x"), msg="painel")
        self.bad(x=ctx(panel_url='https://x"/><a'), msg="painel")
        self.bad(x=ctx(build_id="../x"), msg="build")
        self.bad(x=ctx(panel_pubkey="ssh-ed25519 AAAA\nssh-rsa BBBB"), msg="Chave pública")
        self.bad(x=ctx(panel_pubkey=""), msg="chave pública")
        self.bad(x=ctx(remote_meshid="a b"), msg="Remoto")
        self.bad(x=ctx(remote_agent_url=""), msg="Remoto")

    def test_outros(self):
        self.bad(c=cfg(INPUT_LOCALE="abnt2"), msg="Teclado")
        self.bad(c=cfg(TIMEZONE_WIN="<x>"), msg="Fuso")
        self.bad(c=cfg(DISK_MODE="all"), msg="disco")
        self.bad(c=cfg(OS="ubuntu"))
        self.bad(c=cfg(WIN_VERSION="10"))


def files_of(xml):
    root = ET.fromstring(xml.encode())
    return {f.get("path"): f.text.strip() for f in root.find(f"{X}Extensions").findall(f"{X}File")}


TSKEY = "tskey-auth-kAbCdEf1CNTRL-ZyXwVuTsRqPoNmLkJiHgFeDcBa123456"


class TestVpn(unittest.TestCase):
    base = "C:\\ProgramData\\uab\\"

    def test_desligada_por_padrao(self):
        f = files_of(render())
        conf = json.loads(f[self.base + "uab.json"])
        self.assertFalse(conf["wireguard"])
        self.assertFalse(conf["tailscale"])
        self.assertEqual(conf["tailscale_hostname"], "")
        self.assertNotIn(self.base + "tailscale-authkey", f)
        self.assertIn(self.base + "vpn.ps1", f)  # sempre vai (pequeno); só roda se ligado

    def test_wireguard(self):
        f = files_of(render(c=cfg(WG_ENABLED="yes")))
        conf = json.loads(f[self.base + "uab.json"])
        self.assertTrue(conf["wireguard"])
        # nada de chave: a privada é gerada no PC; a pública vai pelo relatório
        self.assertFalse(any("wg" in p.lower() for p in f if not p.endswith(".ps1") and not p.endswith(".json")))

    def test_tailscale(self):
        xml = render(c=cfg(TAILSCALE_ENABLED="yes", TAILSCALE_HOSTNAME="Recepcao-Tail"), s=sec(tailscale_key=" " + TSKEY + "\n"))
        f = files_of(xml)
        conf = json.loads(f[self.base + "uab.json"])
        self.assertTrue(conf["tailscale"])
        self.assertEqual(conf["tailscale_hostname"], "recepcao-tail")
        self.assertEqual(f[self.base + "tailscale-authkey"], TSKEY)
        self.assertEqual(xml.count(TSKEY), 1)  # só no arquivo da chave (nem no uab.json, nem em comentário)
        self.assertNotIn(TSKEY, f[self.base + "uab.json"])
        # sem nome na Tailnet: usa o nome do computador
        conf = json.loads(files_of(render(c=cfg(TAILSCALE_ENABLED="yes"), s=sec(tailscale_key=TSKEY)))[self.base + "uab.json"])
        self.assertEqual(conf["tailscale_hostname"], "recepcao-01")

    def test_tailscale_ignorado_se_desligado(self):
        f = files_of(render(c=cfg(TAILSCALE_ENABLED="no"), s=sec(tailscale_key=TSKEY)))
        self.assertNotIn(self.base + "tailscale-authkey", f)
        self.assertNotIn(TSKEY, json.dumps(f))

    def test_tailscale_invalido(self):
        for k in ("", "abc", "tskey-", "tskey-auth-x", "tskey-auth-aaaaaaaaaaaa\"/>"):
            with self.assertRaises(ValueError, msg=k) as cm:
                render(c=cfg(TAILSCALE_ENABLED="yes"), s=sec(tailscale_key=k))
            self.assertIn("Tailscale", str(cm.exception))
        for h in ("-x", "a_b", "a" * 64, "tail net"):
            with self.assertRaises(ValueError, msg=h):
                render(c=cfg(TAILSCALE_ENABLED="yes", TAILSCALE_HOSTNAME=h), s=sec(tailscale_key=TSKEY))

    def test_scripts_vpn(self):
        def rd(n):
            with open(os.path.join(w.SCRIPTS_DIR, n), encoding="utf-8") as fh:
                return fh.read()
        vpn, prov, rep, com = rd("vpn.ps1"), rd("provision.ps1"), rd("report.ps1"), rd("common.ps1")
        # instaladores oficiais com a assinatura conferida antes de executar
        self.assertIn("https://download.wireguard.com/windows-client/", vpn)
        self.assertIn("https://pkgs.tailscale.com/stable/", vpn)
        self.assertIn("Get-AuthenticodeSignature", vpn)
        # latest.sig vem como application/octet-stream: precisa decodificar os bytes
        self.assertIn("Get-UabWebText ($base + 'latest.sig')", vpn)
        self.assertIn("WireGuard LLC", vpn)
        self.assertIn("Tailscale Inc", vpn)
        # túnel como serviço do Windows; auth key lida do arquivo (nunca na linha de comando)
        self.assertIn("/installtunnelservice", vpn)
        self.assertIn("--auth-key=file:", vpn)
        self.assertNotIn("--authkey ", vpn)
        # plano único (specialize e provision) e relatório com wg/tailscale/serviços
        for n in ("'openssh'", "'wireguard'", "'tailscale_install'", "'tailscale_login'", "'remote_agent'", "'apps'", "'report'"):
            self.assertIn(n, com)
        self.assertIn("Get-UabPlan", prov)
        self.assertIn("Get-UabPlan", rd("specialize.ps1"))
        self.assertIn("Invoke-UabWgApply", rep)
        self.assertIn("services.wireguard", rep)
        self.assertIn("services.tailscaled", rep)
        self.assertIn("vpn.ps1", w.PS_SCRIPTS)  # extraído com os outros

    @unittest.skipUnless(shutil.which("pwsh"), "pwsh indisponível")
    def test_powershell_sintaxe(self):
        cmd = ("$ok=$true; foreach ($f in Get-ChildItem '%s' -Filter *.ps1) { $t=$null; $e=$null; "
               "[void][System.Management.Automation.Language.Parser]::ParseFile($f.FullName, [ref]$t, [ref]$e); "
               "if ($e) { $ok=$false; $e | %% { \"$($f.Name):$($_.Extent.StartLineNumber): $($_.Message)\" } } }; "
               "if (-not $ok) { exit 1 }") % w.SCRIPTS_DIR
        r = subprocess.run(["pwsh", "-NoProfile", "-Command", cmd], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)


class TestAtivacao(unittest.TestCase):
    base = "C:\\ProgramData\\uab\\"

    def test_sem_chave_nao_ativa(self):
        # chave genérica (instala o Pro sem ativar): nada a ativar
        conf = json.loads(files_of(render(s=sec(win_key="")))[self.base + "uab.json"])
        self.assertFalse(conf["activate"])

    def test_com_chave_ativa(self):
        conf = json.loads(files_of(render(s=sec(win_key="BCDFG-HJKMN-PQRTV-WXY23-46789")))[self.base + "uab.json"])
        self.assertTrue(conf["activate"])
        self.assertNotIn("BCDFG-HJKMN", json.dumps(conf))  # a chave não vai para o uab.json


class TestApps(unittest.TestCase):
    def test_catalogo(self):
        apps = w.load_apps()
        self.assertGreaterEqual(len(apps), 35)
        ids = [a["id"] for a in apps]
        self.assertEqual(len(ids), len(set(ids)))
        for a in apps:
            self.assertEqual(set(a), {"id", "name", "desc", "cat", "icon"})
            self.assertTrue(w.WINGET_ID_RE.match(a["id"]))
            self.assertTrue(a["name"] and a["desc"] and a["cat"] and a["icon"])
            self.assertTrue(re.match(r"^(lucide:[a-z0-9-]+|[a-z0-9]+)$", a["icon"]), a["icon"])


if __name__ == "__main__":
    unittest.main()
