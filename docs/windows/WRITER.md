# Windows 11 no isoBuilder (gravador) — parte B

O gravador (`helper/`, Go puro, sem CGO) agora também grava o pendrive de instalação
automática do **Windows 11**. O painel só entrega a configuração (`autounattend.xml`); a
ISO vem **direto da Microsoft**, no PC do cliente, ou de um arquivo que o usuário já tenha.

## Bundle (JSON embutido)

Mesmo formato de anexo de sempre (`<json><8 bytes tamanho BE>"UABCFG01"` no fim do
executável, ou `isoBuilder.app/Contents/Resources/uab-download.json` no macOS). Para
Windows, o JSON do contrato (§3):

```json
{"kind": "windows", "server": "https://iso.exemplo", "token": "<32+>",
 "config_name": "<host>-autounattend.xml", "sha256": "<64 hex>", "size": 12345,
 "hostname": "<host>", "expires_at": 1234567890,
 "windows": {"version": "11", "edition": "Pro", "lang": "pt-BR", "arch": "x64"}}
```

Validação (`Bundle.valid`, `helper/bundle.go`): servidor `https://`, token ≥ 32,
`config_name` = `^[a-z0-9-]+-autounattend\.xml$`, `size` ≤ 4 MB, `version` = `"11"`,
`arch` ∈ {`x64`, `arm64`}, `lang` no formato BCP-47 simples, `edition` só letras/espaços.
Sem `kind` ou com `"kind": "ubuntu"`: exatamente como antes. `kind` desconhecido = bundle
recusado (gravador antigo não tenta gravar algo que não entende).

`edition` aceita o nome do painel (`Pro`, `Home`, `Education`, `Enterprise`, `Pro Education`,
`Pro for Workstations`) ou diretamente um `EDITIONID` do WIM (ex.: `EnterpriseEval`, usado
nos testes com a ISO de avaliação).

## Fluxo

1. `GET {server}/t/{token}/config` → confere tamanho e SHA256 do bundle. 404/410/401/403 =
   "link expirou". O XML fica numa pasta temporária 0600 (tem a senha) e é apagado ao fechar.
2. ISO da Microsoft (`helper/msdownload.go`), o mesmo fluxo público do site (como o Fido):
   - `GET https://www.microsoft.com/en-us/software-download/windows11` (arm64: `windows11arm64`):
     ID da edição (`<option value="3321">Windows 11 (multi-edition ISO for x64 devices)`) e
     a tabela **"hash values for each language"** (SHA256 por idioma, ex.
     `Brazilian Portuguese 64-bit`);
   - sessão: GUID novo registrado em `ov-df.microsoft.com/mdt.js?instanceId=560dc9f3-…&pageId=si&session_id=…`
     (+ o pedido que o próprio mdt.js faria, com `mdt`/`rticks`) e em
     `vlscppe.microsoft.com/tags?org_id=y6jn8c31&session_id=…`. **Sem o registro no ov-df a
     Microsoft responde `SentinelReject`** (testado);
   - `software-download-connector/api/getskuinformationbyproductedition?profile=606624d44113&ProductEditionId=…`
     → SKU do idioma (mapa `pt-BR` → `Brazilian Portuguese` etc. em `msLangNames`);
   - `…/GetProductDownloadLinksBySku?…&SKU=…` com `Referer` da página → link temporário (24 h)
     `software.download.prss.microsoft.com/…/Win11_25H2_BrazilianPortuguese_x64_v2.iso`;
   - download em `Downloads/` com `.part`, **retomada por Range**, até 40 tentativas; link
     vencido (403/404/410) → pede outro;
   - conferência: **SHA256 da tabela da Microsoft** (se achou o idioma na tabela) + conferência
     da estrutura (abaixo). Arquivo já baixado com o mesmo nome/tamanho/hash é reaproveitado.
   - Bloqueio (`SentinelReject`, código 715-123130; acontece com VPN/proxy/datacenter) ou
     qualquer falha → mensagem clara e a opção **"Já tenho a ISO"**.
3. "Já tenho a ISO": lista os `.iso` de Downloads/Área de Trabalho/Documentos (e pasta do
   programa) ou caminho colado. Conferência (`inspectWindowsISO`, `helper/winiso.go`): UDF
   legível, `bootmgr.efi`, `sources/boot.wim`, `efi/boot/bootx64.efi` (arm64: `bootaa64.efi`),
   `sources/install.wim|esd`, e no XML do WIM: build ≥ 22000 (Windows 11), `ARCH` certo, a
   **edição** pedida (`EDITIONID`, ex. `Professional`) e o **idioma** (`<DEFAULT>`) igual ao da
   configuração. Depois calcula o SHA256 (e confere com `arquivo.iso.sha256`, se existir).
4. Gravação (`runWriteWindows`): pendrive ≥ tamanho da ISO + 5% + 64 MB (na prática 16 GB).

## Pendrive gravado

