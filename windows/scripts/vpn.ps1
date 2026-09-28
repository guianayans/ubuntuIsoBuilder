# ubuntu-autoinstall-builder (Windows) — VPN: WireGuard até o hub do painel e Tailscale.
# Carregado com ". C:\ProgramData\uab\vpn.ps1" (depois do common.ps1) por provision.ps1
# (instalação) e report.ps1 (campos "wg"/"tailscale" do relatório e aplicação do túnel
# que o painel devolve na resposta). Roda como SYSTEM.
#
# WireGuard: a chave privada é gerada AQUI (wg.exe genkey) e nunca sai do PC; o painel
# recebe só a pública, pelo relatório, e devolve a configuração do túnel, que é conferida
# (mesma validação do payload/uab-wg-apply.py do Linux) e instalada como SERVIÇO do
# Windows (wireguard.exe /installtunnelservice): sobe no boot, antes de qualquer login.

$UabWgName = 'uabwg'
$UabWgDir = Join-Path $UabRoot 'wg'
$UabWgConf = Join-Path $UabWgDir "$UabWgName.conf"
$UabWgStatusFile = Join-Path $UabWgDir 'status.json'
$UabWgService = 'WireGuardTunnel$uabwg'
$UabWgExe = Join-Path $env:ProgramFiles 'WireGuard\wireguard.exe'
$UabWgCli = Join-Path $env:ProgramFiles 'WireGuard\wg.exe'
$UabWgKeyRe = '^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$'
$UabWgHandshakeOk = 180  # s (keepalive a cada 25 s)
$UabTsExe = Join-Path $env:ProgramFiles 'Tailscale\tailscale.exe'
$UabTsKeyFile = Join-Path $UabRoot 'tailscale-authkey'

function Get-UabArch {
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { return 'arm64' }
    if ([Environment]::Is64BitOperatingSystem) { return 'amd64' }
    return 'x86'
}

function Install-UabSignedMsi {
    <# Baixa um MSI, confere a assinatura Authenticode (válida E do assinante esperado)
       e, se informado, o SHA256; só então instala em silêncio. #>
    param([string]$Url, [string]$Name, [string]$SignerPattern, [string]$Sha256 = '', [string[]]$Properties = @())
    $f = Join-Path $env:TEMP "uab-$Name-$([guid]::NewGuid().ToString('N').Substring(0, 8)).msi"
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $f -TimeoutSec 900
        if ($Sha256) {
            $h = (Get-FileHash -Algorithm SHA256 -LiteralPath $f).Hash.ToLower()
            if ($h -ne $Sha256.ToLower()) { throw "SHA256 do instalador $Name não confere" }
        }
        $sig = Get-AuthenticodeSignature -LiteralPath $f
        $subj = if ($sig.SignerCertificate) { [string]$sig.SignerCertificate.Subject } else { '' }
        if ($sig.Status -ne 'Valid' -or $subj -notmatch $SignerPattern) {
            throw "assinatura do instalador $Name recusada (estado $($sig.Status), assinante '$subj')"
        }
        Write-UabLog "${Name}: assinatura ok ($(($subj -split ',')[0]))" 'provision'
        $log = Join-Path $UabLogs "msi-$Name.log"
        $args_ = @('/i', "`"$f`"", '/qn', '/norestart', '/l*', "`"$log`"") + $Properties
        $p = Start-Process -FilePath (Join-Path $env:SystemRoot 'System32\msiexec.exe') -ArgumentList $args_ -Wait -PassThru -WindowStyle Hidden
        if (@(0, 1641, 3010) -notcontains $p.ExitCode) { throw "msiexec do $Name saiu com código $($p.ExitCode) (veja $log)" }
    } finally {
        Remove-Item -Force -LiteralPath $f -ErrorAction SilentlyContinue
    }
}

