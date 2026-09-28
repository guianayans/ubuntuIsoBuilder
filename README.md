# ubuntu-autoinstall-builder

Gera uma ISO **bootável** do Ubuntu Server LTS que instala o sistema sozinha
(Subiquity Autoinstall + cloud-init NoCloud). Depois do primeiro boot, o servidor
fica acessível remotamente via **Tailscale + SSH**, sem precisar de ninguém na
frente da máquina.

```text
./setup.sh   →   output/<hostname>-autoinstall.iso   →   pendrive   →   boot   →   ssh usuario@servidor
```

Depois da instalação e do primeiro boot o servidor fica com:

- rede configurada (DHCP ou IP estático);
- OpenSSH ativo, em um de dois modos à sua escolha:
  - **chave SSH** (recomendado): a senha é desativada no SSH depois que a chave é confirmada;
  - **usuário e senha** (mais simples): a senha é aceita só pela rede local e pelo Tailscale, com fail2ban;
- Tailscale conectado à sua Tailnet com o hostname do servidor;
- hostname, usuário administrativo (sudo), timezone, locale e teclado configurados;
- Docker Engine + Docker Compose plugin (opcional), com o usuário no grupo `docker`;
- `curl wget git ca-certificates openssh-server htop unzip rsync` + pacotes extras;
- todos os serviços habilitados para subir sozinhos após um reboot.

---

## Sumário

