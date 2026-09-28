#!/usr/bin/env python3
"""ubuntu-autoinstall-builder — aplica o túnel WireGuard até o hub (seu servidor).

Recebe (stdin) a resposta do painel ao relatório. Se ela trouxer "wg" com a
configuração do túnel, valida TUDO (o servidor não confia cegamente no painel),
grava /etc/wireguard/uabwg.conf e liga o wg-quick@uabwg (sobe sozinho no boot).
A chave privada é gerada neste servidor e nunca sai dele.

Opcional: "acesso à rede do cliente" — mapeia a LAN local (/24) para
10.200.<slot>.0/24 com NETMAP, para você alcançar os equipamentos da rede
local sem conflitar com redes iguais de outros clientes.
"""
import ipaddress
import json
import os
import re
import subprocess
import sys
import time

IFACE = "uabwg"
CONF = f"/etc/wireguard/{IFACE}.conf"
KEYDIR = "/etc/uab/wg"
STATUS = "/var/lib/uab/wg-status.json"
KEY_RE = re.compile(r"^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$")
EP_RE = re.compile(r"^[A-Za-z0-9.-]{1,253}:\d{2,5}$")


def sh(*cmd, check=False):
    return subprocess.run(cmd, capture_output=True, text=True, check=check)


def save_status(**kw):
    os.makedirs(os.path.dirname(STATUS), exist_ok=True)
    try:
        old = json.load(open(STATUS))
    except (OSError, ValueError):
        old = {}
    old.update(kw, updated=int(time.time()))
    json.dump(old, open(STATUS, "w"))


def local_nets():
    out = []
    for a in json.loads(sh("ip", "-j", "-4", "addr").stdout or "[]"):
        if a.get("ifname") == IFACE:
            continue
        for i in a.get("addr_info", []):
            out.append((a["ifname"], ipaddress.ip_interface(f"{i['local']}/{i['prefixlen']}").network))
    return out


def fail(msg):
    save_status(state="erro", message=msg)
    print("uab-wg:", msg)
    sys.exit(0)