function Get-UabWebText([string]$Url) {
    <# Texto de uma URL. Invoke-WebRequest devolve bytes (não texto) quando o servidor não
       manda um Content-Type de texto (caso do latest.sig do WireGuard): decodifica UTF-8. #>
    $c = (Invoke-WebRequest -UseBasicParsing -Uri $Url -TimeoutSec 60).Content
    if ($c -is [byte[]]) { return [Text.Encoding]::UTF8.GetString($c) }
    return [string]$c
}

function Wait-UabService([string]$Name, [int]$Seconds = 60) {
    for ($i = 0; $i -lt $Seconds; $i++) {
        $s = Get-Service -Name $Name -ErrorAction SilentlyContinue
        if ($s -and $s.Status -eq 'Running') { return $true }
        Start-Sleep 1
    }
    return $false
}

# ================================================================ WireGuard
function Install-UabWireGuard {
    <# WireGuard oficial para Windows (download.wireguard.com). O latest.sig lista os MSIs
       atuais (com hash BLAKE2b, que o PowerShell 5.1 não calcula): usamos só o nome do
       arquivo e conferimos a assinatura Authenticode "WireGuard LLC" antes de instalar. #>
    if (Test-Path -LiteralPath $UabWgCli) { return }
    $base = 'https://download.wireguard.com/windows-client/'
    $arch = Get-UabArch
    $list = Get-UabWebText ($base + 'latest.sig')
    $msi = ''
    foreach ($ln in ($list -split "`r?`n")) {
        if ($ln -match "^[0-9a-f]{64}\s+(wireguard-$arch-[0-9.]+\.msi)\s*$") { $msi = $Matches[1] }
    }
    if (-not $msi) { throw "não achei o instalador do WireGuard ($arch) em ${base}latest.sig" }
    Write-UabLog "WireGuard: instalando $msi" 'provision'
    Install-UabSignedMsi -Url ($base + $msi) -Name 'wireguard' -SignerPattern '(^|,\s*)(CN|O)=WireGuard LLC(,|$)' -Properties @('DO_NOT_LAUNCH=1')
    if (-not (Test-Path -LiteralPath $UabWgCli)) { throw 'WireGuard instalado, mas wg.exe não apareceu' }
}

function Protect-UabWgDir {
    New-Item -ItemType Directory -Force -Path $UabWgDir | Out-Null
    # só SYSTEM e Administradores (a pasta tem a chave privada)
    & icacls.exe $UabWgDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' /Q | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "icacls falhou em $UabWgDir" }
}

function Initialize-UabWgKeys {
    <# Gera (uma vez) o par de chaves do túnel e devolve a pública. #>
    Protect-UabWgDir
    $pk = Join-Path $UabWgDir 'private.key'
    $priv = if (Test-Path -LiteralPath $pk) { ([IO.File]::ReadAllText($pk)).Trim() } else { '' }
    if ($priv -notmatch $UabWgKeyRe) {
        $r = Invoke-UabExe $UabWgCli @('genkey') 30
        # a saída junta stdout e stderr (o wg.exe pode avisar algo): só a linha da chave
        $priv = [string](([string]$r.out -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -match $UabWgKeyRe } | Select-Object -First 1))
        if ($r.code -ne 0 -or $priv -notmatch $UabWgKeyRe) { throw "wg genkey falhou (código $($r.code))" }
        Write-UabFileAtomic $pk ($priv + "`n")
        & icacls.exe $pk /reset /Q | Out-Null  # herda da pasta (só SYSTEM/Administradores)
    }
    $pub = (($priv | & $UabWgCli pubkey) | Out-String).Trim()
    if ($pub -notmatch $UabWgKeyRe) { throw 'wg pubkey falhou' }
    Write-UabFileAtomic (Join-Path $UabWgDir 'public.key') ($pub + "`n")
    return $pub
}

function Get-UabWgState {
    $st = @{}
    try {
        $j = Get-Content -LiteralPath $UabWgStatusFile -Raw -Encoding UTF8 -ErrorAction Stop | ConvertFrom-Json
        foreach ($p in $j.PSObject.Properties) { $st[$p.Name] = $p.Value }
    } catch { }
    return $st
}

