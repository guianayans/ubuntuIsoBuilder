# Windows 11 — configuração da instalação (parte A)

Arquivos desta parte: `web/app/winconfig.py`, `windows/apps.json`, `windows/scripts/*`,
`windows/tests/*`. Contrato com as outras partes: [CONTRACT.md](CONTRACT.md).

## Visão geral

O painel chama `winconfig.render_autounattend(cfg, secrets, ctx)` e grava o
`autounattend.xml` (≈55 KB). Esse único arquivo leva **tudo**: configuração do Setup,
scripts e dados. Não há `$OEM$` nem arquivos extras no pendrive — o mesmo arquivo serve
para o pendrive do isoBuilder (raiz da partição) e para o Ventoy (plugin `auto_install`).

```
autounattend.xml
├─ windowsPE    idioma/teclado; LabConfig (TPM/Secure Boot/RAM/CPU/disco);
│               comandos do cmd que extraem e rodam o pe.js  ─────────────┐
├─ specialize   nome do PC, chave, fuso, BypassNRO; PowerShell extrai      │
│               C:\ProgramData\uab\* e roda specialize.ps1 (tarefas)       │
├─ oobeSystem   OOBE sem telas, usuário local administrador, fuso          │
└─ <Extensions xmlns="urn:uab:windows" build="uab-build:<id>">  (ignorado pelo Setup)
     <File path="C:\ProgramData\uab\common.ps1|specialize.ps1|provision.ps1|report.ps1|vpn.ps1">
     <File path="...\uab.json">        configuração (sem segredos)
     <File path="...\panel-token">     token do relatório
     <File path="...\panel.pub">       chave pública do painel (se acesso pelo painel)
     <File path="...\remote-meshid">   grupo do MeshCentral (se Remoto)
     <File path="...\tailscale-authkey"> auth key do Tailscale (se Tailscale; apagada após o login)
     <Extract>  código PowerShell que grava os <File> (roda via iex no specialize)
     <PE>  linhas "UABPE|<js>" (CDATA)  ◄──────────────────────────────────┘
```

## 1. Windows PE: escolha do disco e instalação (`windows/scripts/pe.js`)

O WinPE do Setup **não tem PowerShell nem findstr** (conferido no `boot.wim` do 25H2), mas
tem `cmd`, `find`, `cscript` (JScript), WMI (`Win32_DiskDrive`), `diskpart`, `dism`,
`bcdboot`, `wpeutil` e `notepad`. Por isso:

1. Comandos `RunSynchronous` do passo windowsPE procuram o arquivo de respostas
   (`<letra>:\autounattend.xml`, `<letra>:\unattend.xml` e o valor
   `HKLM\SYSTEM\Setup\UnattendFile`, que o Ventoy usa) e, com `find "UABPE|"`, copiam as
   linhas do `pe.js` para `X:\uab\raw.txt`; um `for /f` tira o prefixo e grava
   `X:\uab\pe.js`, que roda no `cscript`.
   - No arquivo, o `|` do marcador nos próprios comandos está como `&#124;`: assim o texto
     cru só tem `UABPE|` nas linhas do script.
   - O gerador troca caracteres não ASCII do JS por `\uXXXX` (passam intactos pelo cmd).
   - **Todo comando termina com `& exit 0`**: o Setup novo (24H2/25H2) aborta com
     `0x80070001 - 0x40030` se um comando sair com código ≠ 0 (visto no teste em VM).