def main():
    try:
        resp = json.loads(sys.stdin.read() or "{}")
    except ValueError:
        return
    cfg = resp.get("wg")
    if not isinstance(cfg, dict):
        return
    if "address" not in cfg:
        save_status(state="aguardando", message=str(cfg.get("pending") or cfg.get("error") or "aguardando o painel"))
        return

    # ---- validação estrita
    try:
        net = ipaddress.ip_network(cfg.get("net", "10.77.0.0/16"))
        lanmap_net = ipaddress.ip_network(cfg.get("lanmap_net", "10.200.0.0/16"))
        addr = ipaddress.ip_interface(cfg["address"])
        allowed = [ipaddress.ip_network(x) for x in cfg["allowed_ips"]]
    except (KeyError, ValueError, TypeError):
        fail("configuração WireGuard inválida recebida do painel")
    if addr.ip not in net or addr.network.prefixlen != 32:
        fail("endereço WireGuard fora da rede da VPN")
    if not all(a.subnet_of(net) for a in allowed):
        fail("rotas WireGuard fora da rede da VPN")
    if not KEY_RE.match(str(cfg.get("hub_pubkey", ""))) or not EP_RE.match(str(cfg.get("endpoint", ""))):
        fail("chave/endereço do hub inválidos")
    keepalive = int(cfg.get("keepalive", 25))
    for ifn, n in local_nets():
        if n.overlaps(net) or n.overlaps(lanmap_net):
            fail(f"a rede local {n} ({ifn}) conflita com a VPN {net}/{lanmap_net}; túnel não aplicado")

    # Acesso à rede local: problema nele NUNCA derruba o túnel (só essa opção fica de fora).
    # Se a rede mudou de placa (ex.: a ponte br0 da Virtualização pegou o IP da enp3s0),
    # usa a placa que tem a rede agora.
    lan = cfg.get("lan") if isinstance(cfg.get("lan"), dict) else None
    lan_note = ""
    post_up, post_down = [], []
    if lan:
        try:
            lcidr = ipaddress.ip_network(lan["cidr"])
            lmap = ipaddress.ip_network(lan["map"])
            lif = str(lan["iface"])
            assert lcidr.prefixlen == 24 and lmap.prefixlen == 24 and lmap.subnet_of(lanmap_net)
        except (KeyError, ValueError, AssertionError):
            lan, lan_note = None, "acesso à rede local ignorado: configuração inválida"
    if lan:
        holders = [ifn for ifn, n in local_nets() if n == lcidr and re.match(r"^[A-Za-z0-9._-]{1,15}$", ifn)]
        if lif not in holders:
            if holders:
                lan_note = f"rede local {lcidr} agora está na interface {holders[0]} (era {lif})"
                lif = holders[0]
            else:
                lan, lan_note = None, f"acesso à rede local desligado: a rede {lcidr} não existe mais neste servidor"
        if lan_note:
            print("uab-wg:", lan_note)
    if lan:
        rules = [
            f"iptables -t nat -A PREROUTING -i %i -d {lmap} -j NETMAP --to {lcidr}",
            f"iptables -t nat -A POSTROUTING -s {net} -d {lcidr} -o {lif} -j MASQUERADE",
            f"iptables -I FORWARD 1 -i %i -o {lif} -d {lcidr} -j ACCEPT",
            f"iptables -I FORWARD 1 -i {lif} -o %i -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
        ]
        post_up = ["sysctl -qw net.ipv4.ip_forward=1"] + rules
        post_down = [r.replace(" -A ", " -D ").replace(" -I FORWARD 1 ", " -D FORWARD ") for r in rules]

    priv_path = os.path.join(KEYDIR, "private.key")
    if not os.path.exists(priv_path):
        fail("chave privada WireGuard ausente")
    priv = open(priv_path).read().strip()
    lines = ["# gerado por ubuntu-autoinstall-builder (uab-wg-apply). Não edite.", "[Interface]",
             f"PrivateKey = {priv}", f"Address = {addr}"]
    lines += [f"PostUp = {c}" for c in post_up] + [f"PostDown = {c}" for c in post_down]
    lines += ["", "[Peer]", f"PublicKey = {cfg['hub_pubkey']}", f"Endpoint = {cfg['endpoint']}",
              f"AllowedIPs = {', '.join(str(a) for a in allowed)}", f"PersistentKeepalive = {keepalive}", ""]
    new = "\n".join(lines)

    old = open(CONF).read() if os.path.exists(CONF) else ""
    os.makedirs("/etc/wireguard", mode=0o700, exist_ok=True)
    if new != old:
        fd = os.open(CONF + ".new", os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w") as fh:
            fh.write(new)
        os.replace(CONF + ".new", CONF)
        sh("systemctl", "enable", f"wg-quick@{IFACE}")
        r = sh("systemctl", "restart", f"wg-quick@{IFACE}")
        print("uab-wg: túnel (re)aplicado", "OK" if r.returncode == 0 else f"FALHOU: {r.stderr.strip()}")
    elif sh("systemctl", "is-active", f"wg-quick@{IFACE}").stdout.strip() != "active":
        sh("systemctl", "enable", f"wg-quick@{IFACE}")
        sh("systemctl", "restart", f"wg-quick@{IFACE}")

    # Sem handshake recente: reresolve o DNS do hub (IP de casa pode ter mudado).
    hs = 0
    out = sh("wg", "show", IFACE, "latest-handshakes").stdout.split()
    if len(out) >= 2 and out[1].isdigit():
        hs = int(out[1])
    if not hs or time.time() - hs > 180:
        sh("wg", "set", IFACE, "peer", cfg["hub_pubkey"], "endpoint", cfg["endpoint"])
    save_status(state="ativo" if hs and time.time() - hs < 180 else "conectando", address=str(addr),
                handshake=hs, lan=str(lan["map"]) if lan else "", message=lan_note)


if __name__ == "__main__":
    main()