1. [Requisitos](#requisitos)
2. [Construção](#construção) · [Gerar novamente](#gerar-novamente) · [Validar](#validar) · [Testar](#testar)
3. [Interface web](#interface-web-isogvtserveronline) · [isoBuilder (gravador)](#isobuilder-gravador-de-pendrive-helper)
4. [Criar o pendrive](#criar-o-pendrive)
5. [Instalação na máquina remota](#instalação-na-máquina-remota)
6. [Primeiro acesso](#primeiro-acesso)
7. [Tailscale](#tailscale)
8. [SSH por usuário e senha](#ssh-por-usuário-e-senha-opcional) · [SSH: chaves de novos computadores](#ssh-chaves-de-novos-computadores)
9. [Como funciona (arquitetura)](#como-funciona)
10. [Segurança](#segurança)
11. [Recuperação](#recuperação)
12. [Limitações conhecidas](#limitações-conhecidas)

---

## Requisitos

Máquina que **gera** a ISO (não é a máquina que vai ser instalada):

| Sistema | Dependências |
|---|---|
| Ubuntu/Debian | `sudo apt install xorriso python3-yaml gnupg curl openssl openssh-client` (opcional: `python3-jsonschema`; para testes: `qemu-system-x86 qemu-utils ovmf`) |
| Fedora | `sudo dnf install xorriso python3-pyyaml gnupg2 curl openssl openssh-clients` |
| macOS (Homebrew) | `brew install xorriso gnupg` e `python3 -m pip install --user pyyaml` (testes: `brew install qemu`) |

O `setup.sh` detecta o que falta e oferece instalar. Precisa de ~9 GB livres
(ISO oficial em `cache/` + ISO gerada em `output/`) e acesso à internet.

Máquina que **recebe** o Ubuntu: x86-64 (amd64), UEFI (Secure Boot pode ficar
ligado) ou BIOS legado, **cabo de rede** com acesso à internet (DHCP ou IP
estático informado no setup).

---

## Construção

```bash
./setup.sh
```

O assistente pergunta, nesta ordem:

| Etapa | O que pergunta | Padrão |
|---|---|---|
| Ubuntu | 24.04 LTS ou 26.04 LTS | 24.04 |
| Sistema | hostname, usuário, nome, timezone, locale, teclado (+variante) | `America/Sao_Paulo`, `pt_BR.UTF-8`, `br` |
| Senha | senha do usuário (digitada 2×, sem eco; só o hash SHA-512 é salvo) | — |
| Rede | 1. DHCP · 2. IP estático (MAC / nome da interface / primeira `en*`, IP/CIDR, gateway, DNS, domínio de busca) | DHCP |
| Disco | como identificar o disco (serial / caminho / modelo / único disco interno), tamanho mínimo, layout (LVM/ext4), se é USB, **confirmação `APAGAR`** | serial, LVM |
| Acesso SSH | 1. chave SSH (recomendado) · 2. usuário e senha | chave |
| Chave SSH | 1. `~/.ssh/id_ed25519.pub` · 2. caminho de um `.pub` · 3. colar a chave; pode adicionar várias (opcional no modo senha) | — |
| Tailscale | instalar? nome na Tailnet, argumentos extras, **auth key** (entrada oculta) | sim, hostname |
| Docker | instalar Docker Engine + Compose? | sim |
| Pacotes | pacotes extras separados por espaço | nenhum |

No final ele mostra um resumo, grava a configuração e pergunta se já deve gerar a ISO.

### Como descobrir o disco da máquina remota

Dê boot na máquina com qualquer Linux live (o próprio pendrive do Ubuntu
serve: no instalador, `Ctrl+Alt+F2` abre um shell) e rode:

```bash
lsblk -d -o NAME,SIZE,TYPE,TRAN,MODEL,SERIAL
```

Use o **SERIAL** (opção 1). É o que mais resiste a mudanças: `/dev/sda` pode
virar `/dev/sdb` quando o pendrive está conectado.

> **Proteção do disco.** Antes de qualquer escrita, o instalador roda
> `preflight.sh`, que resolve o critério e **aborta sem tocar em nada** quando:
> nenhum disco corresponde; mais de um disco corresponde; o disco é o próprio
> pendrive; o disco é USB/externo (a menos que você tenha dito que é); ou o
> disco é menor que o tamanho mínimo informado. Se passar, ele fixa o disco
> exato (`match: {path: /dev/...}`) na configuração do Subiquity.

Arquivos gerados pelo setup:

```text
config/install.env        configuração (sem segredos, 600)
config/authorized_keys    suas chaves PÚBLICAS
secrets/password.hash     hash SHA-512 da senha (600)
secrets/tailscale-authkey auth key do Tailscale (600)
```

## Gerar novamente

```bash
./build.sh                    # usa config/install.env + secrets/
./build.sh --forget-secrets   # apaga secrets/tailscale-authkey depois de gerar
./build.sh --keep-work        # mantém work/ para depuração (contém segredos)
```

Etapas: dependências → validação da configuração → download e verificação da
ISO oficial (**assinatura GPG** do `SHA256SUMS` + **SHA256** da ISO) → geração do
`user-data`/`meta-data` → validação (schema oficial do Subiquity + verificações
próprias) → GRUB → reconstrução com `xorriso` → verificação da ISO gerada.

Cada build tem um ID único (`output/<hostname>-build-info.txt`). Saídas:

```text
output/<hostname>-autoinstall.iso          ISO final (600)
output/<hostname>-autoinstall.iso.sha256   checksum
output/<hostname>-boot-report.txt          parâmetros de boot da ISO oficial (inspecionados)
output/<hostname>-build-info.txt           build id, ISO de origem e SHA256
```

Para mudar alguma resposta, rode `./setup.sh` de novo: as respostas atuais
aparecem como padrão. Se a auth key tiver sido apagada, o `build.sh` pede de novo.

## Validar

```bash
./validate.sh             # resumo
./validate.sh --verbose   # cada verificação
./validate.sh caminho/para/outra.iso
```

```text
[OK] Autoinstall
[OK] Hostname
[OK] SSH
[OK] Network
[OK] Storage
[OK] Tailscale
[OK] ISO
```

Verifica: sintaxe YAML; estrutura do Autoinstall (`version: 1`, sem seções
interativas, `shutdown: reboot`, early/late-commands); **schema oficial do
Subiquity** (`schema/autoinstall-schema.json`, se `python3-jsonschema` estiver
instalado); `cloud-init schema` (se o cloud-init estiver instalado); hostname;
hash da senha; chaves públicas válidas e **nenhuma chave privada**; netplan;
storage e confirmação `APAGAR`; auth key (formato, permissão 600, ausente do
`user-data`); arquivos NoCloud; payload; GRUB (`autoinstall`,
`ds=nocloud;s=/cdrom/nocloud/`, proteção anti-reinstalação, kernel e initrd
existentes); boot UEFI/BIOS e opções híbridas comparados com a ISO oficial;
`md5sum.txt`; SHA256.

## Testar

```bash
./test-iso.sh                  # UEFI, ISO conectada como pendrive USB
./test-iso.sh --bios           # BIOS legado
./test-iso.sh --cdrom          # ISO como CD-ROM
./test-iso.sh --abort-test     # prova que disco não encontrado ABORTA sem escrever nada
./test-iso.sh --with-tailscale # também faz login no Tailscale (gasta a auth key!)
./test-iso.sh --ssh-password   # testa o modo "usuário e senha" (login real por senha + sudo)
./test-iso.sh --keep           # mantém VM/logs em work/test/
```

Cria uma VM QEMU/KVM com um **disco virtual descartável** (`work/test/disk.qcow2`,
25 GB). **Nenhum disco físico é usado.** Pergunta antes de começar (`--yes` pula).
A ISO de teste é uma variante da sua: aponta para o disco virtual, usa DHCP,
hostname `<hostname>-vmtest`, adiciona uma chave SSH descartável e, por padrão,
**não** leva a auth key do Tailscale.

O teste confirma: a ISO dá boot → o autoinstall começa → o Ubuntu instala → o
sistema reinicia sozinho → o SSH aceita a chave → o SSH **recusa senha** →
hostname, timezone, locale, teclado, pacotes, Docker e Tailscale → reboot com o
pendrive **ainda conectado** não reinstala → serviços sobem de novo.
Com KVM leva ~5–15 min (medido: 24.04 ≈ 4 min, 26.04 ≈ 12 min). Log serial: `work/test/serial.log`.

---

## Interface web (iso.gvtserver.online)

Uma interface web para gerar a ISO pelo navegador, sem terminal. Ela usa os mesmos
scripts do projeto, e o usuário grava o pendrive no próprio computador com o
**isoBuilder**, o gravador de pendrive (Windows e Mac).

### Fluxo do usuário

1. Entra em `https://iso.gvtserver.online` com e-mail e senha.
2. Preenche o assistente: Sistema → Acesso → Rede → Disco → Tailscale → Programas → Revisar (digita `APAGAR`).
3. O servidor gera a ISO. Se a ISO oficial do Ubuntu ainda não estiver no servidor, ela é baixada nessa hora, com progresso, e a assinatura GPG e o SHA256 são conferidos.
4. A página detecta Windows ou Mac e mostra **um único botão de download**: o gravador personalizado, com ~8 MB e um link temporário para aquela ISO. Abaixo vem o tutorial, incluindo o aviso do SmartScreen ou do Gatekeeper.
5. Ao abrir, o gravador:
   - **baixa a ISO sozinho** para Downloads, retomando se a internet cair;
   - confere o SHA256;
   - lista **só pendrives e discos USB**;
   - grava e relê o pendrive para verificar.

Em "Outras formas de baixar" continuam disponíveis a ISO sozinha, o `.sha256` e os gravadores das outras plataformas. Com a ISO e o `.sha256` na mesma pasta, o gravador funciona até sem internet.

**Link do gravador:**
- vale até a ISO expirar, com no máximo `DL_TOKEN_DAYS` (padrão 7 dias);
- para de funcionar se você revogar o acesso de quem baixou ou excluir a ISO;
- no servidor fica só o hash do token.

### Contas

- **E-mail das contas temporárias:** você escolhe só o **prefixo**. O domínio é
  fixo (`@gvtserver.online`, configurável em `GUEST_EMAIL_DOMAIN`), e a senha é
  gerada automaticamente.
- **Entregar uma ISO para alguém:** no último passo do assistente (admin), marque
  **"Entregar esta ISO para uma pessoa"** e digite o prefixo. Ao gerar:
  - a conta "somente download" é criada já vinculada à ISO;
  - as credenciais aparecem na tela para copiar, uma única vez;
  - a pessoa entra, acompanha a geração e baixa o gravador;
  - a ISO fica guardada até o fim do acesso.

  Se o prefixo já existir, nada é criado, nem a conta nem a ISO.
- **Identificação das máquinas:** em **Máquinas**, a coluna **Conta** mostra o
  e-mail da conta que recebeu a ISO (ou de quem baixou). O overlay lista também
  quem gerou a ISO e **quem baixou** o gravador ou a ISO, com data e sistema.
- **Admin:** e-mail e senha vêm das variáveis `ADMIN_EMAIL` (padrão `yanguimaraesviana@gmail.com`) e `ADMIN_PASSWORD`, esta com no mínimo 12 caracteres. Sem `ADMIN_PASSWORD`, o login do admin fica desativado.
- **Acessos temporários:** o admin cria em **Acessos**. Informa para quem é (opcional), um e-mail de login (opcional; se ficar vazio, é gerado um), a validade (1 h a 30 dias) e o máximo de ISOs.
  - A senha é gerada automaticamente e **mostrada uma única vez**, pronta para copiar com endereço, e-mail, senha e validade. No banco fica só o hash scrypt.
  - Ações disponíveis: nova senha, +24 h, revogar (derruba a sessão na hora) e excluir.
- **Dois tipos de acesso temporário:**
  - **Pode gerar ISOs:** a pessoa usa o assistente e gera as próprias ISOs, até o limite definido.
  - **Só baixar uma ISO pronta:** você gera a ISO na sua conta e vincula ao acesso. A pessoa entra e cai direto na página de download dessa ISO. Ela não vê o assistente, não gera nem exclui ISOs, e nunca vê a senha do servidor nem a auth key. A ISO vinculada fica guardada até o fim da validade do acesso. A lista de acessos mostra qual ISO está vinculada.
- **Não existe cadastro.** Só o admin cria contas.
- Cada convidado vê apenas as próprias ISOs. ISOs excluídas continuam contando no limite dele.

### Segurança

- Sessões ficam no servidor (cookie `HttpOnly`, `Secure`, `SameSite=Strict`).
- Proteção CSRF: cabeçalho obrigatório + checagem de origem.
- CSP estrita, sem scripts inline.
- Limite de tentativas de login: 6 por e-mail e 20 por IP a cada 15 min.
- A senha do servidor vira hash `$6$` na hora. A auth key do Tailscale e o hash são **apagados do servidor** assim que a ISO fica pronta; o log do build não contém segredos.
- As ISOs expiram (`ISO_TTL_HOURS`, padrão 48 h) e no máximo `MAX_STORED_ISOS` ficam guardadas.
- Dá para excluir uma ISO gerada a qualquer momento (**Minhas ISOs → Excluir**). A ISO oficial do
  Ubuntu usada como base fica em cache; apague em **Sistema → ISO oficial → Apagar**, ou defina
  `KEEP_BASE_ISO=false` para ela ser apagada sozinha após cada geração (a próxima baixa de novo).
- O container roda como usuário sem privilégios (`cap_drop: ALL`, `no-new-privileges`).

### Máquinas conectadas (menu **Máquinas**)

Toda ISO gerada pela interface web faz o servidor **se reportar ao painel**:
no primeiro boot e depois a cada 10 minutos (`uab-report.timer`). Ao desligar ou
reiniciar, manda um aviso curto (`uab-report-shutdown.service`) e a bolinha fica
vermelha na hora; sem aviso (queda de energia), fica vermelha após 25 min sem relatório.
O servidor envia, por HTTPS, com um token único por ISO (no banco fica só o hash):

- hostname e usuário administrador;
- IPs do Tailscale e da rede local, gateway;
- Ubuntu e kernel, uptime;
- CPU, memória e disco;
- status de SSH, Tailscale, Docker e fail2ban;
- cada etapa do provisionamento.

Nenhuma senha ou chave é enviada.

**PC reinstalado não duplica.** O relatório leva também um *hash* da identidade do
hardware (UUID da placa-mãe; se vier genérico, o MAC da 1ª placa de rede física), e
não o número em si. Se o **mesmo PC** for reinstalado com a **mesma ISO**, a entrada
nova herda apelido, notas, SSH manual, permissões de sócio e o túnel WireGuard
(mesmo IP e mesmos acessos de dispositivos), e a antiga some. Só junta quando a
antiga avisou que desligou ou está parada há 15 min ou mais; se ainda não estava no
1º relatório, tenta de novo a cada relatório nas primeiras 2 h da entrada nova (o que
você já editou na nova é mantido). Assim VMs clonadas ligadas ao mesmo tempo não se
misturam. O IP da VPN (10.77.x.x) é do túnel, não da rede do cliente: o PC pode ter
ganhado outro IP na rede local que a VPN continua igual. Só o "acesso à rede local"
é desligado se a rede do PC reinstalado mudou (ou a placa mudou de nome); é só
ligar de novo na aba da VPN. Outra ISO, ou o mesmo pendrive em outro PC, continuam como máquinas
separadas. Pendrives gravados antes dessa versão não mandam o hash; neles a
entrada antiga fica vermelha até ser removida.

**Arquivar e esquecer.** "Arquivar" tira a máquina da lista sem apagar nada: ela
vai para **Arquivadas** (no fim da página). Se continuar ligada, os relatórios
chegam normalmente, mas ela continua arquivada, com terminal, VPN (túnel pausado no
hub, sem perder IP nem acessos de dispositivos) e acesso de sócios bloqueados.
**Restaurar** devolve tudo como era. **Esquecer de vez** apaga os dados, o túnel e
os acessos, e o painel passa a recusar os relatórios daquela instalação (HTTP 410).
Se o mesmo PC for reinstalado de propósito com o pendrive, ele ganha outra
identidade e aparece de novo como máquina nova; se a antiga estava só arquivada, a
reinstalada também entra arquivada. Se o mesmo PC for reinstalado com **outra ISO**,
a entrada antiga (parada) é **arquivada sozinha**, com uma anotação explicando, sem
juntar os dados (vale para ISOs geradas com o hash do hardware).

A lista mostra quem está **online** (relatório nos últimos 25 min). Clicar numa
máquina abre um **overlay** com:

| Aba | O que tem |
|---|---|
| Resumo | todas as informações acima, atualizadas a cada 15 s |
| Terminal | **terminal SSH no navegador**, um clique: o painel entra pela Tailnet com a chave dele, sem digitar IP, usuário ou senha. `sudo` pede a senha do servidor |
| Conectar do meu computador | botão para instalar o Tailscale no seu sistema; **"Abrir SSH no Terminal"** (`ssh://usuario@ip`) e o comando `ssh` pronto para copiar, com o IP já buscado |
| Configurações | apelido, notas, endereço/porta SSH manual, relatório completo, arquivar |

**Como o terminal no navegador funciona:**
- O container do painel roda o Tailscale em modo *userspace* (sem root, sem
  privilégios) e entra na Tailnet como `iso-painel` usando `TS_AUTHKEY`. O estado
  fica no volume, então a auth key só é usada na primeira vez.
- O painel tem uma chave SSH própria (`/data/panel/id_ed25519`). Nas ISOs com
  "Acesso pelo painel" ligado (padrão; fica no passo *Programas*), a chave pública
  vai para o servidor com restrições:
  `from="100.64.0.0/10,fd7a:115c:a1e0::/48",no-agent-forwarding,no-X11-forwarding,no-port-forwarding`.
  Ela **só é aceita vindo da Tailnet** e não permite túneis.
- A chave do painel entra como o usuário administrador. **Não** dá root: o `sudo`
  continua pedindo a senha. Ela também não conta como "chave do usuário" para a
  regra de desativar a senha.
- Só a conta admin vê as máquinas e abre terminal. A conexão do terminal
  (WebSocket) exige sessão de admin e a mesma origem.

**Para ativar o terminal:** crie uma auth key em
<https://login.tailscale.com/admin/settings/keys> (recomendado: tag `tag:iso-painel`,
*Pre-approved*) e ponha em `TS_AUTHKEY` no Coolify. Depois reinicie. Sem ela, a
lista e as instruções continuam funcionando; só o terminal no navegador fica
indisponível (o painel avisa).

Se a sua ACL do Tailscale restringe quem acessa quem, libere a porta 22 de
`tag:iso-painel` para os servidores, por exemplo:
`{"action":"accept","src":["tag:iso-painel"],"dst":["tag:server:22"]}`.

No servidor: `sudo uab-report -v` envia um relatório na hora e mostra a resposta;
`uab-status` mostra quando foi o último.

### WireGuard pelo seu servidor (ponto de encontro)

Alternativa (ou reserva) ao Tailscale, **sem depender de serviço externo**. O seu
servidor, onde roda o ISO Builder, vira o ponto de encontro. Cada servidor
instalado **conecta para fora** até ele, então funciona atrás de qualquer
roteador ou CGNAT do cliente, sem mexer na rede do hotel.

```text
 seu Mac/celular ──WireGuard──►  SEU SERVIDOR (hub, porta UDP)  ◄──WireGuard── servidor do hotel
   10.77.N.10                        10.77.0.1                                  10.77.N.1
                                                                               └─ rede do hotel (opcional)
                                                                                  192.168.0.0/24 → 10.200.N.0/24
```

**Como funciona:**
1. No assistente, passo *Acesso remoto*: marque **WireGuard pelo seu servidor**
   (e/ou Tailscale; os dois juntos = um é reserva do outro).
2. No primeiro boot, o servidor gera a **própria chave privada, que nunca sai
   dele**, e manda a chave pública no relatório ao painel. O painel reserva um
   slot `N` e devolve a configuração do túnel. O servidor valida a configuração
   (endereços só dentro da VPN, sem conflito com a rede local) e liga o
   `wg-quick@uabwg`, que sobe sozinho a cada boot. **A ISO não leva nenhuma chave
   WireGuard.**
3. Em **Máquinas → VPN (WireGuard)**:
   - **Gerar acesso** para um dispositivo seu (MacBook, iPhone…): mostra o
     **QR code** e o arquivo `.conf` para o app oficial do WireGuard. A chave
     privada aparece uma única vez e não fica no painel. Cada acesso pode ser
     **revogado**.
   - **Acesso à rede local do cliente** (opcional): escolha a rede do servidor
     (`/24`). Como vários clientes costumam usar a mesma faixa (`192.168.0.0/24`),
     cada um ganha um endereço próprio: no hotel com slot 5, `192.168.0.50` vira
     `10.200.5.50`. O servidor aplica `NETMAP`/`MASQUERADE` em até 10 minutos, no
     próximo relatório. Depois disso, gere o acesso do dispositivo de novo para
     incluir essa rota.
     Se a rede mudar de placa (ex.: a ponte `br0` da Virtualização pega o IP da
     `enp3s0`), o painel e o servidor passam a usar a placa nova sozinhos; se a rede
     sumir, só essa opção é desligada. Problema no acesso à rede local nunca derruba
     o túnel.
   - **Por que não conecta:** com o túnel desconectado, a aba mostra o motivo:
     máquina desligada, hub fora do ar, configuração recusada pelo servidor, ou
     rede do cliente bloqueando UDP (quando as outras máquinas conectam e só esta
     não) / porta ou DDNS do seu lado (quando nenhuma conecta).
4. **Terminal no navegador:** o painel usa o túnel automaticamente, pelo hub (proxy
   SOCKS5 com senha, só para destinos da VPN). Não precisa de Tailscale.

**Isolamento (firewall do hub):** cada dispositivo só alcança o servidor (e a
rede) da máquina para a qual foi gerado. Servidores **não** alcançam a sua casa,
os seus dispositivos nem outros servidores, e ninguém usa o hub para sair para a
internet.

**Para ativar:**
1. Escolha uma porta UDP (padrão `51822`) e **abra essa porta no seu roteador**,
   apontando para este servidor. O `wg-easy` existente (51821) não é usado nem
   alterado.
2. No Coolify, defina `HUB_ENDPOINT=<seu DDNS ou IP público>:51822`. Opcional:
   `HUB_PORT`, se usar outra porta.
3. Faça o Redeploy: o `docker-compose.yml` já traz o serviço **`wg-hub`**
   (WireGuard do kernel, só `NET_ADMIN`). Em **Sistema** aparece "Hub no ar".

**Limites e cuidados:**
- O acesso depende de o seu servidor e a internet de casa estarem no ar. Para
  máquinas críticas, gere a ISO com WireGuard **e** Tailscale.
- Até 254 máquinas por hub e 240 dispositivos por máquina.
- O app WireGuard do Mac/iPhone ativa **um túnel por vez**, ou seja, uma máquina
  por vez.
- Mudança de IP da sua casa: o servidor reresolve o DDNS sozinho quando fica sem
  contato.
- O WireGuard pelo hub vale para ISOs geradas pela **interface web** (ele depende
  do relatório ao painel).

**Testado:** hub num namespace de rede isolado, VM instalada com ISO gerada pelo
painel e um "Mac" simulado com a configuração gerada:
- registro automático do servidor e túnel conectado cerca de 10 s depois do registro;
- SSH do Mac pela VPN e terminal do painel pelo túnel;
- acesso à rede do cliente pelo endereço mapeado;
- isolamento: outro hotel, o hub, a internet e o servidor iniciando conexão com o Mac, tudo bloqueado;
- reboot do servidor: túnel e rede de volta sozinhos em cerca de 26 s.

### Instalação no servidor (Coolify + Traefik)

```bash
# 1. Variáveis no Coolify (ou .env ao lado do docker-compose.yml):
#    ADMIN_PASSWORD=<senha forte, 12+ caracteres>
#    HUB_ENDPOINT=<seu-ddns>:51822   (WireGuard pelo seu servidor; abra a porta UDP no roteador)
#    TS_AUTHKEY=<auth key do Tailscale para o painel>  (terminal pela Tailnet, opcional)
#    (opcionais) ADMIN_EMAIL, ISO_TTL_HOURS, MAX_STORED_ISOS, KEEP_BASE_ISO, TEMP_MAX_BUILDS, TS_HOSTNAME
# 2. Rota no Traefik:
sudo cp proxy.yml /data/coolify/proxy/dynamic/IsoBuilder.yaml
# 3. Subir:
docker compose up -d --build
```

Se o DNS de `*.gvtserver.online` não for coringa, crie o registro `iso` apontando para o servidor.
Cada ISO ocupa ~4 GB, e a ISO oficial em cache ocupa ~3–4 GB por versão. Os dados
ficam no volume `iso_builder_data`. O painel **Sistema** mostra o espaço livre e
permite baixar ou atualizar a ISO oficial.

### isoBuilder, gravador de pendrive (`helper/`)

Programa em Go, sem dependências e sem instalação. Abre uma página local
(`127.0.0.1` + token aleatório) no navegador.

| Arquivo | Sistema |
|---|---|
| `isoBuilder-Windows.exe` | Windows 10/11 x64 (pede permissão de administrador) |
| `isoBuilder-Windows-ARM64.exe` | Windows em ARM |
| `isoBuilder-macOS.zip` | macOS 11+, Intel e Apple Silicon (pede a senha do Mac na hora de gravar) |
| `isobuilder-linux-amd64` | Linux (`sudo`) |

**Unidade INSTALAR no pendrive.** Depois de gravar e verificar a ISO, o isoBuilder
cria no espaço livre uma partição FAT32 de 64 MB chamada **INSTALAR** com o
`Instalar-no-Windows.exe`, o `Instalar-no-Mac.app` (Mac Intel), o `instalar-linux` e um
`LEIA-ME.txt`. Quem recebe o pendrive abre o programa do seu sistema, digita FORMATAR e o
computador reinicia pelo pendrive: no Windows, direto pela opção USB da placa-mãe quando
ela existe (`bcdedit … bootsequence`), senão pela tela "Usar um dispositivo"; no Linux via
`efibootmgr --bootnext`; no Mac, segurando Option. As partições da ISO passam a ser do
tipo "Linux", então o Windows e o macOS não oferecem "formatar" o pendrive (o boot não
depende do tipo). Nada roda sozinho ao conectar o pendrive: isso não existe mais nos
sistemas atuais (AutoRun), e ainda bem.

Ícone: `helper/assets/icon.png` (+ `.icns` do Mac e `.ico` do Windows), gerados por
`python3 helper/assets/make-icon.py`. No Windows o ícone e o nome/versão vão dentro do
`.exe` (`helper/assets/winres.py`, sem ferramentas externas).

Proteções do gravador:
- lista só discos USB, SD ou removíveis, nunca o disco do sistema;
- no Windows, confere de novo se o disco não é do sistema antes de limpá-lo;
- grava o primeiro 1 MiB por último, para o Windows não montar partições no meio da gravação;
- **relê o pendrive e compara o SHA256** com a ISO;
- recusa ISO corrompida quando existe o `.sha256` ao lado.

Compilar localmente: `helper/build.sh` (gera `helper/dist/`). O Docker compila sozinho.

**Sem assinatura de código:** o Windows mostra "O Windows protegeu o computador"
(*Mais informações → Executar assim mesmo*) e o Mac mostra "desenvolvedor não identificado"
(*botão direito → Abrir*; no macOS 15+: *Ajustes → Privacidade e Segurança → Abrir Mesmo Assim*).
Para eliminar esses avisos é preciso um certificado de assinatura de código Windows
e uma conta Apple Developer (notarização).

---

## Criar o pendrive

Use um pendrive de 8 GB ou mais. **Todo o conteúdo do pendrive será apagado.**
Confira o dispositivo duas vezes: gravar no disco errado destrói os dados dele.

### macOS

```bash
diskutil list                                   # identifique o pendrive, ex.: /dev/disk4
diskutil unmountDisk /dev/disk4
sudo dd if=output/<hostname>-autoinstall.iso of=/dev/rdisk4 bs=4m status=progress
sync && diskutil eject /dev/disk4
```

(`rdisk` é bem mais rápido que `disk`. Se o macOS disser "disco ilegível" depois, clique em **Ignorar**.)
Alternativa gráfica: [balenaEtcher](https://etcher.balena.io/).

### Linux

```bash
lsblk -d -o NAME,SIZE,TRAN,MODEL               # identifique o pendrive (TRAN=usb), ex.: /dev/sdX
sudo umount /dev/sdX?* 2>/dev/null
sudo dd if=output/<hostname>-autoinstall.iso of=/dev/sdX bs=4M conv=fsync oflag=direct status=progress
sync
```

### Windows

- **Rufus** (<https://rufus.ie>): selecione o pendrive e a ISO → *Iniciar* →
  quando perguntar, escolha **"Gravar em modo Imagem DD"**. O modo ISO recria
  o boot e pode descartar as alterações.
- ou **balenaEtcher**, que sempre grava em modo imagem.

Verificar a ISO antes de gravar: `shasum -a 256 -c <hostname>-autoinstall.iso.sha256`
(macOS), `sha256sum -c ...` (Linux) ou `Get-FileHash .\<arquivo>.iso` (PowerShell).

> Ventoy e similares (boot via loopback) têm uma entrada automática em
> `loopback.cfg`, mas **não foram testados**. Prefira gravar em modo imagem.

---

## Instalação na máquina remota

1. Conecte o **cabo de rede**.
2. Dê boot pelo pendrive (tecla de menu de boot: F12, F11, F8, Esc… varia
   conforme o fabricante). Secure Boot pode ficar ligado.
3. Aparece o menu do GRUB. Em 10 s ele inicia sozinho
   **"Instalacao AUTOMATICA <hostname> (APAGA o disco de destino)"**.
4. O instalador roda sem perguntas (≈10–20 min), verifica o disco antes de
   apagar, instala e **reinicia sozinho**.
5. No primeiro boot, o serviço `uab-provision` aplica o endurecimento do SSH e
   instala os pacotes, o Tailscale e o Docker (≈5–10 min, conforme a internet).
   Se algo falhar (ex.: sem internet), ele tenta de novo a cada 60 s,
   indefinidamente.
   **Reiniciar no meio é seguro:** se o reinício pegar uma instalação de pacotes
   pela metade (o `dpkg` fica "interrompido" e recusaria qualquer instalação), o
   provisionador roda `dpkg --configure -a` sozinho e continua. Enquanto não
   termina, o painel mostra "Instalando (X de Y etapas): evite reiniciar" no Resumo
   e no Terminal, e o login por SSH mostra o mesmo aviso. Etapa que falha aparece
   com o motivo (ex.: "sem internet ou repositório fora do ar").

**Pendrive esquecido conectado?** Sem problema: o GRUB da ISO procura o
marcador `/boot/.uab-installed-<build>` gravado pela instalação e, se achar,
**inicia o sistema instalado em vez de reinstalar**. O mesmo menu ainda oferece
reinstalar manualmente. Mesmo assim, retire o pendrive quando puder.

Na tela de login do console aparecem o IP local e o IP Tailscale (útil se
alguém estiver perto da máquina).

---

## Primeiro acesso

Pelo IP Tailscale (funciona sempre, com ou sem MagicDNS):

```bash
ssh <usuario>@<tailscale-ip>          # ex.: ssh operador@100.87.14.62
```

Com **MagicDNS** ligado na Tailnet (Admin console → DNS → MagicDNS):

```bash
ssh <usuario>@<hostname>              # ex.: ssh operador@srv-arquivos-01
ssh <usuario>@<hostname>.<tailnet>.ts.net
```

Como descobrir o IP Tailscale: no [Admin console](https://login.tailscale.com/admin/machines),
ou em qualquer computador da Tailnet:

```bash
tailscale status | grep <hostname>
tailscale ip -4 <hostname>
```

Na rede local (mesma LAN): `ssh <usuario>@<ip-local>`.

Dica, no `~/.ssh/config` do seu computador:

```text
Host srv-arquivos-01
    HostName 100.87.14.62         # ou srv-arquivos-01 com MagicDNS
    User operador
    IdentityFile ~/.ssh/id_ed25519
```

Primeiros comandos no servidor:

```bash
uab-status                 # estado de rede, SSH, Tailscale, Docker e provisionamento
sudo tail -f /var/log/uab-provision.log
docker compose version     # se Docker foi instalado
```

---

## Tailscale

### Criar uma auth key para provisionamento

Em <https://login.tailscale.com/admin/settings/keys> → **Generate auth key…**:

| Opção | Recomendado | Por quê |
|---|---|---|
| Reusable | **Desligado** | uma chave por servidor; se vazar, não serve para outra máquina |
| Expiration | o suficiente até a instalação (ex.: 7–30 dias) | a chave só é usada no primeiro boot |
| Ephemeral | **Desligado** | nó efêmero some da Tailnet quando fica offline |
| Pre-approved | **Ligado** se a Tailnet exige aprovação de dispositivos | senão o servidor fica aguardando aprovação manual |
| Tags | **`tag:server`** (recomendado) | nós com tag **não têm expiração de chave de nó** e não ficam presos à sua conta pessoal |

Para usar tags, declare no *Access controls* (ACL) quem pode atribuí-las:

```json
"tagOwners": { "tag:server": ["autogroup:admin"] }
```

**Sem tag**, o nó herda seu usuário e a *key expiry* padrão (180 dias). Nesse
caso, no Admin console → Machines → servidor → `…` → **Disable key expiry**,
senão o servidor sai da Tailnet quando a chave expirar.

Tratamento da auth key pelo projeto: é digitada sem eco e exibida mascarada
(`tskey-au…abcd`); fica em `secrets/tailscale-authkey` (600, fora do Git); vai
para a ISO num arquivo separado (`/uab/secrets/`), **não** no `user-data` nem
nos logs do instalador; no servidor fica em `/etc/uab/tailscale-authkey` (600,
root), é passada como `tailscale up --auth-key=file:...` (nunca aparece na
linha de comando) e é **apagada com `shred`** assim que o login funciona.

### Adicionar outros dispositivos à Tailnet

Todo dispositivo que precisa acessar o servidor tem que estar na **mesma
Tailnet** (logado com a mesma conta/organização, ou com o servidor
compartilhado via *Share*).

| Dispositivo | Como |
|---|---|
| Outro Mac | App Store → "Tailscale" (ou `brew install --cask tailscale`) → Log in |
| Windows | <https://tailscale.com/download/windows> → instalar → Log in |
| Linux | `curl -fsSL https://tailscale.com/install.sh \| sh && sudo tailscale up` (ou pacote da distro) |
| iPhone/iPad/Android | App "Tailscale" na loja → Log in (para SSH use Termius, Blink, JuiceSSH etc.) |
| Pessoa de outra conta | Admin console → Machines → servidor → **Share…** |

Depois: `tailscale ping <hostname>` para testar.

### Autorização Tailscale × autenticação SSH

São **duas camadas independentes**, e você precisa das duas:

1. **Tailscale (rede):** decide se o seu dispositivo **alcança** o servidor.
   Está na mesma Tailnet e as ACLs permitem? Então os pacotes chegam na porta 22.
2. **SSH (login):** decide se você **entra** no servidor. Exige que a sua chave
   **pública** esteja em `~/.ssh/authorized_keys` do usuário no servidor.
   Senha está desativada.

Um notebook novo logado no Tailscale **alcança** o servidor, mas recebe
`Permission denied (publickey)` até a chave dele ser adicionada. Uma chave
autorizada não serve de nada se o dispositivo não estiver na Tailnet (ou na
mesma LAN).

---

## Programas: Docker, Coolify, Cockpit

No `./setup.sh` e no passo *Programas* do assistente web:

| Opção | Instala |
|---|---|
| Nenhum | só o sistema, SSH, Tailscale e os pacotes básicos |
| Docker + Compose | Docker Engine + Compose do repositório oficial (fingerprint da chave conferido) |
| Coolify | Docker + Coolify (instalador oficial `cdn.coollabs.io`), em `http://<ip>:8000` |
| Coolify + Cockpit completo (**padrão no assistente web**) | o anterior + Cockpit com todos os módulos, em `https://<ip>:9090` |

**Pacotes extras (estilo Ninite):** abaixo do campo "Pacotes extras", **Escolher da
lista** abre um catálogo por categoria (monitoramento, rede, terminal, arquivos,
discos, segurança, servidores, backup, hardware), com uma frase explicando cada
pacote. Marcar escreve o nome no campo, desmarcar apaga, e digitar no campo marca a
caixa. Todos os nomes do catálogo foram conferidos no Ubuntu 24.04 e 26.04. `nginx`
e `apache2` mostram aviso quando o Coolify está escolhido (portas 80/443). Os extras
são instalados numa etapa própria (`extra_packages`), depois dos básicos: um nome que
não existir naquela versão do Ubuntu é **ignorado com aviso** (aparece em
Provisionamento), sem travar a instalação.

**Cockpit completo:** usa o mesmo método do `host-mirror`. São 9 pacotes
instalados numa única transação a partir do `-backports`: `cockpit`,
`cockpit-bridge`, `cockpit-machines`, `cockpit-files`, `cockpit-networkmanager`,
`cockpit-packagekit`, `cockpit-storaged`, `cockpit-podman` e
`cockpit-sosreport`. O `cockpit-files` exige um `cockpit-bridge` mais novo, que
na 24.04 só existe no `-backports`. O `cockpit-pcp` fica de fora porque conflita
com esse `cockpit-bridge`. O login usa o usuário e a senha do servidor.

**Coolify: cuidados aplicados automaticamente (acesso remoto em primeiro lugar):**
- **Admin criado na instalação.** Você informa e-mail e senha (12+ caracteres,
  com maiúscula, minúscula, número e símbolo). O Coolify cria o admin e **fecha o
  cadastro público**; sem isso, o primeiro visitante da porta 8000 viraria admin.
  O e-mail precisa ter domínio real (o Coolify confere o DNS), e a senha não pode
  constar em vazamentos conhecidos. A senha vai na ISO (0600) e é **apagada** do
  servidor depois do uso. O provisionamento confere que o cadastro foi fechado.
- **Rede do Docker isolada.** O instalador do Coolify usa 10.0.0.0/8 por padrão,
  o que pode conflitar com uma rede local 10.x e cortar o acesso. Aqui ele recebe
  `172.30.0.0/16`.
- **Root só para o Coolify.** O Coolify gerencia o próprio servidor entrando como
  `root` a partir dos containers dele. O SSH libera `root` **só por chave e só
  vindo das redes do Docker** (`Match Address 172.16.0.0/12` →
  `PermitRootLogin prohibit-password`). Vindo da rede local, do Tailscale ou da
  internet, `root` continua bloqueado. Isso é conferido com `sshd -T` e desfeito
  se algo sair errado.

Testado em VM (Coolify + Cockpit, 38 verificações): Cockpit com os 11 pacotes
`cockpit-*`, Coolify respondendo, admin criado e cadastro fechado, rede isolada,
`root` bloqueado da rede local e, **depois do reboot, rede, DNS, Cockpit e Coolify
voltando sozinhos** (o NetworkManager do Cockpit não interfere no netplan).

## SSH por usuário e senha (opcional)

Se você não quiser lidar com chaves, escolha **"Usuário e senha"** no `./setup.sh`
ou no assistente web (passo *Acesso*). Aí:

```bash
ssh <usuario>@<servidor>         # digita a senha
sudo <comando>                   # permissão de administrador (root), mesma senha
sudo -i                          # abre um shell de root
```

Proteções aplicadas automaticamente nesse modo:

| Proteção | Por quê |
|---|---|
| Senha aceita **só** da rede local (10/8, 172.16/12, 192.168/16) e do **Tailscale** (100.64/10) | Se a porta 22 for aberta para a internet, os robôs que testam senhas o tempo todo não conseguem nem tentar. De IPs públicos, só chave. |
| Login direto como `root` bloqueado | `root` é o usuário mais atacado. Você tem o mesmo poder com `sudo`. |
| **fail2ban** ativo | Bloqueia por um tempo o IP que errar a senha várias vezes. |
| `MaxAuthTries 4`, `LoginGraceTime 30` | Menos tentativas por conexão. |
| Senha com no mínimo 12 caracteres | Senha curta é quebrada rápido. |

Chaves SSH continuam aceitas também nesse modo: dá para adicionar uma depois com
`uab-add-ssh-key`. Para mudar de modo num servidor já instalado, edite
`/etc/ssh/sshd_config.d/10-uab-hardening.conf` e rode `sudo systemctl restart ssh`.

## Extras (os recursos do host-mirror)

Opcionais, no passo **Programas → Extras** do assistente web (ou no `./setup.sh`).
Tudo é instalado pelo provisionador no primeiro boot e aparece em **Máquinas**.

| Extra | O que faz | Você informa |
|---|---|---|
| **DDNS No-IP** | `ddclient` mantém o nome apontando para o IP público (a cada 5 min) | hostname, usuário e senha do No-IP |
| **Cloudflare Tunnel** | `cloudflared` do repositório oficial (chave conferida) conecta o túnel; os domínios ficam no painel da Cloudflare | token do túnel (pode colar o comando inteiro) |
| **GPU NVIDIA** | detecta a placa, instala o driver recomendado (`ubuntu-drivers`), liga a GPU no Docker/Coolify **já instalado** (`nvidia-container-toolkit`) e reinicia **uma vez** sozinho para carregar o driver | nada |
| **Virtualização (KVM)** | libvirt/QEMU, UEFI e TPM para VMs, pastas `default` e `isos`; usuário nos grupos `libvirt`/`kvm`; com Cockpit, VMs pelo navegador | nada |
| **Ponte br0** | a placa principal entra na ponte `br0` (mesmo MAC e endereço), e as VMs recebem IP do seu roteador. Se a rede não voltar em 90 s, **desfaz sozinha** | nada |
| **Ajustes de servidor** | `fsck` automático no boot (repara após queda de energia), console sem apagar e, com virtualização, IOMMU conforme a CPU | nada |

Segredos: a senha do No-IP e o token vão na pasta protegida da ISO, são gravados só
em `/etc/ddclient.conf` e `/etc/cloudflared/token.env` (0600, root; o token nunca vai
na linha de comando) e a cópia de `/etc/uab` é apagada. Sem GPU NVIDIA na máquina, a
etapa só registra "nenhuma GPU encontrada". Com **Secure Boot** ligado, o driver usa
os módulos assinados do Ubuntu. Se não houver módulo assinado para a placa, desligue
o Secure Boot na BIOS.

Não incluídos, de propósito: os scripts de backup para um 2º disco (dependem de um
disco escolhido na hora e são destrutivos) e a passagem de GPU para VMs.

## Remoto (MeshCentral) em `/remoto/`

A stack do ISO Builder tem um serviço **remoto** (pasta `remote/`): um MeshCentral com o
visual do painel, em **https://iso.gvtserver.online/remoto/**. É independente do remote
antigo (dados próprios, começa vazio).

- **Login único:** a página **Acesso remoto** do painel abre o remoto já logado (token de
  60 min assinado com a chave de login do MeshCentral, que o container publica num volume
  só entre os dois). Direto: usuário `admin`, senha = `ADMIN_PASSWORD`.
- **Servidores aparecendo sozinhos:** com **Programas → Extras → Aparecer no Remoto**, a ISO
  instala o agente no 1º boot, no grupo "Servidores ISO Builder" (criado sozinho). É a 2ª rota
  para chegar ao servidor, além de **Máquinas**.
- **Rota:** o router `iso-remoto` do `proxy.yml` manda `/remoto` para o serviço `remoto` (porta 4021).
- **Visual:** `remote/web/public/styles/isobuilder.css` (claro/escuro, laranja do painel),
  sobre a interface moderna do MeshCentral (`siteStyle: 3`).

## Sócios (acesso a máquinas escolhidas)

Em **Acessos → Sócio (permanente)** você cria uma conta sem prazo (prefixo ou e-mail completo
de qualquer domínio). O sócio **não** gera ISOs, não cria acessos e não adiciona computadores:
ele só vê as máquinas que você liberar em **Permissões**, com estes itens por máquina:

| Item | O que libera |
|---|---|
| Ver | aparece em Máquinas (Resumo, Conectar) |
| Terminal | terminal do painel e do Remoto |
| Tela / Arquivos | pelo Remoto (a máquina precisa do agente) |
| VPN | gerar acessos WireGuard daquela máquina |
| Configurações | nome, notas e endereço SSH |

**Admin** marca tudo de uma vez (dá para desmarcar itens depois). Computadores que só existem
no Remoto (adicionados por convite) também aparecem para liberar. No Remoto, o painel cria a
conta `socio-<id>` no MeshCentral e aplica as mesmas permissões por aparelho — o próprio
MeshCentral bloqueia o que não foi liberado. Excluir o sócio remove também essa conta.

**Transformar uma conta temporária em sócio.** Em **Acessos**, no menu **⋯** de uma conta
"só download" ou "pode gerar ISOs" (mesmo vencida ou revogada), **Tornar sócio** mantém o
login e a senha, tira a validade e reativa a conta. Como sócio, ela não baixa nem gera
ISOs; as ISOs que ela gerou passam para o admin, e os PCs delas continuam aparecendo. Em
seguida abre o editor de **Permissões** com **Ver** já marcado nas máquinas instaladas
com as ISOs que a pessoa recebeu. Nada é liberado até você clicar em Salvar.

## Porta do SSH

O padrão é a **22**. Para usar outra, preencha **Porta do SSH** no assistente web
(passo *Acesso*) ou responda no `./setup.sh` (`SSH_PORT` em `config/install.env`).
Vale 22 ou de 1024 a 65535, exceto 6001, 6002, 8000, 8080 e 9090 (usadas pelo
Coolify, Traefik e Cockpit).

```bash
ssh -p 2222 <usuario>@<servidor>
```

No primeiro boot o provisionador (passo `ssh_port`):

- grava `Port N` em `/etc/ssh/sshd_config.d/05-uab-port.conf`, confere com `sshd -t`/`sshd -T`
  e recarrega o `ssh.socket` (Ubuntu 24.04+ usa ativação por socket);
- só dá como pronto se o SSH **estiver escutando** na porta nova; senão desfaz e o SSH
  continua na 22 (você não perde o acesso);
- ajusta o **fail2ban** para vigiar a porta nova e, com Coolify, o servidor `localhost`
  do Coolify (que gerencia a máquina por SSH).

Até o provisionamento terminar (alguns minutos após o primeiro boot), o SSH fica na 22.
O painel descobre a porta real pelo relatório da máquina (terminal e "Conectar").

## SSH: chaves de novos computadores

Como a senha está desativada no SSH, `ssh-copy-id` **não funciona** a partir
de um computador novo. Use um computador que já tem acesso:

```bash
# 1. No computador NOVO: gere a chave (se não tiver) e mostre a pública
ssh-keygen -t ed25519 -C "notebook-novo"
cat ~/.ssh/id_ed25519.pub            # Windows: type $env:USERPROFILE\.ssh\id_ed25519.pub

# 2. Num computador que JÁ acessa o servidor
ssh <usuario>@<servidor> "uab-add-ssh-key 'ssh-ed25519 AAAA... notebook-novo'"
# ou, com o arquivo .pub em mãos:
ssh <usuario>@<servidor> uab-add-ssh-key - < notebook-novo.pub
```

Ferramentas instaladas no servidor (código em `server-tools/`):

| Comando | Faz |
|---|---|
| `uab-list-ssh-keys` | lista as chaves com número e fingerprint |
| `uab-add-ssh-key 'ssh-ed25519 …'` / `-f arquivo.pub` / `-` (stdin) | valida (recusa chave privada e formato inválido), evita duplicata, faz backup e adiciona |
| `uab-remove-ssh-key <número\|SHA256:…>` | faz backup, pede confirmação e **recusa remover a última chave** |
| `uab-status` | estado geral (use `sudo` para ver tudo) |

Adicione a chave nova e **teste o login** antes de remover uma chave antiga.
Para incluir a chave já na próxima ISO: rode `./setup.sh` e escolha
"Adicionar outra chave pública".

---

## Como funciona

```text
ubuntu-autoinstall-builder/
├── setup.sh                 assistente interativo → build
├── build.sh                 gera a ISO a partir de config/ + secrets/
├── validate.sh              [OK]/[FALHA] por área
├── test-iso.sh              teste ponta a ponta em VM QEMU/KVM descartável
├── config/
│   ├── install.example.env  exemplo comentado
│   ├── install.env          (gerado; fora do Git)
│   └── authorized_keys      (gerado; chaves públicas; fora do Git)
├── templates/
│   ├── user-data.yaml       base do Autoinstall
│   └── meta-data.yaml
├── schema/
│   └── autoinstall-schema.json   schema oficial do Subiquity (canonical/subiquity)
├── scripts/
│   ├── common.sh            funções, prompts, validação de chave
│   ├── check-deps.sh        detecta/instala dependências
│   ├── collect-config.sh    perguntas do assistente
│   ├── generate-password.sh senha → hash SHA-512
│   ├── validate-config.sh   valida configuração e segredos
│   ├── download-ubuntu.sh   download + GPG + SHA256
│   ├── generate-autoinstall.sh  monta nocloud/ e payload
│   ├── customize-iso.sh     inspeciona a ISO; gera grub.cfg/loopback.cfg/md5sum.txt
│   ├── build-iso.sh         xorriso -boot_image any replay
│   ├── verify-iso.sh        verifica a ISO gerada
│   └── lib/uab.py           renderização do YAML, validações, GRUB
├── payload/                 copiado para /uab na ISO
│   ├── preflight.sh         early-commands: verificação do disco
│   ├── late-install.sh      late-commands: instala o provisionador
│   ├── on-error.sh          error-commands: mostra a falha no console
│   ├── uab-provision.sh     serviço de primeiro boot (SSH, pacotes, Tailscale, Docker)
│   ├── systemd/uab-provision.service
│   └── README-SERVER.txt    referência rápida (fica em /opt/uab no servidor)
├── server-tools/            viram /usr/local/sbin/uab-* no servidor
├── secrets/  cache/  work/  output/     (fora do Git)
```

Conteúdo adicionado à ISO oficial (o resto fica **idêntico**):

```text
/nocloud/user-data, meta-data, vendor-data    cloud-init NoCloud com a chave autoinstall:
/uab/…                                        preflight, late-install, payload, BUILD_INFO
/uab/secrets/tailscale-authkey                (se Tailscale)
/boot/grub/grub.cfg, loopback.cfg             entrada autoinstall + proteção anti-reinstalação
/md5sum.txt                                   atualizado para os arquivos alterados
```

**Boot.** A ISO oficial foi inspecionada (`xorriso -report_el_torito as_mkisofs`,
salvo em `output/<hostname>-boot-report.txt`): El Torito BIOS
(`/boot/grub/i386-pc/eltorito.img`), imagem EFI numa partição GPT anexada,
MBR protetivo/híbrido. A ISO nova é feita com
`xorriso -indev oficial.iso -outdev nova.iso -map … -boot_image any replay`,
que **reproduz exatamente** esses parâmetros, sem montar parâmetros de boot à
mão. O `verify-iso.sh` confere que eles continuam iguais.

**Linha do kernel.** `linux /casper/vmlinuz autoinstall 'ds=nocloud;s=/cdrom/nocloud/' ---`.
No GRUB o `;` separa comandos, por isso o argumento vai entre aspas simples e o
kernel recebe `ds=nocloud;s=/cdrom/nocloud/`. O `autoinstall` dispensa a
confirmação interativa. Kernel e initrd saem do `grub.cfg` original, não são
fixados no código.

**Instalação** (`user-data`): `interactive-sections: []`, locale/teclado/timezone,
`identity` com hash SHA-512, `ssh.install-server` + chaves, netplan (DHCP com
`match: name: en*`/`eth*` ou IP estático com `match` por MAC/nome),
`storage.layout` LVM (`sizing-policy: all`) ou ext4 com `match` do disco,
`apt.fallback: offline-install` (instala mesmo sem internet), `updates: security`,
`shutdown: reboot`.

**Primeiro boot** (`uab-provision.service`, `Restart=on-failure` a cada 60 s,
sem limite de tentativas; etapas idempotentes em `/var/lib/uab/`):

1. `ssh_keys`: espera o cloud-init criar o usuário; garante as chaves em
   `authorized_keys` (600) e confirma que ao menos uma é válida (`ssh-keygen -l`).
2. `ssh_harden`: **só depois disso** grava
   `/etc/ssh/sshd_config.d/10-uab-hardening.conf`
   (`PermitRootLogin no`, `PubkeyAuthentication yes`, `PasswordAuthentication no`,
   `KbdInteractiveAuthentication no`), valida com `sshd -t` e `sshd -T` e reverte
   se algo der errado. O arquivo `10-` prevalece sobre o `50-cloud-init.conf`.
3. rede: espera DNS e HTTP.
4. `base_packages`: pacotes padrão + extras.
5. `tailscale_install`: repositório oficial `pkgs.tailscale.com`, com o
   fingerprint da chave GPG conferido; `tailscaled` habilitado.
   `tailscale_login`: `tailscale up --auth-key=file:… --hostname=<hostname>`;
   apaga a chave em seguida.
6. `docker_install`: repositório oficial `download.docker.com` (fingerprint
   `9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88`), remove pacotes
   conflitantes, instala `docker-ce docker-ce-cli containerd.io
   docker-buildx-plugin docker-compose-plugin`, habilita no boot, põe o usuário
   no grupo `docker` e confere `docker version` e `docker compose version`.

Uma falha no Docker não impede o Tailscale e vice-versa. SSH, Tailscale,
Docker e a rede (systemd-networkd/netplan) sobem sozinhos no boot.

---

## Segurança

- **Nunca vão para o Git** (`.gitignore`): `secrets/`, `config/install.env`,
  `config/authorized_keys`, `cache/`, `work/`, `output/`, `*.iso`, chaves e
  auth keys.
- `secrets/` fica com 700; arquivos de segredo, a configuração e a ISO final
  com **600**; `umask 077` em todos os scripts.
- **Senha:** lida sem eco, confirmada, passada ao `openssl passwd -6` pelo
  **stdin** (nunca como argumento de linha de comando); as variáveis são
  apagadas logo depois. Só o hash `$6$` é gravado.
- **Chave SSH:** só a **pública** é lida. Arquivos com `PRIVATE KEY` são
  recusados no setup, no validate, no build e nas ferramentas do servidor.
  RSA abaixo de 3072 bits é recusada.
- **A ISO é sensível:** contém o hash da senha e, se usar Tailscale, a auth key.
  Guarde a ISO e o pendrive como guardaria uma senha; use uma auth key de uso
  único e com expiração curta. `./build.sh --forget-secrets` apaga a auth key
  local depois de gerar.
- Downloads verificados: ISO (GPG + SHA256), chaves dos repositórios Docker e
  Tailscale (fingerprint conferido antes de confiar).
- `config/install.env` só aceita linhas `KEY='valor'` (nada de código).

---

## Recuperação

Primeiro, veja o estado: `uab-status` (via SSH na LAN ou no console).
Logs: `/var/log/uab-provision.log`, `journalctl -u uab-provision`,
`/var/log/installer/` (instalação), `/var/log/uab-preflight.log`.

**Tailscale não conecta / servidor não aparece na Tailnet**

- Causas comuns: auth key expirada, já usada (não reutilizável), revogada,
  tag sem `tagOwners` na ACL, ou Tailnet exigindo aprovação (key sem *Pre-approved*).
- O provisionador tenta de novo a cada 60 s. Veja o motivo em
  `sudo grep -i tailscale /var/log/uab-provision.log`.
- Admin console → Machines: o servidor pode estar lá aguardando **aprovação**
  ou com nome `<hostname>-1` (nome duplicado de uma instalação anterior; apague
  a máquina antiga e rode `sudo tailscale set --hostname=<hostname>`).
- Correção por SSH na LAN ou no console:
  `sudo tailscale up --hostname=<hostname>` (mostra um link de login) ou
  `sudo tailscale up --auth-key=<nova-key> --hostname=<hostname>`.
- Firewall da rede bloqueando UDP: o Tailscale usa relays DERP (HTTPS 443),
  então costuma funcionar mesmo assim, só que mais lento. `tailscale netcheck`
  mostra o diagnóstico.

**DHCP falha (sem IP)**

- Confira cabo e link (`ip -br link`), e se o roteador tem DHCP ligado.
- No console: `ip -br addr`, `sudo networkctl status`, `cat /etc/netplan/*.yaml`.
- IP temporário: `sudo ip addr add 192.168.1.50/24 dev <if> && sudo ip route add default via 192.168.1.1`.
- Permanente: edite `/etc/netplan/50-cloud-init.yaml` e rode `sudo netplan apply`.
- Interface Wi-Fi não é configurada (use cabo).

**SSH não inicia / não aceita a chave**

- Console: `systemctl status ssh.socket ssh.service`, `sudo sshd -t`.
- `Permission denied (publickey)`: a chave do seu computador não está no
  servidor → veja [SSH: chaves de novos computadores](#ssh-chaves-de-novos-computadores).
  Teste com `ssh -v` para ver qual chave está sendo oferecida.
- Se o endurecimento não foi aplicado, a senha continua aceita; veja
  `sudo cat /var/lib/uab/status`.
- Para reabrir a senha **temporariamente** (console): `sudo rm /etc/ssh/sshd_config.d/10-uab-hardening.conf && sudo systemctl restart ssh`.

**Autoinstall falha**

- Aparece "UAB: a instalacao automatica FALHOU" no console, com as últimas
  linhas do log. Nenhuma etapa seguinte é executada.
- `UAB-PREFLIGHT: ERRO …` = problema com o disco (não encontrado, ambíguo,
  USB, pequeno demais). **Nada foi apagado.** Rode `lsblk -d -o NAME,SIZE,TRAN,MODEL,SERIAL`
  (`Ctrl+Alt+F2`), ajuste no `./setup.sh` e gere a ISO de novo.
- Outros erros: `/var/log/installer/subiquity-server-debug.log` e `curtin-install.log`.
  Para copiar os logs para outro pendrive: `Ctrl+Alt+F2`, monte o pendrive e
  copie `/var/log/installer`.

**Instalação parece parada**

- Sem internet, o instalador espera o timeout do mirror e depois segue offline
  (`fallback: offline-install`). Isso pode levar alguns minutos.
- `Ctrl+Alt+F2` → `tail -f /var/log/installer/subiquity-server-debug.log`.
- Se a máquina voltou ao menu do GRUB depois do reboot: é a proteção
  anti-reinstalação. Ela inicia o sistema instalado sozinha em 10 s.
- Firmware que não dá boot pelo pendrive: desative *Fast Boot*/*CSM* conforme
  o caso ou use o menu de boot do fabricante. Com Secure Boot ligado, a ISO usa
  o shim assinado pela Canonical.

**Servidor instalado, mas o provisionamento não terminou**

- `sudo systemctl status uab-provision`, `sudo cat /var/lib/uab/status`.
- Refazer tudo: `sudo rm /var/lib/uab/*.done /var/lib/uab/status && sudo systemctl restart uab-provision`.

**Reinstalar a mesma máquina**

- Boot pelo pendrive → escolha "Instalacao AUTOMATICA …" no menu (o padrão
  seria iniciar o sistema instalado, por causa do marcador). Uma ISO gerada de
  novo tem outro build id e reinstala direto.

---

## Limitações conhecidas

- Só **amd64** (x86-64). ARM64 não é suportado.
- A rede tem que ser cabeada (Ethernet). Wi-Fi não é configurado.
- Sem criptografia de disco (LUKS): exigiria digitar a senha no console a cada boot.
- O disco é descoberto na máquina remota. O builder não tem como listar os
  discos dela; por isso o preflight aborta em qualquer ambiguidade.
- Bonds, VLANs, bridges e IPv6 estático não estão no assistente (dá para
  editar a seção `network` em `scripts/lib/uab.py`).
- A ISO carrega a auth key do Tailscale e o hash da senha. Proteja o pendrive.
- No modo "usuário e senha", a segurança depende da força da senha. Na rede
  local e no Tailscale ela é aceita normalmente; use uma senha longa.
- Enquanto o provisionador não confirma a chave SSH (alguns segundos após o
  primeiro boot), o SSH aceita senha na rede local.
- Boot por loopback (Ventoy) não foi testado.
- O teste em VM usa DHCP e o disco virtual; ele não testa IP estático nem o
  critério real do seu disco (o `--abort-test` testa a proteção).
- Testes completos em VM feitos com 24.04.5 (UEFI e BIOS) e 26.04.1 (UEFI).
  Hardware físico varia (firmware, placas de rede); faça um teste com a
  máquina real antes de levá-la para longe.