2. O `pe.js`:
   - acha a mídia (`sources\install.wim`, `.esd` ou `install.swm` + `install*.swm`) e o
     arquivo de respostas **deste build** (atributo `uab-build:<id>`);
   - lista os discos pelo WMI e **ignora**: interface USB/1394, `USBSTOR`/`UASPSTOR`, mídia
     removível/externa, o disco que contém a mídia de instalação ou o arquivo de respostas e
     discos com menos de 24 GB. Escolhe o **maior** restante (empate: menor número);
   - se já existir `\ProgramData\uab\build-id` com este build em alguma unidade (o PC voltou
     a iniciar pelo pendrive depois de instalar), mostra um aviso e só apaga se o técnico
     fechar a janela — evita reinstalar em laço;
   - descobre o firmware: `PEFirmwareType` do registro (1 = BIOS, 2 = UEFI); se a leitura
     falhar, pela saída de `bcdedit /enum {current}` (carregador do WinPE terminando em `.efi`
     = UEFI, `.exe` = BIOS); se ainda assim for desconhecido, usa **MBR** (disco < 2 TB; senão
     GPT) com a partição de sistema em **FAT32** ativa e `bcdboot /f ALL`, que inicia nos dois
     (o UEFI só lê FAT; o BIOS inicia da partição ativa). Quando a leitura funciona, nada muda;
   - particiona com `diskpart` (UEFI: GPT com EFI 300 MB + MSR 16 MB + Windows; BIOS: MBR
     com partição de sistema de 500 MB ativa + Windows), aplica a edição
     `Windows 11 <edição>` com `dism /Apply-Image` (com `/SWMFile` quando dividido), cria o
     boot com o `bcdboot` da própria imagem (`/f UEFI`, `/f BIOS` ou `/f ALL`), copia o arquivo de respostas para
     `<Windows>\Panther\unattend.xml`, grava `ProgramData\uab\build-id` e `logs\pe.log`,
     e reinicia (`wpeutil reboot`).
   - **Sem disco elegível** (ou qualquer erro): mostra o motivo no Bloco de Notas e, ao
     fechar, devolve o controle ao Setup normal, que pede o disco ao técnico (o XML não tem
     `DiskConfiguration`/`ImageInstall`, então o Setup nunca escolhe disco sozinho).
     Nada é apagado antes de haver um disco elegível.

Por que aplicar a imagem “à mão” em vez de deixar o Setup instalar: o `InstallTo/DiskID`
do XML é fixo e não dá para escolher “o maior disco não-USB” com ele; aplicar com
`dism` + `bcdboot` é o mesmo que o Setup faz e os passos specialize/oobeSystem continuam
normalmente a partir de `Panther\unattend.xml`. Efeito colateral bom: as checagens de
TPM/Secure Boot/CPU/RAM do Setup nem rodam (o LabConfig fica para o caminho interativo).

## 2. specialize (`specialize.ps1`)

Um comando PowerShell curto carrega `%WINDIR%\Panther\unattend.xml` e roda (`iex`) o
código do elemento `<Extract>`, que grava cada `<File>` (scripts `.ps1` em UTF-8 com BOM,
que o PowerShell 5.1 exige para acentos) e roda `specialize.ps1`. **Todo `Path` de
`RunSynchronous` tem no máximo 259 caracteres**: acima disso o Setup recusa o arquivo
inteiro no specialize (“O arquivo de resposta é inválido”, visto no teste) — o gerador
verifica isso.

- `C:\ProgramData\uab` só para SYSTEM e Administradores (ACL por SID, independe do idioma;
  aplicada só na pasta com `(OI)(CI)`, e os arquivos herdam com `icacls /reset` — aplicar
  `(OI)(CI)` com `/T` em arquivo deixa o arquivo sem permissão nem para o SYSTEM);
- `BypassNRO`;
- tarefas agendadas (como SYSTEM, `S-1-5-18`):
  - **UAB Provision** — no boot (+1 min) e a cada 5 min; `IgnoreNew`; desativa-se ao concluir;
  - **UAB Report** — no boot e a cada 10 min;
  - **UAB Shutdown** — no evento User32 1074 (desligar/reiniciar), avisa o painel.

## 3. Pós-instalação (`provision.ps1`)

Espera o OOBE terminar (`HKLM\SYSTEM\Setup`: `SystemSetupInProgress=0`, `OOBEInProgress≠1`
e o usuário existir); então **apaga `Panther\unattend.xml`** (tem a senha ofuscada e o
token) e roda as etapas; etapa concluída não repete; falha fica registrada e é tentada de
novo na próxima execução (5 min ou próximo boot):

