# Windows 11 no painel (parte C)

O que o painel faz para o Windows 11 Pro. O contrato entre as partes está em `CONTRACT.md`.

## Assistente (Nova ISO)

- O 1º passo é **Qual sistema?**, com três cartões: Ubuntu Server 24.04, Ubuntu Server 26.04 e
  Windows 11 Pro. O cartão do Windows fica desativado se o gerador (`web/app/winconfig.py`) não
  estiver instalado (`/api/me` → `features.windows`).
- Etapas do Ubuntu: Sistema, Servidor, Acesso, Rede, Disco, Acesso remoto, Programas, Revisar
  (as mesmas de antes; a versão passou do antigo 1º passo para os cartões).
- Etapas do Windows: Sistema, Identificação (nome do PC com 1 a 15 caracteres, usuário local,
  senha, chave opcional com máscara), Região (pt-BR, teclado, fuso), Disco (aviso), Acesso
  (OpenSSH/terminal pelo painel e agente do Remoto), Programas (catálogo winget com logos),
  Revisar.
- O catálogo, os fusos e os teclados vêm de `GET /api/windows/apps` (`core.win_apps()` lê
  `winconfig.load_apps()`, e as listas vêm de `core.WIN_TIMEZONES` e `core.WIN_KEYBOARDS`).

## Backend

- `core.validate_build(d)` com `os: "windows"` chama `validate_windows`, que devolve
  `{"os", "config", "secrets": {"password", "win_key"}, "panel_access", "win_key_set"}`.
  Chave em branco vira a chave genérica do Pro (sem ativação).
- Campos do POST `/api/builds`: `os, hostname, username, realname, password, win_key,
  win_timezone, win_keyboard, panel_access, remote_agent, win_apps, confirm_erase` e,
  opcionalmente, `deliver`.
- `builds.os` (migração: `'ubuntu'` por padrão). Um build Windows fica com `series = "11"` e gera
  o `autounattend.xml` na hora (`builder.make_windows`) em `jobs/<id>/output/` (arquivo 0600).
  O status vai direto para `done`, sem passar pela fila da ISO.
- `ctx` do gerador: `panel_url` (`AGENT_URL` ou `PUBLIC_URL`), `report_token` (novo; no banco
  fica só o hash), `panel_pubkey` (a chave pública do painel, **sem** `from=`, porque o Windows
  ainda não tem túnel), `remote_agent_url` = `<panel_url>/remoto` e `remote_meshid`.
- Validade e limpeza iguais às da ISO. As configurações do Windows não contam no limite
  `MAX_STORED_ISOS`, que continua valendo para as ISOs grandes.
- Erros do gerador são registrados só com o tipo da exceção (nunca com a senha nem com a chave).

## Endereços

| Endereço | Ubuntu | Windows |
|---|---|---|
| `GET /download/iso/{id}` | ISO | 404 |
| `GET /download/config/{id}` | 404 | autounattend.xml (avançado: Ventoy, Rufus) |
| `GET /download/sha256/{id}` | da ISO | do XML |
| `GET /download/bundle/{id}/{plataforma}` | JSON com `kind: "ubuntu"` | JSON com `kind: "windows"` (contrato §3) |
| `GET /t/{token}/iso` | ISO | 404 |
| `GET /t/{token}/config` | 404 | XML (410 se o link ou o arquivo expirou) |

Contas "só download" funcionam da mesma forma com um build Windows.

## Máquinas

- `platform` vem do relatório (`"windows"`, ou `"linux"` quando o campo não vem) e `build_os` vem
  do build. O logo aparece por `osLogo`.
- No Windows, o Resumo mostra o nome do computador, a versão (em vez do kernel), o disco C:, os
  serviços OpenSSH e Remoto e as etapas `openssh`, `remote_agent`, `apps` e `report`.
- A aba VPN fica escondida no Windows.
- O terminal usa o OpenSSH do Windows (PowerShell). Sem Tailscale/WireGuard, o painel usa o
  endereço definido em Configurações; sem endereço, o aviso sugere a aba Tela (Remoto).

## Ícones

`python3 web/tools/make-pkg-icons.py` também gera `window.WIN_ICONS` a partir do campo `icon` de
`windows/apps.json`: um slug do Simple Icons ou `lucide:<nome>`. Um slug que não existe vira um
ícone genérico, com aviso. Rode de novo sempre que `apps.json` mudar.
