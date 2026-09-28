# Windows 11 no ISO Builder — contrato entre as partes (Fase 1)

Objetivo: o técnico escolhe **Windows 11 Pro** no assistente do painel; o painel gera só a
**configuração** (um `autounattend.xml`, poucos KB, com tudo embutido); o cliente baixa o
gravador **isoBuilder**, que **baixa a ISO oficial da Microsoft** (ou usa uma ISO local),
grava o pendrive com a configuração injetada; o PC se instala sozinho, aparece em
**Máquinas** (com o logo do Windows) e no **Remoto** (agente MeshCentral), com os apps
escolhidos instalados via **winget**.

O painel **nunca** hospeda nem distribui a ISO do Windows (licença da Microsoft).

Três partes, cada uma com seus arquivos (não editar arquivos da outra parte):

| Parte | Dono dos arquivos |
|---|---|
| A. Configuração do Windows | `windows/**`, `web/app/winconfig.py`, `docs/windows/PAYLOAD.md` |
| B. Gravador (Go) | `helper/**`, `docs/windows/WRITER.md` |
| C. Painel | `web/app/main.py`, `core.py`, `builder.py`, `panel.py`, `web/static/**`, `web/tools/make-pkg-icons.py`, `docs/windows/PANEL.md` |

## 1. Configuração validada (painel → gerador)

`core.validate_build(d)` devolve, para Windows, um dict `cfg` com:

```
OS            = "windows"
WIN_VERSION   = "11"
WIN_EDITION   = "Pro"
WIN_LANG      = "pt-BR"                      # idioma da ISO a baixar e da instalação
HOSTNAME_     = "recepcao-01"                # 1–15 caracteres [a-z0-9-] (limite NetBIOS)
ADMIN_USER    = "suporte"                    # usuário local administrador (core.USER_RE)
REALNAME      = "Suporte"                    # opcional
TIMEZONE_WIN  = "E. South America Standard Time"
INPUT_LOCALE  = "0416:00010416"              # teclado ABNT2
UI_LANG       = "pt-BR"
PANEL_ACCESS  = "yes" | "no"                 # OpenSSH + chave do painel (terminal no navegador)
REMOTE_AGENT  = "yes" | "no"                 # agente MeshCentral (Remoto)
WIN_APPS      = "Google.Chrome 7zip.7zip ..." # IDs do winget, separados por espaço
DISK_MODE     = "largest"                    # único modo na Fase 1 (ver §4)
```

Segredos (fora do `cfg`, passados à parte): `password` (texto), `win_key` (chave do
Windows; vazio = chave genérica de instalação do Pro, sem ativação:
`VK7JG-NPHTM-C97JM-9MPGT-3V66T`).

## 2. Gerador (parte A) — API usada pelo painel

```python
# web/app/winconfig.py
def render_autounattend(cfg: dict, secrets: dict, ctx: dict) -> str:
    """Devolve o autounattend.xml completo (UTF-8), autocontido."""
# secrets = {"password": str, "win_key": str}
# ctx = {"build_id": str, "panel_url": str, "report_token": str,
#        "panel_pubkey": str | "", "remote_agent_url": str | "", "remote_meshid": str | ""}

def load_apps() -> list[dict]:
    """Catálogo de apps do Windows (windows/apps.json)."""
```

`windows/apps.json` — lista de objetos:
```json
{"id": "Google.Chrome", "name": "Google Chrome", "desc": "navegador", "cat": "Navegadores", "icon": "googlechrome"}
```
`icon` = slug do Simple Icons ou `"lucide:<nome>"` (a parte C gera os ícones embutidos).
Todos os IDs precisam existir no repositório `winget` (conferir).