| etapa | o que faz |
|---|---|
| `openssh` (se acesso pelo painel) | capacidade `OpenSSH.Server` (Windows Update); se falhar, `winget Microsoft.OpenSSH.Preview`. Serviço automático, `DefaultShell` = PowerShell, chave do painel em `C:\ProgramData\ssh\administrators_authorized_keys` (ACL: só Administradores e SYSTEM, sem herança), `PasswordAuthentication no` (só chave, como no Ubuntu), regra de firewall porta 22 em todos os perfis. |
| `remote_agent` (se Remoto) | baixa `<remote_agent_url>/meshagents?id=4&meshid=<id>&installflags=0` (x64; `id=3` em 32 bits) — o MeshCentral devolve o `MeshService64.exe` já com a configuração do grupo embutida — e roda `-fullinstall` (serviço “Mesh Agent”). Apaga o `remote-meshid` depois. |
| `apps` | cada ID via winget (`--exact --source winget --silent --accept-package-agreements --accept-source-agreements --disable-interactivity`, primeiro `--scope machine`, depois sem escopo). ID já instalado é pulado. Falha é registrada em `status.json → apps` e não trava os outros; após 3 tentativas o ID é desistido e a etapa fica `ok (sem sucesso após 3 tentativas: …)`. |
| `wireguard` (se WireGuard) | instala o WireGuard oficial, gera as chaves e registra no painel pelo relatório (§3.1). Túnel ainda não aplicado (hub fora do ar, painel sem hub) não é falha: fica `ok (túnel ainda não aplicado: …)` e o relatório aplica depois. |
| `tailscale_install` (se Tailscale) | MSI oficial estável do Tailscale (§3.2), serviço “Tailscale” automático. |
| `tailscale_login` (se Tailscale) | `tailscale up --auth-key=file:… --hostname=<nome> --unattended`. Chave inválida/expirada/já usada: `falhou …` com o motivo e nova tentativa; na 3ª falha a etapa fica `desistiu após 3 tentativas: …`, a chave é apagada e a pós-instalação termina (as outras etapas não esperam por ela). |
| `report` | garante as tarefas UAB Report / UAB Shutdown. |

Sem internet: `provisioned = "pendente: network"` e nova tentativa.

**winget como SYSTEM** (decisão): o `winget.exe` não está no PATH do SYSTEM, mas o App
Installer vem provisionado na imagem; o script usa o `winget.exe` da pasta mais nova
`C:\Program Files\WindowsApps\Microsoft.DesktopAppInstaller_*_x64__8wekyb3d8bbwe` e põe no
PATH as pastas das dependências do pacote (`Microsoft.VCLibs.140.00(.UWPDesktop)`,
`Microsoft.WindowsAppRuntime.*`, `Microsoft.UI.Xaml.*`) — sem isso, como SYSTEM, o winget
morre com `0xC0000135` (DLL não encontrada), porque o pacote não está registrado para o
SYSTEM (visto e corrigido no teste). Se não
existir/não responder, instala o App Installer oficial (release do `microsoft/winget-cli`
no GitHub, com `DesktopAppInstaller_Dependencies.zip`) via `Add-AppxProvisionedPackage`.
Rodar como SYSTEM permite instalar sem ninguém logar; programas que só têm instalador
“por usuário” (ex.: Discord, Spotify) não ficam bem assim e foram deixados fora do catálogo.

Ordem: `openssh`, `wireguard`, `tailscale_install`, `tailscale_login`, `remote_agent`,
`apps`, `report` (função `Get-UabPlan`, a mesma no specialize e na pós-instalação). Valor
`desistiu…` = não tenta mais e não conta como pendente.

### 3.1 WireGuard pelo hub do painel (`vpn.ps1`)

- **Instalador:** o `latest.sig` de `https://download.wireguard.com/windows-client/` dá o
  nome do MSI atual (`wireguard-amd64-<versão>.msi`). O hash desse arquivo é BLAKE2b (o
  PowerShell 5.1 não calcula), então a garantia é a **assinatura Authenticode**: estado
  `Valid` e assinante `CN=WireGuard LLC` — conferida antes do `msiexec /qn DO_NOT_LAUNCH=1`.
- **Chaves:** `wg.exe genkey` no próprio PC; `C:\ProgramData\uab\wg\private.key` (pasta só
  SYSTEM + Administradores, sem herança). A privada nunca sai do PC; a pública vai no
  relatório (`wg.pubkey`).
- **Aplicação** (a cada relatório, com a resposta do painel): mesma validação estrita do
  `payload/uab-wg-apply.py` — `address` /32 dentro de `net`, `allowed_ips` dentro de `net`,
  chave do hub e `endpoint` (`host:porta`) no formato certo, nenhuma rede local do PC
  sobrepondo `net`/`lanmap_net`. Grava `wg\uabwg.conf` e instala o túnel como **serviço**
  (`wireguard.exe /installtunnelservice`, serviço `WireGuardTunnel$uabwg`, automático:
  sobe no boot antes de qualquer login). Só reinstala o serviço se a config mudou; serviço
  parado é reiniciado.
- **Sem handshake há mais de 3 min** (e mais de 3 min desde a última aplicação): reinicia o
  serviço do túnel, que resolve o nome do hub de novo (IP de casa/DDNS pode ter mudado).
- **`lan` (acesso à rede local do cliente): ignorado no Windows** — o túnel sobe sem ele e
  o `message` do relatório avisa. O painel esconde a opção para PCs Windows.