function Save-UabWgState([hashtable]$Changes) {
    $st = Get-UabWgState
    foreach ($k in $Changes.Keys) { $st[$k] = $Changes[$k] }
    $st.updated = Get-UabUnixNow
    New-Item -ItemType Directory -Force -Path $UabWgDir | Out-Null
    Write-UabFileAtomic $UabWgStatusFile (ConvertTo-Json -InputObject $st -Compress)
}

function Get-UabWgReport {
    <# Campo "wg" do relatório (mesmo formato do Linux): pubkey + estado do túnel. #>
    $r = [ordered]@{}
    $pf = Join-Path $UabWgDir 'public.key'
    if (Test-Path -LiteralPath $pf) {
        $pub = ([IO.File]::ReadAllText($pf)).Trim()
        if ($pub -match $UabWgKeyRe) { $r.pubkey = $pub }
    }
    $st = Get-UabWgState
    foreach ($k in 'state', 'address', 'handshake', 'lan', 'message') { if ($st.ContainsKey($k)) { $r[$k] = $st[$k] } }
    return $r
}

# ---- redes IPv4 (validação estrita, como o ipaddress do Python)
function ConvertTo-UabNet([string]$Cidr, [switch]$AllowHost) {
    <# "a.b.c.d/n" -> @{ ip; len; net; mask } (inteiros). Sem -AllowHost, bits de host
       ligados = inválido (como ip_network(strict=True)); com -AllowHost é um endereço
       com prefixo (como ip_interface). #>
    if ($Cidr -notmatch '^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})/(\d{1,2})$') { throw "rede inválida: $Cidr" }
    $o = @([int]$Matches[1], [int]$Matches[2], [int]$Matches[3], [int]$Matches[4])
    $len = [int]$Matches[5]
    if ($len -gt 32 -or @($o | Where-Object { $_ -gt 255 }).Count) { throw "rede inválida: $Cidr" }
    [uint64]$ip = ($o[0] * 16777216) + ($o[1] * 65536) + ($o[2] * 256) + $o[3]
    [uint64]$mask = [uint64]4294967295 - ([uint64][math]::Pow(2, 32 - $len) - 1)
    $net = $ip -band $mask
    if (-not $AllowHost -and $net -ne $ip) { throw "rede inválida (bits de host): $Cidr" }
    return @{ ip = $ip; len = $len; net = $net; mask = $mask; text = $Cidr }
}

function Test-UabSubnetOf($a, $b) { return ($a.len -ge $b.len) -and (($a.net -band $b.mask) -eq $b.net) }
function Test-UabOverlap($a, $b) {
    $m = if ($a.len -lt $b.len) { $a.mask } else { $b.mask }
    return (($a.net -band $m) -eq ($b.net -band $m))
}

function Get-UabWgHandshake {
    $r = Invoke-UabExe $UabWgCli @('show', $UabWgName, 'latest-handshakes') 20
    foreach ($ln in ([string]$r.out -split "`r?`n")) {
        $f = $ln.Trim() -split '\s+'
        if ($f.Count -ge 2 -and $f[1] -match '^\d+$') { return [int64]$f[1] }
    }
    return [int64]0
}

function Get-UabUnixNow { return [int64]([DateTimeOffset]::UtcNow.ToUnixTimeSeconds()) }