**Autocontido:** os scripts PowerShell vão **dentro** do `autounattend.xml` e são extraídos
durante a instalação (ex.: um comando no passo *specialize* lê
`C:\Windows\Panther\unattend.xml` e grava os scripts em `C:\ProgramData\uab\`). Assim o mesmo
arquivo funciona no pendrive do isoBuilder **e** no Ventoy (plugin `auto_install`), sem
`$OEM$`.

## 3. Endereços do painel (parte C)

- `GET /download/bundle/{bid}/{platform}` — como hoje; para build Windows o JSON embutido é:
  ```json
  {"kind": "windows", "server": "https://iso.exemplo", "token": "<32+ chars>",
   "config_name": "<host>-autounattend.xml", "sha256": "<64 hex>", "size": 12345,
   "hostname": "<host>", "expires_at": 1234567890,
   "windows": {"version": "11", "edition": "Pro", "lang": "pt-BR", "arch": "x64"}}
  ```
  (build Ubuntu: igual a hoje, sem `kind` ou com `"kind": "ubuntu"`.)
- `GET /t/{token}/config` — devolve o `autounattend.xml` (só para build Windows; 410 se
  expirado). `GET /t/{token}/iso` continua só para Ubuntu.
- `builds` ganha a coluna `os TEXT NOT NULL DEFAULT 'ubuntu'`. Build Windows: `series = "11"`,
  `iso_path` aponta para o `autounattend.xml` gerado (0600), `iso_size`/`sha256` dele.
  Gera em segundos (sem fila de ISO). Apagado pelas mesmas regras de validade.

## 4. Instalação (parte A)

- **Disco:** apagar e instalar no **maior disco interno que não seja USB**; nunca o pendrive.
  Se não houver disco elegível, parar com mensagem clara (não apagar nada).
- UEFI (GPT); contornar exigências de TPM/Secure Boot/RAM (LabConfig) para PCs antigos.
- Sem conta Microsoft (BypassNRO / usuário local), sem telas de OOBE, idioma e teclado.
- Nome do computador = `HOSTNAME_`; usuário local admin com a senha; chave do Windows.
- Pós-instalação (tarefa agendada como SYSTEM, repete até concluir, sobrevive a reboot):
  1. `openssh` — OpenSSH Server (capacidade do Windows; precisa de internet), serviço
     automático, shell padrão PowerShell, chave do painel em
     `C:\ProgramData\ssh\administrators_authorized_keys` com a ACL correta (se PANEL_ACCESS).
  2. `remote_agent` — agente MeshCentral do grupo `remote_meshid` baixado de
     `remote_agent_url` (se REMOTE_AGENT).
  3. `apps` — cada ID de `WIN_APPS` via winget (silencioso, aceitando acordos); ID que falhar
     é registrado e não trava os outros.
  4. `report` — tarefa "UAB Report" a cada 10 min e no boot.
  Estado em `C:\ProgramData\uab\status.json` (etapas `ok` / `falhou <data>: <motivo>`).

## 5. Relatório do Windows (parte A → painel)

`POST {panel_url}/api/agent/report`, `Authorization: Bearer <report_token>`, mesmo JSON do
Linux (`payload/uab-report.sh`), com:
- `machine_id`: 32 hex minúsculos (MachineGuid sem hífens);
- `hw_id`: `sha256("uab-hw:smbios:" + <UUID SMBIOS em minúsculas>)`, mesma regra do Linux
  (UUID genérico/vazio → MAC da 1ª placa física: `"uab-hw:mac:" + mac minúsculo com ":"`);
- `hostname` em minúsculas; `os` = `Caption` do Windows (ex.: "Microsoft Windows 11 Pro");
- `platform: "windows"`; `kernel` = versão/build; `admin_user`; `ssh_auth: "key"`; `ssh_port: 22`;
- `local_ips` no formato `"<nome da interface> <ip>/<prefixo>"`;
- `services` {`ssh`, `meshagent`}; `provisioned`, `provision_plan`, `provision_current`,
  `steps` (mesmo sentido do Linux); `resources` (cpus, mem_total, mem_available, disk_total,
  disk_free); `uptime_seconds`.

## 6. Gravador (parte B)

- Lê o JSON embutido; se `kind == "windows"`: baixa a configuração de `/t/{token}/config`
  (confere SHA256), obtém a ISO do Windows 11 `lang`/`arch` **direto da Microsoft** (mesmo
  fluxo público do site de download, como o Fido/Rufus) ou, se falhar, deixa o usuário
  escolher uma ISO já baixada (conferir que é Windows 11 e tem a edição).
- Grava o pendrive: tabela MBR ou GPT com partição **FAT32** que o UEFI inicializa, arquivos
  da ISO copiados, `sources/install.wim` > 4 GB **dividido** em `install.swm`,
  `install2.swm`… (o Setup do Windows aceita), `autounattend.xml` na raiz.
- Pendrive com **Ventoy**: copia a ISO do Windows e configura o plugin `auto_install` com o
  `autounattend.xml` (em `ventoy/ventoy.json`).
- Continua valendo tudo do modo Ubuntu (lista de pendrives, conferência, "Reiniciar agora",
  partição/pasta INSTALAR quando fizer sentido).

## 7. Regras para todas as partes

- Nunca alterar nada fora de `/pendriver/ubuntu-autoinstall-builder` (exceto arquivos
  temporários e containers/VMs de teste descartáveis, removidos ao final).
- Nunca mexer em `/pendriver/remote`, no proxy, nos containers de produção nem fazer deploy.
- Não fazer `git commit`/`push`. Não imprimir segredos.
- Arquivos grandes de teste em `/pendriver/ubuntu-autoinstall-builder/work/win/`
  (ignorado pelo git). **Disco compartilhado com a produção:** antes de criar arquivo grande,
  conferir `df`; **nunca deixar menos de 10 GB livres**; apagar imagens/VMs ao terminar.
  A ISO do Windows de teste fica em `work/win/iso/` e é compartilhada entre as partes.
- Textos para o usuário em português do Brasil.