- Estado em `wg\status.json` (`state` = `aguardando` | `conectando` | `ativo` | `erro`,
  `address`, `handshake`, `lan`, `message`, `applied`), enviado no relatório como no Linux.
- A interface `uabwg` fica fora de `local_ips`. O firewall do Windows já libera o SSH
  (porta 22) em todos os perfis; o painel entra pelo SOCKS do hub (origem `10.77.0.1`).

### 3.2 Tailscale (`vpn.ps1`)

- `https://pkgs.tailscale.com/stable/?mode=json` → `MSIs.amd64`; baixa o MSI, confere o
  **SHA256** publicado ao lado (`<msi>.sha256`) e a **assinatura Authenticode**
  (`CN=Tailscale Inc.`), e instala com `TS_UNATTENDEDMODE=always TS_NOLAUNCH=true`.
- Login com a auth key lida do arquivo (`--auth-key=file:…`: não aparece na linha de
  comando nem no log; a saída do `tailscale up` que vai para o status tem `tskey-***`).
  Depois do login (ou de desistir) o arquivo é apagado.
- Relatório: `tailscale` = `{state, ips, dns, hostname}` (de `tailscale status --json`),
  `services.tailscaled` = serviço “Tailscale” ativo.

`C:\ProgramData\uab\status.json`:
```json
{"plan":["openssh","remote_agent","apps","report"],"current":"","provisioned":"ok 2026-09-28 15:10:02",
 "steps":{"openssh":"ok","remote_agent":"ok","apps":"ok","report":"ok"},
 "apps":{"7zip.7zip":"ok","Notepad++.Notepad++":"ok"}}
```
Logs em `C:\ProgramData\uab\logs\` (`pe.log`, `specialize.log`, `provision.log`, `report.log`).

### 3.3 Ativação (`provision.ps1`, etapa `activation`)

Só existe quando o técnico informou a chave no painel (`uab.json` → `activate: true`; com a
chave genérica do Pro não há o que ativar). A chave já é aplicada na instalação
(`ProductKey`); depois dos programas, com internet, a etapa pede a ativação na hora
(`slmgr.vbs /ato`) e confere `SoftwareLicensingProduct.LicenseStatus = 1`. Resultado no painel:
`ok (Windows ativado)` ou, com chave recusada, até 3 tentativas (contador em
`C:\ProgramData\uab\activation-tries`, porque o `status.json` só guarda os campos dele) e
então `ok (não ativado após 3 tentativas: <motivo>)` — sem travar o resto. A chave não vai
para o `uab.json`. Não testado com chave real (a VM usa a chave genérica); sintaxe
conferida com o parser do PowerShell 7.

## 4. Relatório (`report.ps1`)

`POST <panel_url>/api/agent/report` com `Authorization: Bearer <token>`, mesmo JSON do
Linux (`payload/uab-report.sh`): `machine_id` (MachineGuid sem hífens), `hw_id`
(`sha256("uab-hw:smbios:<uuid>")`, ou MAC da 1ª placa física se o UUID for genérico),
`hostname` minúsculo, `platform: "windows"`, `os` (Caption, ex. “Microsoft Windows 11 Pro”),
`kernel` (`10.0.<build>.<UBR> (<25H2>)`), `admin_user`, `ssh_auth: "key"`, `ssh_port`,
`local_ips` (`"<interface> <ip>/<prefixo>"`), `gateway`, `provisioned`/`provision_plan`/
`provision_current`/`steps` (+ `apps`), `services` (`ssh`, `meshagent` se Remoto,
`uab-provision`), `resources` (cpus, `load1` aproximado pelo uso de CPU, memória, disco do
sistema), `uptime_seconds`, `wg` e `tailscale` (§3.1/§3.2; `services.wireguard` e
`services.tailscaled` quando usados). Com WireGuard, a resposta do painel é passada ao
`Invoke-UabWgApply`. `-Shutdown` manda o aviso `X-UAB-Event: shutdown`.
Uso manual: `& C:\ProgramData\uab\report.ps1 -Verbose` (PowerShell como administrador).

## 5. Catálogo (`windows/apps.json`)

53 apps em 11 categorias. **IDs conferidos** no índice oficial do winget
(`https://cdn.winget.microsoft.com/cache/source.msix` → `Public/index.db`, tabela `ids`) em
2026-09-28: todos existem (o `RustDesk.RustDesk` não existe no índice e ficou de fora).
**Ícones** conferidos por download: slugs do Simple Icons em
`cdn.jsdelivr.net/npm/simple-icons@16/icons/<slug>.svg` e Lucide em
`cdn.jsdelivr.net/npm/lucide-static@latest/icons/<nome>.svg` (Microsoft, WinRAR, PDF etc.
usam Lucide — o Simple Icons não tem logos da Microsoft).

