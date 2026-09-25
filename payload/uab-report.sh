#!/bin/bash
# ubuntu-autoinstall-builder — relatório ao painel ("estou vivo").
# Envia ao painel (iso.gvtserver.online ou o que gerou a ISO) o estado desta
# máquina: IPs, Tailscale, provisionamento, serviços e recursos. Sem segredos.
# Executado pelo uab-report.timer (no boot e a cada 10 min) e ao fim do
# provisionamento. Uso manual: sudo uab-report [-v]
set -uo pipefail

CONF=/etc/uab/uab.conf
TOKEN_FILE=/etc/uab/panel-token
[ -r "$CONF" ] || exit 0
# shellcheck disable=SC1090
. "$CONF"
[ -n "${UAB_PANEL_URL:-}" ] && [ -s "$TOKEN_FILE" ] || exit 0
VERBOSE=0; [ "${1:-}" = -v ] && VERBOSE=1

# Chamado pelo uab-report-shutdown.service ao desligar/reiniciar: aviso curto para
# o painel marcar a máquina como desligada na hora (sem isso, leva até 25 min).
if [ "${1:-}" = --shutdown ]; then
  reason=poweroff
  systemctl list-jobs --no-legend 2>/dev/null | grep -qE '(reboot|kexec)\.target' && reason=reboot
  printf '{"machine_id":"%s","reason":"%s"}' "$(cat /etc/machine-id)" "$reason" \
    | curl -fsS -o /dev/null --max-time 6 -X POST -H "X-UAB-Event: shutdown" \
        -H "Authorization: Bearer $(cat "$TOKEN_FILE")" -H "Content-Type: application/json" \
        --data-binary @- "$UAB_PANEL_URL/api/agent/report" || true
  exit 0
fi

payload="$(python3 - <<'PY'
import json, os, re, shutil, socket, subprocess

def sh(*cmd, timeout=10):
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout).stdout.strip()
    except Exception:
        return ""

def active(unit):
    return sh("systemctl", "is-active", unit) == "active"

conf = {}
for line in open("/etc/uab/uab.conf"):
    m = re.match(r"^([A-Z_]+)=(.*)$", line.strip())
    if m:
        conf[m.group(1)] = m.group(2).strip("'")

ts = {}
try:
    d = json.loads(sh("tailscale", "status", "--json") or "{}")
    me = d.get("Self") or {}
    ts = {"state": d.get("BackendState", ""), "ips": me.get("TailscaleIPs") or [],
          "dns": (me.get("DNSName") or "").rstrip("."), "hostname": me.get("HostName", "")}
except Exception:
    pass

local = []
try:
    for a in json.loads(sh("ip", "-j", "-4", "addr", "show", "scope", "global") or "[]"):
        if a.get("ifname", "").startswith(("tailscale", "docker", "br-", "veth")):
            continue
        for i in a.get("addr_info", []):
            local.append(f"{a['ifname']} {i['local']}/{i['prefixlen']}")
except Exception:
    pass

steps = {}
try:
    for line in open("/var/lib/uab/status"):
        k, _, v = line.strip().partition("=")
        steps[k] = v
except OSError:
    pass

osname = ""
try:
    osname = re.search(r'PRETTY_NAME="([^"]+)"', open("/etc/os-release").read()).group(1)
except Exception:
    pass

mem = {}
try:
    for line in open("/proc/meminfo"):
        k, v = line.split(":")
        mem[k] = int(v.split()[0]) * 1024
except Exception:
    pass
du = shutil.disk_usage("/")

wg = None
if conf.get("UAB_WG") == "yes":
    wg = {}
    try:
        wg["pubkey"] = open("/etc/uab/wg/public.key").read().strip()
    except OSError:
        pass
    try:
        wg.update({k: v for k, v in json.load(open("/var/lib/uab/wg-status.json")).items()
                   if k in ("state", "address", "handshake", "lan", "message")})
    except (OSError, ValueError):
        pass
def ssh_port():
    """Porta em que o SSH escuta de fato (a escolhida na ISO só vale após o provisionamento)."""
    os.makedirs("/run/sshd", mode=0o755, exist_ok=True)
    for line in sh("sshd", "-T").splitlines():
        if line.startswith("port ") and line.split()[1].isdigit():
            return int(line.split()[1])
    return int(conf.get("UAB_SSH_PORT") or 22)


load = os.getloadavg()

print(json.dumps({
    "machine_id": open("/etc/machine-id").read().strip(),
    "hostname": socket.gethostname(),
    "build_id": conf.get("UAB_BUILD_ID", ""),
    "admin_user": conf.get("UAB_ADMIN_USER", ""),
    "ssh_auth": conf.get("UAB_SSH_AUTH", "key"),
    "ssh_port": ssh_port(),
    "os": osname,
    "kernel": os.uname().release,
    "uptime_seconds": int(float(open("/proc/uptime").read().split()[0])),
    "tailscale": ts,
    "local_ips": local,
    "gateway": sh("sh", "-c", "ip route show default | awk '{print $3; exit}'"),
    "provisioned": steps.pop("provisioned", ""),
    "steps": steps,
    "services": {"ssh": active("ssh.socket") or active("ssh.service"), "tailscaled": active("tailscaled"),
                 "docker": active("docker"), "fail2ban": active("fail2ban"),
                 "cockpit": active("cockpit.socket"),
                 "coolify": sh("docker", "inspect", "-f", "{{.State.Running}}", "coolify") == "true" if shutil.which("docker") else False,
                 "uab-provision": active("uab-provision"),
                 **({"ddclient": active("ddclient")} if conf.get("UAB_DDNS") == "yes" else {}),
                 **({"cloudflared": active("cloudflared")} if conf.get("UAB_CLOUDFLARE") == "yes" else {}),
                 **({"libvirtd": active("libvirtd")} if conf.get("UAB_VIRT") == "yes" else {}),
                 **({"meshagent": active("meshagent")} if conf.get("UAB_REMOTE_AGENT") == "yes" else {}),
                 **({"nvidia": sh("nvidia-smi", "-L") != ""} if conf.get("UAB_GPU") == "yes" and shutil.which("nvidia-smi") else {})},
    "docker_containers": len([l for l in sh("docker", "ps", "-q").splitlines() if l]) if shutil.which("docker") else None,
    "wg": wg,
    "resources": {"cpus": os.cpu_count(), "load1": round(load[0], 2), "mem_total": mem.get("MemTotal"),
                  "mem_available": mem.get("MemAvailable"), "disk_total": du.total, "disk_free": du.free},
}))
PY
)" || exit 0

resp="$(curl -fsS --max-time 20 --retry 2 -X POST \
  -H "Authorization: Bearer $(cat "$TOKEN_FILE")" -H "Content-Type: application/json" \
  --data-binary @- "$UAB_PANEL_URL/api/agent/report" <<<"$payload" 2>&1)"
rc=$?
[ "$VERBOSE" = 1 ] && { echo "$payload"; echo "resposta: $resp (código $rc)"; }
mkdir -p /var/lib/uab
if [ $rc = 0 ]; then
  date '+%F %T' >/var/lib/uab/last-report
  # túnel WireGuard até o hub: a resposta do painel traz a configuração
  if [ "${UAB_WG:-no}" = yes ] && [ -x /usr/local/sbin/uab-wg-apply ]; then
    printf '%s' "$resp" | /usr/local/sbin/uab-wg-apply
  fi
fi
exit 0
