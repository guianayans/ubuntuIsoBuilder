"""Testes do lado do painel para o Windows: validação do assistente (core.validate_windows),
resumo do build (builder.summary_of) e textos da VPN (main._vpn_reason).

Rodar (na raiz do projeto):  python3 -m unittest discover -s windows/tests -v
"""
import atexit
import os
import shutil
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
if "DATA_DIR" not in os.environ:
    os.environ["DATA_DIR"] = tempfile.mkdtemp(prefix="uabtest-data-")
    atexit.register(shutil.rmtree, os.environ["DATA_DIR"], True)
sys.path.insert(0, os.path.join(HERE, "..", "..", "web"))
from app import builder, core  # noqa: E402

TSKEY = "tskey-auth-kAbCdEf1CNTRL-ZyXwVuTsRqPoNmLkJiHgFeDcBa123456"


def form(**kw):
    d = {"os": "windows", "hostname": "recepcao-01", "username": "suporte", "password": "Senha-Forte-123",
         "win_timezone": "E. South America Standard Time", "win_keyboard": "0416:00010416", "panel_access": True,
         "remote_agent": True, "win_apps": "7zip.7zip", "confirm_erase": "APAGAR"}
    d.update(kw)
    return d


class TestValidateWindows(unittest.TestCase):
    def test_sem_vpn(self):
        v = core.validate_build(form())
        self.assertEqual(v["config"]["WG_ENABLED"], "no")
        self.assertEqual(v["config"]["TAILSCALE_ENABLED"], "no")
        self.assertEqual(v["secrets"]["tailscale_key"], "")

    def test_wireguard_e_tailscale(self):
        v = core.validate_build(form(wireguard=True, tailscale=True, tailscale_key=" " + TSKEY, tailscale_hostname="Recep-TS"))
        c = v["config"]
        self.assertEqual((c["WG_ENABLED"], c["TAILSCALE_ENABLED"], c["TAILSCALE_HOSTNAME"]), ("yes", "yes", "recep-ts"))
        self.assertEqual(v["secrets"]["tailscale_key"], TSKEY)
        self.assertNotIn(TSKEY, str(c))  # segredo fora da configuração
        s = builder.summary_of(c)
        self.assertIn('"WG_ENABLED": "yes"', s)  # o agent_report depende disto
        self.assertIn('"TAILSCALE_ENABLED": "yes"', s)
        self.assertNotIn(TSKEY, s)
        # nome na Tailnet padrão = nome do computador
        self.assertEqual(core.validate_build(form(tailscale=True, tailscale_key=TSKEY))["config"]["TAILSCALE_HOSTNAME"],
                         "recepcao-01")

    def test_tailscale_invalido(self):
        for k in ("", "abc", "tskey-auth-x"):
            with self.assertRaises(core.Invalid):
                core.validate_build(form(tailscale=True, tailscale_key=k))
        with self.assertRaises(core.Invalid):
            core.validate_build(form(tailscale=True, tailscale_key=TSKEY, tailscale_hostname="a_b"))

    def test_gera_xml_com_vpn(self):
        wc = core.winconfig()
        v = core.validate_build(form(wireguard=True, tailscale=True, tailscale_key=TSKEY))
        ctx = {"build_id": "abc123", "panel_url": "https://iso.exemplo.com.br", "report_token": "T" * 40,
               "panel_pubkey": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq7l8tJ1c0Zb1pX0mYlT1y0n0m2VZ3N8b4C1Q2w3e4r x",
               "remote_agent_url": "https://iso.exemplo.com.br/remoto", "remote_meshid": "a" * 40}
        xml = wc.render_autounattend(dict(v["config"]), dict(v["secrets"]), ctx)
        self.assertIn('"wireguard": true', xml)
        self.assertIn("tailscale-authkey", xml)


class TestMachineRow(unittest.TestCase):
    def test_tailscale_sem_login(self):
        # Windows com o Tailscale sem login mandava "ips": [null] (e a lista de máquinas quebrava)
        import json
        from app import panel
        rep = {"platform": "windows", "tailscale": {"state": "NeedsLogin", "ips": [None], "dns": "", "hostname": "PC"},
               "wg": {"address": "10.77.1.1/32"}}
        row = {"id": "x", "build_id": "b", "hostname": "pc", "label": "", "notes": "", "first_seen": 0, "last_seen": 0,
               "off_at": None, "off_reason": "", "ssh_host": "", "ssh_port": 22, "source_ip": "", "report": json.dumps(rep)}
        m = panel.machine_row(row)
        self.assertEqual((m["tailscale_ip"], m["platform"], m["wg_ip"]), ("", "windows", "10.77.1.1"))
        with self.assertRaises(LookupError):
            panel.ssh_target(row)


class TestVpnReason(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        os.environ.setdefault("HUB_ENDPOINT", "hub.exemplo.com:51822")
        try:
            from app import main
        except Exception as exc:  # noqa: BLE001
            raise unittest.SkipTest(f"painel não importa aqui: {exc}")
        cls.main = main

    def m(self, **kw):
        d = {"wg": {"connected": False, "last_handshake": 0}, "online": True, "first_seen": time.time() - 3600,
             "platform": "windows", "report": {"wg": {"state": "erro", "message": "x"}}}
        d.update(kw)
        return d

    def test_textos_windows(self):
        hub = {"online": True, "peers": {}}
        r, _ = self.main._vpn_reason(self.m(), hub)
        self.assertTrue(r.startswith("O PC recusou"), r)
        r, _ = self.main._vpn_reason(self.m(report={"wg": {"state": "aguardando", "message": "hub offline"}}), hub)
        self.assertIn("no PC", r)
        r, _ = self.main._vpn_reason(self.m(platform="linux"), hub)
        self.assertTrue(r.startswith("O servidor recusou"), r)
        r, n = self.main._vpn_reason(self.m(wg={"connected": True}, report={"wg": {"state": "ativo", "message": "nota"}}), hub)
        self.assertEqual((r, n), ("", "nota"))


if __name__ == "__main__":
    unittest.main()