## 6. Testes

```bash
# unitários (XML válido, escape, validação, extração do pe.js, catálogo, VPN,
# validação do assistente/painel para o Windows; o de sintaxe PowerShell roda se houver pwsh)
python3 -m unittest discover -s windows/tests -v

# instalação em VM (QEMU/KVM + OVMF), sem o gravador:
windows/tests/make_test_xml.py /tmp/t.xml --panel-url http://10.0.2.2:18081 \
   --token-file token --pubkey-file panel.pub --remote-url http://10.0.2.2:18421/remoto \
   --meshid-file meshid --password-file senha \
   --image "Windows 11 Enterprise Evaluation" --no-key     # só para a ISO de avaliação
windows/tests/vm_test.sh start /tmp/t.xml   # ISO como CD, XML num "pendrive" USB FAT,
                                            # discos SATA de 30 GB (0) e 64 GB (1)
windows/tests/vm_test.sh shot /tmp/tela.ppm # screenshot (VNC em 127.0.0.1:5947)
windows/tests/vm_test.sh clean
```
O `vm_test.sh` encaminha `10.0.2.100:443` da VM (via `nc`, uma conexão por vez) para um
proxy TLS local em `127.0.0.1:18443` — que precisa usar o `webserver-cert-*` do próprio
MeshCentral (com `TlsOffload` o agente confere o certificado) —, e o SSH da VM para
`127.0.0.1:12222`. Os discos internos têm `bootindex` antes do CD porque o OVMF descarta
entradas de boot fora da lista do QEMU (inclusive o “Windows Boot Manager” do bcdboot).
`CD_BOOTINDEX=0 vm_test.sh boot` inicia o PC já instalado pelo CD de novo (teste do laço).

### Resultado do teste em VM (2026-09-28, ISO Win11 25H2 Enterprise Eval pt-BR)

Discos: SATA 30 GB (disco 0), SATA 64 GB (disco 1), “pendrive” USB FAT com o XML (disco 2).
Instalação 100% automática, sem tocar em nada:

| hora | evento |
|---|---|
| 15:48 | boot pelo CD; pe.js: USB ignorado, discos 0 e 1 elegíveis, **escolhido o disco 1 (64 GB)** |
| 15:50 | dism + bcdboot concluídos, reinício (aplicar a imagem levou ~2 min) |
| 15:52 | specialize: arquivos extraídos, ACL, 3 tarefas criadas; OOBE sem nenhuma tela |
| 15:58 | 1º relatório no painel de teste (`POST /api/agent/report` 200) |
| 16:04 | `openssh` ok (capacidade do Windows Update), `remote_agent` ok, apps 7-Zip/Notepad++/VLC ok |
| 16:15 | ID inexistente de propósito (`Uab.NaoExiste`) desistido após 3 tentativas; `provisioned = ok` |

Conferido: `GET /api/machines` mostra a máquina online (`os` “Microsoft Windows 11
Enterprise Evaluation”, `kernel` “10.0.26200.6584 (25H2)”, serviços ssh/meshagent ativos,
etapas ok); SSH com a chave do painel entra como `pc-teste-01\suporte` no PowerShell;
`PasswordAuthentication no`; ACL do `administrators_authorized_keys` só SISTEMA +
Administradores (Windows em pt-BR); usuário local no grupo Administradores; nome
`PC-TESTE-01`, fuso `E. South America Standard Time`, pt-BR, teclado `0416:00010416`;
serviço “Mesh Agent” ativo e o dispositivo `PC-TESTE-01` **conectado** no MeshCentral de
teste, no grupo “Servidores ISO Builder”; `Panther\unattend.xml` apagado; tarefa
`UAB Provision` desativada ao concluir, `UAB Report`/`UAB Shutdown` prontas; aviso de
desligamento recebido (`off_reason = poweroff`); `hw_id` = `sha256("uab-hw:mac:52:54:00:12:34:56")`
(UUID do QEMU é genérico → MAC, como no Linux). Disco 0 continuou `RAW` (intocado); o
`autounattend.xml` do pendrive ficou idêntico (o Windows só criou `System Volume Information`
nele, como faz em qualquer pendrive). Também testado: PC já instalado iniciando pelo
pendrive de novo → aviso “JÁ FOI INSTALADO” sem apagar; só um disco de 8 GB → mensagem
“Nenhum disco interno elegível… Nenhum disco foi apagado” e, ao fechar, o Setup normal
pedindo o disco.

