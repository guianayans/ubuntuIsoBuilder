# Roadmap — criador universal de ISO Ubuntu Server

Melhoria planejada para depois da interface web e do gravador de pendrive:
transformar o projeto em um criador de ISO **universal**, com seleção de
programas, presets e opções extras.

## Base que já existe

- `payload/uab-provision.sh` roda etapas **independentes, idempotentes e com
  retentativa automática** (SSH, pacotes, Tailscale, Docker). Cada programa
  novo vira mais uma etapa no mesmo formato.
- `EXTRA_PACKAGES` já cobre pacotes simples do repositório Ubuntu.
- Docker e Tailscale já usam repositório oficial com fingerprint GPG
  conferido: servem de modelo para os módulos.

## 1. Catálogo de módulos (`catalog/`)

Um arquivo por programa (ex.: `catalog/portainer.yaml`) contendo:

- nome, descrição, categoria, ícone;
- pacotes apt e/ou repositório oficial + fingerprint da chave GPG;
- script de instalação (idempotente) e script de verificação
  (serviço ativo, porta respondendo, comando de versão);
- dependências (ex.: Portainer → Docker);
- versões do Ubuntu suportadas (24.04, 26.04);
- campos de configuração que o assistente precisa perguntar (senha, domínio,
  porta…), com indicação de quais são segredos.

Os primeiros módulos seriam Docker e Tailscale, migrados do provisionador atual.

## 2. Presets

Combinações prontas de módulos:

| Preset | Módulos |
|---|---|
| Servidor Docker | Docker, Compose, Portainer |
| Servidor web | Caddy ou Nginx, UFW |
| NAS / arquivos | Samba, NFS |
| Monitoramento | Netdata ou node-exporter |
| Mínimo seguro | SSH, UFW, fail2ban, atualizações automáticas |

Opção **"salvar como preset"** na interface, para gerar outra ISO igual
trocando só hostname e chaves.

## 3. Apps em container

Módulos que sobem um `docker-compose.yml` pronto no servidor
(ex.: Uptime Kuma, Nextcloud, Portainer), com volumes em `/opt/apps/<app>`.

## 4. Opções gerais adicionais

- firewall (UFW) com portas escolhidas;
- atualizações automáticas (unattended-upgrades);
- swap;
- vários usuários;
- WireGuard como alternativa ao Tailscale;
- disco de dados separado ou RAID.

## 5. Interface

- Etapa **"Programas"** no assistente web: caixinhas agrupadas por categoria,
  seleção de preset, dependências marcadas automaticamente.
- Campos extras aparecem só para os módulos selecionados.

## Regras obrigatórias

- **Fail-safe:** a falha de um módulo nunca pode afetar SSH, rede ou
  Tailscale. SSH/Tailscale continuam sendo as primeiras etapas; cada módulo é
  uma etapa isolada com marcador próprio em `/var/lib/uab/`.
- **Testes:** cada preset precisa passar no `test-iso.sh` em VM antes de ser
  marcado como "verificado" (idealmente um teste automático por preset).
- **Segredos:** senhas/tokens de módulos seguem as mesmas regras atuais
  (fora do Git, 600, mascarados, fora do user-data, apagados após o uso).
- **Documentação:** cada módulo documenta como acessar e como recuperar.

## Cuidados

- Mais programas no primeiro boot = provisionamento mais longo e mais
  dependente de internet.
- Módulos de terceiros podem mudar repositório/chave; a verificação de
  fingerprint deve falhar de forma segura (com retentativa e aviso no
  `uab-status`).

## 6. Windows (planejado em 28/09/2026 — implementar depois)

Gerar instalação automática de **Windows 10/11** (depois Windows Server) com o
mesmo fluxo do Ubuntu: formata sozinho e aparece em Máquinas.

**Como:** `autounattend.xml` gerado pelo assistente (idioma, ABNT2, fuso, nome do
PC, usuário local com senha, sem conta Microsoft/OOBE, bypass de TPM/Secure Boot
no 11) + `SetupComplete.cmd`/PowerShell fazendo o papel do `uab-provision`.

| Ubuntu hoje | Windows |
|---|---|
| relatório ao painel | tarefa agendada PowerShell (10 min), mesmo JSON; machine_id = MachineGuid; hw_id = UUID SMBIOS (Win32_ComputerSystemProduct) |
| Remoto (MeshCentral) | agente oficial Windows |
| Tailscale | instalador oficial com auth key |
| WireGuard pelo hub | app oficial como serviço (`/installtunnelservice`) |
| terminal no painel | OpenSSH Server nativo (PowerShell) |
| programas | winget (Chrome, 7-Zip, AnyDesk…) |
| Coolify/Docker | não se aplica |

**Pontos difíceis:**
- **Licença:** não distribuir a ISO da Microsoft. Preferido: o isoBuilder usa a
  ISO que o técnico já tem no computador e o painel manda só a configuração
  (alternativa: enviar a ISO base uma vez ao painel). Ativação é de quem instala.
- **Gravação:** ISO do Windows não é híbrida (dd não inicializa); precisa de
  partição FAT32 com os arquivos e `install.wim` > 4 GB dividido em `.swm`. Com
  **Ventoy** é simples (aceita a ISO e injeta o autounattend por plugin
  `auto_install`).
- **Disco:** script no windowsPE escolhe o maior disco interno (nunca o pendrive),
  em vez do "disco 0".
- **Drivers:** Intel VMD/RST e algumas placas de rede exigem pasta de drivers
  injetada no pendrive.

**Fases:**
1. Win 10/11 Pro, usuário local, relatório + Remoto + Tailscale, via Ventoy ou
   isoBuilder com a ISO do técnico.
2. WireGuard pelo hub, terminal SSH, programas via winget, pasta de drivers.
3. Gravação sem Ventoy (divisão do install.wim) e Windows Server.

**Decisões tomadas (28/09/2026):**
- **Público:** máquinas novas; a chave do Windows é digitada pelo técnico no assistente.
- **ISO:** o painel NÃO hospeda Windows. O gravador isoBuilder baixa a ISO oficial da
  Microsoft no PC do cliente e injeta a configuração na hora de gravar (como o Rufus);
  alternativa "já tenho a ISO" (escolher arquivo) para quando o download da Microsoft
  for bloqueado.
- **Assistente:** o primeiro passo passa a ser **qual sistema** (Ubuntu Server 24.04/26.04,
  Windows 11 Pro…); isso define as etapas seguintes e qual ISO o gravador baixa.
- **Programas no Windows:** agente do Mesh (Remoto) + catálogo estilo Ninite com logos
  (instalados via winget na pós-instalação), escolhidos ao configurar a ISO.
- **Máquinas:** logo do sistema em cada máquina (feito; Windows com ícone próprio de
  quatro quadrados, porque o Simple Icons não distribui logos da Microsoft).
- **Backup:** tag `v1-ubuntu` no GitHub = última versão só com Ubuntu.
- Obs.: o suporte do Windows 10 acabou em 14/10/2025; foco no Windows 11.