function Invoke-UabWgApply($cfg) {
    <# Aplica o túnel que o painel devolveu na resposta do relatório ($resp.wg). Valida
       tudo (o PC não confia cegamente no painel) e só reinstala o serviço se a config mudou. #>
    if ($null -eq $cfg) { return }
    $names = @($cfg.PSObject.Properties | ForEach-Object Name)
    if ($names -notcontains 'address') {
        $msg = if ($cfg.pending) { [string]$cfg.pending } elseif ($cfg.error) { [string]$cfg.error } else { 'aguardando o painel' }
        Save-UabWgState @{ state = 'aguardando'; message = $msg }
        return
    }
    if (-not (Test-Path -LiteralPath $UabWgExe)) { Save-UabWgState @{ state = 'erro'; message = 'WireGuard não instalado neste PC' }; return }

    # ---- validação estrita (mesmas regras do uab-wg-apply.py)
    $fail = { param($m) Save-UabWgState @{ state = 'erro'; message = $m }; Write-UabLog "wg: $m" 'report' }
    try {
        $net = ConvertTo-UabNet ([string]$(if ($cfg.net) { $cfg.net } else { '10.77.0.0/16' }))
        $lanmap = ConvertTo-UabNet ([string]$(if ($cfg.lanmap_net) { $cfg.lanmap_net } else { '10.200.0.0/16' }))
        $addr = ConvertTo-UabNet ([string]$cfg.address) -AllowHost
        $allowed = @(@($cfg.allowed_ips) | ForEach-Object { ConvertTo-UabNet ([string]$_) })
    } catch { & $fail 'configuração WireGuard inválida recebida do painel'; return }
    if ($addr.len -ne 32 -or -not (Test-UabSubnetOf $addr $net)) { & $fail 'endereço WireGuard fora da rede da VPN'; return }
    if (-not $allowed.Count -or @($allowed | Where-Object { -not (Test-UabSubnetOf $_ $net) }).Count) { & $fail 'rotas WireGuard fora da rede da VPN'; return }
    $hub = [string]$cfg.hub_pubkey; $ep = [string]$cfg.endpoint
    if ($hub -notmatch $UabWgKeyRe -or $ep -notmatch '^[A-Za-z0-9.-]{1,253}:\d{2,5}$') { & $fail 'chave/endereço do hub inválidos'; return }
    $ka = 25
    if ($null -ne $cfg.keepalive -and [string]$cfg.keepalive -match '^\d{1,4}$') { $ka = [int]$cfg.keepalive }
    try {
        foreach ($ip in (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction Stop)) {
            if ($ip.InterfaceAlias -eq $UabWgName -or $ip.IPAddress -like '127.*') { continue }
            $n = ConvertTo-UabNet "$($ip.IPAddress)/$($ip.PrefixLength)" -AllowHost
            if ((Test-UabOverlap $n $net) -or (Test-UabOverlap $n $lanmap)) {
                & $fail "a rede local $($ip.IPAddress)/$($ip.PrefixLength) ($($ip.InterfaceAlias)) conflita com a VPN $($net.text)/$($lanmap.text); túnel não aplicado"
                return
            }
        }
    } catch { }
    # "Acesso à rede local do cliente" (NETMAP) ainda não existe no Windows: o túnel sobe sem ele.
    $note = if ($cfg.lan) { 'acesso à rede local do cliente não é suportado no Windows (ignorado); o túnel funciona normalmente' } else { '' }

    $pk = Join-Path $UabWgDir 'private.key'
    if (-not (Test-Path -LiteralPath $pk)) { & $fail 'chave privada WireGuard ausente'; return }
    $priv = ([IO.File]::ReadAllText($pk)).Trim()
    $lines = @('# gerado pelo ISO Builder (vpn.ps1). Não edite.', '[Interface]', "PrivateKey = $priv", "Address = $($addr.text)", '',
        '[Peer]', "PublicKey = $hub", "Endpoint = $ep", "AllowedIPs = $(($allowed | ForEach-Object { $_.text }) -join ', ')",
        "PersistentKeepalive = $ka", '')
    $new = $lines -join "`r`n"
    $old = if (Test-Path -LiteralPath $UabWgConf) { [IO.File]::ReadAllText($UabWgConf) } else { '' }
    $svc = Get-Service -Name $UabWgService -ErrorAction SilentlyContinue
    $now = Get-UabUnixNow
    $st = Get-UabWgState
    $applied = if ($st.applied) { [int64]$st.applied } else { [int64]0 }
    try {
        if ($new -ne $old -or -not $svc) {
            Protect-UabWgDir
            Write-UabFileAtomic $UabWgConf $new
            & icacls.exe $UabWgConf /reset /Q | Out-Null
            if ($svc) {
                Start-Process -FilePath $UabWgExe -ArgumentList @('/uninstalltunnelservice', $UabWgName) -Wait -WindowStyle Hidden
                for ($i = 0; $i -lt 30 -and (Get-Service -Name $UabWgService -ErrorAction SilentlyContinue); $i++) { Start-Sleep 1 }
            }
            Start-Process -FilePath $UabWgExe -ArgumentList @('/installtunnelservice', "`"$UabWgConf`"") -Wait -WindowStyle Hidden
            $ok = Wait-UabService $UabWgService 30
            $applied = $now
            Write-UabLog "wg: túnel (re)aplicado ($($addr.text) -> $ep): $(if ($ok) { 'OK' } else { 'FALHOU (serviço não ficou ativo)' })" 'report'
            if (-not $ok) { & $fail 'o serviço do túnel WireGuard não ficou ativo'; return }
        } elseif ($svc.Status -ne 'Running') {
            Set-Service -Name $UabWgService -StartupType Automatic -ErrorAction SilentlyContinue
            Start-Service -Name $UabWgService -ErrorAction Stop
            $applied = $now
            Write-UabLog 'wg: serviço do túnel estava parado: iniciado' 'report'
        }
    } catch { & $fail "falha ao instalar o túnel: $($_.Exception.Message)"; return }

    # Sem handshake há mais de 3 min: reinicia o túnel (reresolve o DNS do hub, cujo IP pode ter mudado)
    $hs = Get-UabWgHandshake
    if ((-not $hs -or ($now - $hs) -gt $UabWgHandshakeOk) -and ($now - $applied) -gt $UabWgHandshakeOk) {
        try {
            Restart-Service -Name $UabWgService -Force -ErrorAction Stop
            $applied = $now
            Write-UabLog "wg: sem handshake há mais de 3 min: túnel reiniciado" 'report'
        } catch { Write-UabLog "wg: não reiniciei o túnel: $($_.Exception.Message)" 'report' }
    }
    $active = $hs -and ($now - $hs) -lt $UabWgHandshakeOk
    Save-UabWgState @{ state = $(if ($active) { 'ativo' } else { 'conectando' }); address = $addr.text; handshake = $hs
        lan = ''; message = $note; applied = $applied }
}

# ================================================================ Tailscale
function Install-UabTailscale {
    <# MSI oficial estável (pkgs.tailscale.com): SHA256 publicado ao lado e assinatura
       Authenticode "Tailscale Inc." conferidos antes de instalar. #>
    if ((Test-Path -LiteralPath $UabTsExe) -and (Get-Service -Name 'Tailscale' -ErrorAction SilentlyContinue)) { return }
    $base = 'https://pkgs.tailscale.com/stable/'
    $arch = Get-UabArch
    $j = Invoke-RestMethod -UseBasicParsing -Uri ($base + '?mode=json') -TimeoutSec 60
    $msi = [string]$j.MSIs.$arch
    if ($msi -notmatch '^tailscale-setup-[0-9.]+-(amd64|arm64|x86)\.msi$') { throw "não achei o instalador do Tailscale ($arch)" }
    $sha = (Get-UabWebText ($base + $msi + '.sha256')).Trim()
    if ($sha -notmatch '^[0-9a-f]{64}$') { throw 'SHA256 do instalador do Tailscale indisponível' }
    Write-UabLog "Tailscale: instalando $msi" 'provision'
    Install-UabSignedMsi -Url ($base + $msi) -Name 'tailscale' -Sha256 $sha -SignerPattern '(^|,\s*)(CN|O)=Tailscale Inc\.(,|$)' `
        -Properties @('TS_UNATTENDEDMODE=always', 'TS_NOLAUNCH=true')
    if (-not (Test-Path -LiteralPath $UabTsExe)) { throw 'Tailscale instalado, mas tailscale.exe não apareceu' }
    Set-Service -Name 'Tailscale' -StartupType Automatic -ErrorAction SilentlyContinue
    Start-Service -Name 'Tailscale' -ErrorAction SilentlyContinue
    if (-not (Wait-UabService 'Tailscale' 60)) { throw 'o serviço do Tailscale não ficou ativo' }
}

function Get-UabTsStatus {
    if (-not (Test-Path -LiteralPath $UabTsExe)) { return $null }
    $r = Invoke-UabExe $UabTsExe @('status', '--json') 30
    try { return ([string]$r.out | ConvertFrom-Json) } catch { return $null }
}

function Get-UabTsReport {
    <# Campo "tailscale" do relatório (mesmo formato do Linux). #>
    $d = Get-UabTsStatus
    if (-not $d) { return @{} }
    $me = $d.Self
    # sem login, TailscaleIPs vem null: @($null) viraria [null] no JSON
    $ips = @(if ($me -and $me.TailscaleIPs) { @($me.TailscaleIPs | Where-Object { $_ } | ForEach-Object { [string]$_ }) })
    return [ordered]@{ state = [string]$d.BackendState; ips = $ips
        dns = $(if ($me) { ([string]$me.DNSName).TrimEnd('.') } else { '' }); hostname = $(if ($me) { [string]$me.HostName } else { '' }) }
}

function Invoke-UabTailscaleLogin([string]$Hostname) {
    <# tailscale up com a auth key (lida do arquivo: nunca na linha de comando nem no log).
       Chave inválida/expirada: falha clara; após 3 tentativas desiste (apaga a chave) sem
       travar a pós-instalação. #>
    $d = Get-UabTsStatus
    if ($d -and $d.BackendState -eq 'Running') {
        Remove-Item -Force -LiteralPath $UabTsKeyFile -ErrorAction SilentlyContinue
        return "ok ($(@($d.Self.TailscaleIPs)[0]))"
    }
    if (-not (Test-Path -LiteralPath $UabTsKeyFile)) {
        return "desistiu: Tailscale instalado, mas sem auth key. No PC (como administrador): tailscale up --unattended --hostname=$Hostname"
    }
    $triesFile = Join-Path $UabRoot 'tailscale-tries'
    $tries = 0
    if (Test-Path -LiteralPath $triesFile) { [void][int]::TryParse(([IO.File]::ReadAllText($triesFile)).Trim(), [ref]$tries) }
    $r = Invoke-UabExe $UabTsExe @('up', "--auth-key=file:$UabTsKeyFile", "--hostname=$Hostname", '--unattended', '--timeout=150s') 180
    $d = Get-UabTsStatus
    if ($r.code -eq 0 -and $d -and $d.BackendState -eq 'Running') {
        Remove-Item -Force -LiteralPath $UabTsKeyFile, $triesFile -ErrorAction SilentlyContinue
        Write-UabLog 'Tailscale: auth key apagada do disco' 'provision'
        return "ok ($(@($d.Self.TailscaleIPs)[0]))"
    }
    $why = (([string]$r.out -split "`r?`n" | Where-Object { $_ -match '\w' } | Select-Object -Last 2) -join ' ') -replace 'tskey-[A-Za-z0-9_-]+', 'tskey-***'
    if (-not $why) { $why = "código $($r.code)" }
    $why = "tailscale up falhou (auth key inválida, expirada ou já usada?): $why"
    $tries++
    if ($tries -ge 3) {
        Remove-Item -Force -LiteralPath $UabTsKeyFile, $triesFile -ErrorAction SilentlyContinue
        return "desistiu após 3 tentativas: $why"
    }
    Set-Content -LiteralPath $triesFile -Value $tries -Encoding ASCII
    throw $why
}