### Resultado do teste em VM — VPN (2026-09-28, ISO oficial Win11 25H2 **Pro** pt-BR)

Hub de teste (imagem de `web/hub`, container `uabtest-hub`, UDP só em `127.0.0.1`) e painel
de teste compartilhando o `WG_STATE`, como no compose; build criado por `POST /api/builds`
com `wireguard`, `tailscale` (auth key **inválida** de propósito), `panel_access` e 7-Zip.

| hora | evento |
|---|---|
| 18:01 → 18:08 | instalação sem toque e 1º relatório (~7 min) |
| 18:20 | `tailscale_install` ok (SHA256 + Authenticode “Tailscale Inc.”); `tailscale_login`: `falhou …: backend error: invalid key: API key does not exist` — `openssh`/`apps` seguiram normalmente |
| 18:20 | **bug achado:** `latest.sig` vem como `application/octet-stream` e o `Invoke-WebRequest` devolve bytes → corrigido (`Get-UabWebText`) |
| 18:25 | **bug achado:** `wg genkey` (saída junto com stderr) tinha um aviso extra → corrigido (pega só a linha da chave) |
| 18:30 | `wireguard` ok: MSI 1.1.1 assinado por WireGuard LLC, chaves geradas no PC (ACL só SISTEMA/Administradores), túnel `WireGuardTunnel$uabwg` automático; handshake no hub em segundos; `/api/machines`: `connected: true`, IP `10.77.1.1` |
| 18:30 | `tailscale_login` = `desistiu após 3 tentativas: …` (chave apagada); `provisioned = ok` |
| 18:31 | terminal do painel (websocket `/api/machines/{id}/terminal`) abre o PowerShell como `pc-vpn-01\suporte` “via WireGuard do seu servidor”; no PC a conexão SSH vem de `10.77.0.1` (hub) |
| 18:31 | “gerar acesso de dispositivo”: config + QR; usada num container de teste, alcança a porta 22 do PC pela VPN (`SSH-2.0-OpenSSH_for_Windows_9…`); ligar “rede local do cliente” num PC Windows é recusado com mensagem clara |
| 18:32 → 18:35 | reinício: o serviço do túnel sobe 3 s após o boot, **sem ninguém logado**, novo handshake e terminal de novo pelo túnel |
| 18:36 → 18:53 | **reinstalação** (mesmo build, mesmo hardware, scripts corrigidos, instalação do zero): `wireguard` ok na 1ª tentativa; entrada adotada — mesmo slot/IP `10.77.1.1` com a chave nova, os 2 acessos de dispositivo mantidos, peer antigo removido do hub; terminal pelo túnel ok |

Também achado e corrigido: sem login no Tailscale o relatório mandava `"ips": [null]` e a
lista de máquinas do painel quebrava (`panel.machine_row`); agora o PC manda `[]` e o painel
ignora itens que não são texto (teste em `windows/tests/test_panel_windows.py`).
Observação (Fase 1): `Stop-Computer` pela sessão SSH não gerou o aviso de desligamento
(evento 1074); a adoção da reinstalação aconteceu pela janela de 15 min, como previsto.

## 7. Limitações conhecidas

- Só x64 (agente Mesh `id=4`; ARM64 não testado). Só Windows 11.
- O Setup interativo (quando não há disco elegível) usa as checagens normais; o LabConfig
  cobre TPM/Secure Boot/RAM, mas o Setup novo pode recusar CPUs muito antigas.
- `OpenSSH.Server` vem do Windows Update: sem internet ou com WSUS bloqueando, cai para o
  winget; sem nenhum dos dois, a etapa fica pendente e é tentada de novo.
- Apps “por usuário” instalados como SYSTEM não aparecem para o usuário (não estão no catálogo).
- O aviso de desligamento (evento 1074) cobre desligar/reiniciar pelo Windows; queda de
  energia não avisa (o painel marca offline por tempo, como no Linux).
- Ativação: sem chave, fica com a chave genérica do Pro (não ativado).
- WireGuard: sem “acesso à rede local do cliente” (NETMAP) no Windows; o MSI do WireGuard
  é conferido só pela assinatura Authenticode (o hash publicado é BLAKE2b).
- WireGuard/Tailscale precisam de internet na pós-instalação (baixam os instaladores).