- **MBR** com **uma partição FAT32** (tipo `0x0C`, ativa, CHS do início calculado para
  255×63 e `FE FF FF` além do cilindro 1023) de 1 MiB até o fim do pendrive (rótulo `WIN11`),
  cluster padrão do Windows (4–32 KB), área de dados alinhada a 1 MiB. **Inicia nos dois
  modos**, sem opção a escolher:
  - **UEFI**: o firmware inicia `\EFI\BOOT\BOOTX64.EFI`, assinado pela Microsoft (funciona
    **com Secure Boot ligado**);
  - **BIOS antiga (Legacy/CSM)**: código de boot **próprio** (assembly 16-bit, `helper/bootcode/`,
    ver abaixo) na MBR e no setor de boot, que carrega o `bootmgr` da raiz da ISO; ele lê
    `\boot\BCD` e inicia o WinPE de `\sources\boot.wim`. Daí em diante é igual ao UEFI: o
    `pe.js` vê `PEFirmwareType = 1` e instala em **MBR** com `bcdboot /f BIOS`.
    Em PCs sem UEFI o Windows 11 não é suportado oficialmente pela Microsoft (a tela final e o
    `LEIA-ME.txt` avisam).
- Todos os arquivos da ISO, com nomes 8.3 exatos quando possível (`EFI\BOOT\BOOTX64.EFI`) e
  nomes longos (LFN) para o resto.
- `sources/install.wim` > 4 GiB-1 → **`install.swm`, `install2.swm`, …** (até ~3,9 GiB cada).
  Se couber, vai inteiro. `install.esd` > 4 GB (sólido) = erro claro (não dá para dividir).
- `autounattend.xml` do painel na raiz (substitui um `autounattend.xml`/`unattend.xml` da ISO).
- Pasta `INSTALAR/` com `uab-stick.json` (`"os": "windows"`), `LEIA-ME.txt` e os isoBuilder
  (`/t/{token}/tool/{windows,macos,linux}`, como no Ubuntu); aberto dali, o isoBuilder mostra só
  "Reiniciar agora e formatar este computador".
- Ordem de gravação: FAT/pastas → arquivos → limpa o último 1 MiB (restos de GPT antiga) →
  **MBR por último** (o sistema só vê a partição no fim). Tudo em blocos alinhados a 4 KiB
  (escrita bruta no Windows/macOS). Depois **relê** FAT, pastas e cada arquivo e compara SHA256.

### Boot BIOS (`helper/bootcode/`)

Código escrito para o projeto (não é da Microsoft nem de projetos GPL), montado com o `nasm`
num container descartável (`helper/bootcode/build.sh`); os `.bin` são versionados ao lado dos
`.asm` e embutidos no gravador com `go:embed` (`helper/bootcode.go`).

- `mbr.asm` → `mbr.bin` (440 bytes, só o código: a assinatura do disco (440..443, aleatória,
  a mesma na gravação e na conferência), a tabela de partições e o `55AA` são do gravador).
  Copia-se para `0000:0600`, acha a partição ativa, lê o 1º setor dela com a **INT 13h
  AH=42h (LBA, extensões EDD)** para `0000:7C00`, confere `55AA` e salta com `DL` = disco e
  `DS:SI` = entrada da partição.
- `fat32.asm` → `fat32.bin` (3 setores). Setor 0 = setor de boot FAT32 (`EB 58 90`; os bytes
  3..89, o BPB, são preenchidos pelo gravador): confere as extensões LBA e lê o **estágio 2**
  dos setores **2 e 3** da partição (a área reservada tem ≥ 32 setores; o 1 é o FSInfo e o 6/7
  as cópias), marcado com `UAB2`. O estágio 2 calcula FAT e dados pelo BPB, percorre a raiz
  seguindo a cadeia na FAT, acha `BOOTMGR` (8.3) e carrega o arquivo **seguindo a FAT**
  (contíguo ou fragmentado; clusters de 512 B a 64 KB, leituras de até 64 setores; LBA de
  32 bits) em `2000:0000`, limitado ao topo da memória convencional (INT 12h). Salta para
  `2000:0000` com `DL` = disco, `DS = ES = SS = 0`, `SP = 7C00` e o setor de boot (com o BPB e
  o nº do disco em `0x40`) em `0000:7C00`.
- Erros (sem LBA, leitura, `BOOTMGR` ausente, FAT inválida) mostram o motivo em português,
  esperam uma tecla e devolvem o controle à BIOS (INT 18h).
- O gravador aloca os arquivos contíguos (o `BOOTMGR` sai contíguo), mas o carregador não
  depende disso.

### Divisão do WIM (`helper/wim.go`)

Implementada a partir da especificação pública do formato WIM, sem recomprimir: os recursos
(já comprimidos, LZX) são copiados byte a byte. Em cada parte: cabeçalho igual ao original com
`usPartNumber`/`usTotalParts`, flag `SPANNED` (0x8), mesmo GUID, mesmo `ImageCount`,
integridade zerada; tabela de recursos só com os recursos da parte (nº da parte e nova
posição); XML inteiro com `TOTALBYTES` = posição do XML (como o próprio WIM original). A parte
1 leva os recursos de metadados (na ordem original = ordem das imagens) e o de boot. Recursos
em ordem de posição no WIM original (leitura sequencial da ISO). Tudo em fluxo: nada de
arquivo temporário; as partes são "montadas" direto no FAT32 do pendrive.

## Ventoy

Pendrive com Ventoy ("Copiar para o Ventoy"): copia a ISO como `<host>-<nome da ISO>.iso`
(fica claro no menu do Ventoy e cada computador tem a sua configuração), grava
`/ventoy/script/<config_name>` e acrescenta (ou troca) em `/ventoy/ventoy.json`:

```json
{"auto_install": [{"image": "/pasta/<host>-Win11_….iso",
                   "template": ["/ventoy/script/<host>-autounattend.xml"], "autosel": 1}]}
```

`autosel: 1` sem `timeout` = o Ventoy usa o modelo direto, sem outro menu (escolher a ISO no
menu do Ventoy já é o ato de instalar). O resto do `ventoy.json` é preservado (cópia em
`ventoy.json.bak`); `ventoy.json` inválido = erro, nada é alterado. Partição do Ventoy em FAT32
não aceita a ISO (> 4 GB): erro pedindo exFAT. Um `autounattend.xml` com `$$NOME$$` é recusado
no Ventoy (ele trataria como variável e pediria um valor na tela).

## Interface

Windows: "Windows 11 Pro para “host”" → baixando a configuração → pedindo o link à Microsoft →
barra de progresso do download (velocidade, minutos restantes, retomada) → conferindo → escolher
pendrive (≥ 16 GB) → gravar/verificar → pronto (instruções de boot: funciona em UEFI e em BIOS antiga (Legacy/CSM), aviso curto
de que sem UEFI o Windows 11 não é suportado oficialmente; "FORMATAR ESTA MÁQUINA"). Botão "Já tenho a ISO do Windows 11" sempre disponível durante o download (interrompe
o download); em erro da Microsoft, a lista de ISOs aparece sozinha.

## Modo trabalhador (macOS e uso avançado)

`isoBuilder --write --iso X --disk ID --unattend autounattend.xml [--tools pasta] [--status s.json]`
grava o pendrive do Windows (sem `--unattend`: ISO do Ubuntu bit a bit, como antes).

## Testes

- `go test` (via `helper/build.sh` / docker `golang:1-bookworm`):
  `TestWindowsBundle`, `TestFetchConfig` (SHA256/expirado), `TestWIMSplit` (WIM sintético:
  limites, nomes, SHA1 de cada recurso nas partes, `TOTALBYTES`, corrupção detectada),
  `TestWIMSplitRejectsSolid`, `TestBigFAT32` (leitor FAT32 próprio no teste: conteúdo, LFN,
  8.3 do boot UEFI, FATs iguais, alinhamento de toda escrita a 4 KiB, limpeza do fim, corrupção
  detectada), `TestBootCodeStructure` (tamanhos, `EB 58 90`, `55AA`, BPB zerado no `.bin`,
  marca `UAB2`, CHS), `TestBootCodeOnStick` (pendrive gravado: código da MBR, assinatura,
  tabela, BPB, estágio 2 nos setores 2–3, cópia no setor 6, FSInfo intacto, `BOOTMGR` 8.3,
  contíguo e fora do 1º cluster da raiz), `TestParseMSPage`, `TestMSGetLink` (servidor falso, incluindo `SentinelReject`),
  `TestVentoyAutoInstall`.
- Opcionais: `UAB_WIN_ISO=/caminho.iso go test -run TestRealWindowsISO` (UDF + edições +
  divisão do install.wim real + conferência byte a byte e SHA1 dos recursos sem compressão;
  `UAB_SWM_OUT=pasta` grava as partes); `UAB_MS_LIVE=1 go test -run TestMSLive` (pede o link
  à Microsoft e baixa só 4 MB por Range).
- `UAB_MS_FULL=/pasta go test -run TestMSFullDownload`: download **completo** real pelo
  mesmo código (`downloadWindowsISO`), com conferência do SHA256 publicado pela Microsoft.
- Boot BIOS real: `helper/bootcode/qemu-test.sh` grava pendrives (arquivos esparsos) de 6, 12,
  24, 40 e 200 GB (clusters de 4, 8, 16 e 32 KB) com um `BOOTMGR` falso
  (`bootcode/testbootmgr.asm`: confere CS/SS/SP/DL, o BPB em `7C00` e a soma de todo o
  arquivo), contíguo ou **fragmentado no fim da partição** (LBA até ~4×10⁸), e inicia cada um
  como disco USB no QEMU/KVM com SeaBIOS (`pc` e `q35`); resultado pela serial.
- Teste sem a Microsoft: `UAB_MS_BASE=http://127.0.0.1:<porta>` troca o site por um servidor
  local (e não fala com ov-df/vlscppe).
