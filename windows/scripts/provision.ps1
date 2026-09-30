# ubuntu-autoinstall-builder (Windows) — pós-instalação (tarefa "UAB Provision", como SYSTEM).
# Roda no boot e a cada 5 min até concluir (sobrevive a reinícios; etapa concluída não
# se repete). Etapas: openssh, wireguard, tailscale_install, tailscale_login, remote_agent,
# apps, report. Estado em status.json: etapa "ok", "falhou <data>: <motivo>" ou
# "desistiu...: <motivo>" (não tenta mais; não trava as outras);
# provisioned "ok <data>" ou "pendente: ...".
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
. (Join-Path $env:ProgramData 'uab\common.ps1')
. (Join-Path $env:ProgramData 'uab\vpn.ps1')
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.ServicePointManager]::SecurityProtocol

$mutex = New-Object Threading.Mutex($false, 'Global\uab-provision')
if (-not $mutex.WaitOne(0)) { exit 0 }  # outra execução em andamento

function Log([string]$m) { Write-UabLog $m 'provision' }

$conf = Get-UabConf
$st = Get-UabStatus
if ($st.provisioned -like 'ok*') {
    Disable-ScheduledTask -TaskName 'UAB Provision' -ErrorAction SilentlyContinue | Out-Null
    exit 0
}

# ---------------------------------------------------------------- espera o fim do OOBE
# A tarefa pode disparar no boot em que o OOBE ainda roda: nada de instalar nem de
# apagar o unattend.xml (o OOBE ainda lê dele) antes de terminar.
function Test-SetupDone {
    $s = Get-ItemProperty 'HKLM:\SYSTEM\Setup' -ErrorAction SilentlyContinue
    if (-not $s) { return $false }
    if ($s.SystemSetupInProgress -ne 0 -or $s.OOBEInProgress -eq 1) { return $false }
    return [bool](Get-LocalUser -Name $conf.admin_user -ErrorAction SilentlyContinue)
}
$t0 = Get-Date
while (-not (Test-SetupDone)) {
    if (((Get-Date) - $t0).TotalMinutes -gt 20) { Log 'OOBE ainda não terminou; tento na próxima execução'; exit 0 }
    Start-Sleep -Seconds 15
}

# O arquivo de respostas tem a senha (ofuscada) e o token: não fica no disco.
foreach ($f in @("$env:SystemRoot\Panther\unattend.xml", "$env:SystemRoot\Panther\unattend-original.xml",
        "$env:SystemRoot\System32\Sysprep\unattend.xml")) {
    if (Test-Path -LiteralPath $f) {
        try { Remove-Item -Force -LiteralPath $f; Log "removido $f (tinha a senha)" } catch { Log "aviso: não removi ${f}: $_" }
    }
}

Log "=== início (build $($conf.build_id)) ==="
$failed = @()

function Invoke-Step([string]$Name, [scriptblock]$Body) {
    $s = Get-UabStatus
    if ([string]$s.steps[$Name] -like 'ok*' -or [string]$s.steps[$Name] -like 'desistiu*') { return }
    Set-UabStatus 'current' $Name
    Log ">>> $Name"
    try {
        $r = & $Body
        $r = @($r | Where-Object { $_ -is [string] })[-1]
        $v = if ($r -like 'ok*' -or $r -like 'desistiu*') { $r } else { 'ok' }
        Set-UabStep $Name $v
        Log "<<< ${Name}: $v"
    } catch {
        $msg = ($_.Exception.Message -replace '\s+', ' ').Trim()
        if ($msg.Length -gt 160) { $msg = $msg.Substring(0, 160) }
        Set-UabStep $Name ("falhou {0}: {1}" -f (Get-UabNow), $msg)
        Log "<<< ${Name}: FALHOU: $msg"
        $script:failed += $Name
    }
    Set-UabStatus 'current' ''
    & $UabPS -NoProfile -ExecutionPolicy Bypass -File (Join-Path $UabRoot 'report.ps1') 2>$null | Out-Null
}

function Test-Internet {
    foreach ($u in @('http://www.msftconnecttest.com/connecttest.txt', 'https://cdn.winget.microsoft.com/cache/source.msix')) {
        try { Invoke-WebRequest -UseBasicParsing -Method Head -Uri $u -TimeoutSec 15 | Out-Null; return $true } catch { }
    }
    return $false
}

# ---------------------------------------------------------------- winget
function Get-Winget {
    $w = Get-UabWinget
    if ($w) {
        $r = Invoke-UabWinget $w @('--version') 2
        if ($r.code -eq 0) { return $w }
        Log "winget em $w não respondeu (código $($r.code)): $($r.out)"
    }
    # Sem App Installer utilizável: instala o pacote oficial (github.com/microsoft/winget-cli)
    # para todos os usuários, com as dependências.
    Log 'instalando o App Installer (winget) a partir do GitHub da Microsoft'
    $tmp = Join-Path $env:TEMP 'uab-winget'
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    $rel = Invoke-RestMethod -UseBasicParsing -Uri 'https://api.github.com/repos/microsoft/winget-cli/releases/latest' -TimeoutSec 60
    $bundle = $rel.assets | Where-Object name -like '*.msixbundle' | Select-Object -First 1
    $lic = $rel.assets | Where-Object name -like '*License1.xml' | Select-Object -First 1
    $deps = $rel.assets | Where-Object name -eq 'DesktopAppInstaller_Dependencies.zip' | Select-Object -First 1
    if (-not $bundle -or -not $deps) { throw 'release do winget sem os arquivos esperados' }
    Invoke-WebRequest -UseBasicParsing -Uri $bundle.browser_download_url -OutFile "$tmp\winget.msixbundle" -TimeoutSec 900
    Invoke-WebRequest -UseBasicParsing -Uri $deps.browser_download_url -OutFile "$tmp\deps.zip" -TimeoutSec 900
    Expand-Archive -Force -Path "$tmp\deps.zip" -DestinationPath "$tmp\deps"
    $depFiles = @(Get-ChildItem -Recurse -Path "$tmp\deps" -Filter '*.appx' | Where-Object FullName -match '\\x64\\' | ForEach-Object FullName)
    $a = @{ Online = $true; PackagePath = "$tmp\winget.msixbundle"; DependencyPackagePath = $depFiles }
    if ($lic) {
        Invoke-WebRequest -UseBasicParsing -Uri $lic.browser_download_url -OutFile "$tmp\license.xml" -TimeoutSec 120
        $a.LicensePath = "$tmp\license.xml"
    } else { $a.SkipLicense = $true }
    Add-AppxProvisionedPackage @a | Out-Null
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    $w = Get-UabWinget
    if (-not $w) { throw 'winget não encontrado após instalar o App Installer' }
    return $w
}

$script:wingetReady = $false
function Initialize-Winget([string]$w) {
    if ($script:wingetReady) { return }
    $r = Invoke-UabWinget $w @('source', 'update', '--name', 'winget', '--disable-interactivity') 10
    Log "winget source update: código $($r.code)"
    $script:wingetReady = $true
}

# instalado? (winget list devolve 0 quando acha o ID)
function Test-WingetInstalled([string]$w, [string]$id) {
    $r = Invoke-UabWinget $w @('list', '--id', $id, '--exact', '--source', 'winget', '--accept-source-agreements', '--disable-interactivity') 5
    return ($r.code -eq 0 -and $r.out -match [regex]::Escape($id))
}

# 0 = ok; 0x8A15002B = nenhuma atualização aplicável; 0x8A150061 = já instalado
$WingetOk = @(0, -1978335189, -1978335135)

function Install-WingetId([string]$w, [string]$id) {
    Initialize-Winget $w
    if (Test-WingetInstalled $w $id) { return 'já instalado' }
    $base = @('install', '--id', $id, '--exact', '--source', 'winget', '--silent',
        '--accept-package-agreements', '--accept-source-agreements', '--disable-interactivity')
    # primeiro para a máquina toda; se o pacote não tiver instalador "machine", sem escopo
    $r = Invoke-UabWinget $w ($base + @('--scope', 'machine'))
    if ($WingetOk -notcontains $r.code) {
        Log "winget $id (scope machine): código $($r.code): $(($r.out -split "`n" | Select-Object -Last 3) -join ' | ')"
        $r = Invoke-UabWinget $w $base
    }
    if ($WingetOk -contains $r.code) { return 'ok' }
    $last = ($r.out -split "`n" | Where-Object { $_ -match '\w' } | Select-Object -Last 1)
    throw ("código {0}{1}" -f $r.code, $(if ($last) { ": $last" } else { '' }))
}

# ---------------------------------------------------------------- etapas
function Step-OpenSSH {
    $svc = Get-Service -Name sshd -ErrorAction SilentlyContinue
    if (-not $svc) {
        try {
            $cap = Get-WindowsCapability -Online -ErrorAction Stop | Where-Object Name -like 'OpenSSH.Server*' | Select-Object -First 1
            if ($cap -and $cap.State -ne 'Installed') {
                Log "instalando a capacidade $($cap.Name) (Windows Update)"
                Add-WindowsCapability -Online -Name $cap.Name -ErrorAction Stop | Out-Null
            }
        } catch { Log "capacidade OpenSSH.Server falhou: $($_.Exception.Message)" }
        $svc = Get-Service -Name sshd -ErrorAction SilentlyContinue
    }
    if (-not $svc) {
        Log 'tentando o OpenSSH pelo winget (Microsoft.OpenSSH.Preview)'
        $w = Get-Winget
        Install-WingetId $w 'Microsoft.OpenSSH.Preview' | Out-Null
        $svc = Get-Service -Name sshd -ErrorAction SilentlyContinue
    }
    if (-not $svc) { throw 'OpenSSH Server não instalou (sem internet ou Windows Update bloqueado)' }

    Set-Service -Name sshd -StartupType Automatic
    Start-Service -Name sshd  # na 1ª partida gera as chaves do host e o sshd_config
    $cfg = Join-Path $env:ProgramData 'ssh\sshd_config'
    for ($i = 0; $i -lt 30 -and -not (Test-Path $cfg); $i++) { Start-Sleep 1 }

    # shell padrão: PowerShell (o terminal do painel)
    New-Item -Path 'HKLM:\SOFTWARE\OpenSSH' -Force | Out-Null
    New-ItemProperty -Path 'HKLM:\SOFTWARE\OpenSSH' -Name DefaultShell -PropertyType String -Force `
        -Value (Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe') | Out-Null

    # chaves aceitas (a conta é Administrador -> C:\ProgramData\ssh\administrators_authorized_keys,
    # que o sshd só aceita se for de SYSTEM e Administradores, sem herança): a chave do painel
    # (terminal no navegador) + as chaves do usuário, se ele escolheu acesso por chave.
    $keys = @()
    $pf = Join-Path $UabRoot 'panel.pub'
    if (Test-Path $pf) { foreach ($k in (Get-Content -LiteralPath $pf -Encoding UTF8)) { if ($k.Trim()) { $keys += $k.Trim() } } }
    foreach ($k in @($conf.ssh_keys)) { if ("$k".Trim()) { $keys += "$k".Trim() } }
    if ($keys.Count) {
        $ak = Join-Path $env:ProgramData 'ssh\administrators_authorized_keys'
        $lines = @()
        if (Test-Path $ak) { $lines = @(Get-Content -LiteralPath $ak -Encoding UTF8 | Where-Object { $_.Trim() }) }
        foreach ($k in $keys) { if ($lines -notcontains $k) { $lines += $k } }
        [IO.File]::WriteAllText($ak, (($lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding $false))
        & icacls.exe $ak /inheritance:r /grant:r '*S-1-5-32-544:F' '*S-1-5-18:F' | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "icacls falhou em $ak" }
    }

    # sshd_config: por padrão só chave. No modo "usuário e senha" ($conf.ssh_password), a senha é
    # aceita SOMENTE da rede local e da VPN (de IP público, só chave) — igual ao Linux. O bloco
    # global vai no topo (1ª ocorrência vale) e o "Match Address" no fim (não engloba os padrões).
    if (Test-Path $cfg) {
        $nets = '10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,127.0.0.0/8,fd7a:115c:a1e0::/48,fc00::/7,fe80::/10,::1'
        $txt = Get-Content -LiteralPath $cfg -Raw -Encoding UTF8
        $txt = [regex]::Replace($txt, '(?s)# uab-ssh-ini.*?# uab-ssh-fim\r?\n', '')            # bloco global antigo
        $txt = [regex]::Replace($txt, '(?s)\r?\n# uab-ssh-pw-ini.*?# uab-ssh-pw-fim', '')       # bloco Match antigo
        $txt = [regex]::Replace($txt, '(?m)^# uab: somente chave.*(\r?\n(Password|Pubkey)Authentication.*)*\r?\n?', '')  # marca ainda mais antiga
        $head = "# uab-ssh-ini (ISO Builder)`r`nPubkeyAuthentication yes`r`nPasswordAuthentication no`r`n# uab-ssh-fim`r`n"
        $txt = $head + $txt.TrimStart()
        if ($conf.ssh_password) {
            $txt = $txt.TrimEnd() + "`r`n`r`n# uab-ssh-pw-ini`r`nMatch Address $nets`r`n    PasswordAuthentication yes`r`n# uab-ssh-pw-fim`r`n"
        }
        [IO.File]::WriteAllText($cfg, $txt, (New-Object Text.UTF8Encoding $false))
        # valida: se ficar inválido, remove o bloco de senha para não perder o acesso do painel
        $sshdExe = Join-Path $env:SystemRoot 'System32\OpenSSH\sshd.exe'
        if ($conf.ssh_password -and (Test-Path $sshdExe)) {
            & $sshdExe -t 2>$null
            if ($LASTEXITCODE -ne 0) {
                Log 'sshd -t recusou o bloco de senha; revertendo para só-chave'
                $txt = [regex]::Replace((Get-Content -LiteralPath $cfg -Raw -Encoding UTF8), '(?s)\r?\n# uab-ssh-pw-ini.*?# uab-ssh-pw-fim\r?\n?', '')
                [IO.File]::WriteAllText($cfg, $txt, (New-Object Text.UTF8Encoding $false))
            }
        }
    }

    # firewall: porta 22 em todos os perfis (a capacidade cria a regra; o winget pode não criar)
    $rule = Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue
    if ($rule) { Set-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -Enabled True -Profile Any }
    else {
        New-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'OpenSSH Server (sshd)' -Enabled True `
            -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 -Profile Any | Out-Null
    }
    Restart-Service -Name sshd -Force
    Start-Sleep 2
    if ((Get-Service sshd).Status -ne 'Running') { throw 'o serviço sshd não ficou ativo' }
}

function Step-RemoteAgent {
    $svc = Get-Service -Name 'Mesh Agent' -ErrorAction SilentlyContinue
    if ($svc -and $svc.Status -eq 'Running') { return }
    $mid = Read-UabSecret 'remote-meshid'
    if (-not $mid) { throw 'id do grupo do Remoto ausente' }
    $id = if ([Environment]::Is64BitOperatingSystem) { 4 } else { 3 }  # 4 = Windows x64, 3 = x86
    $url = "$($conf.remote_url.TrimEnd('/'))/meshagents?id=$id&meshid=$([uri]::EscapeDataString($mid))&installflags=0"
    $exe = Join-Path $UabRoot 'meshagent.exe'
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $exe -TimeoutSec 300
    $b = [IO.File]::ReadAllBytes($exe)
    if ($b.Length -lt 100000 -or $b[0] -ne 0x4D -or $b[1] -ne 0x5A) { throw "o Remoto não devolveu o agente (resposta de $($b.Length) bytes)" }
    $p = Start-Process -FilePath $exe -ArgumentList '-fullinstall' -Wait -PassThru -WindowStyle Hidden
    Log "meshagent -fullinstall: código $($p.ExitCode)"
    for ($i = 0; $i -lt 60; $i++) {
        $svc = Get-Service -Name 'Mesh Agent' -ErrorAction SilentlyContinue
        if ($svc -and $svc.Status -eq 'Running') { break }
        Start-Sleep 2
    }
    Remove-Item -Force $exe -ErrorAction SilentlyContinue
    if (-not $svc -or $svc.Status -ne 'Running') { throw 'o agente do Remoto não ficou ativo' }
    Remove-Item -Force (Join-Path $UabRoot 'remote-meshid') -ErrorAction SilentlyContinue
}

function Step-Apps {
    $w = Get-Winget
    $s = Get-UabStatus
    $res = @{}
    if ($s.apps) { foreach ($p in $s.apps.PSObject.Properties) { $res[$p.Name] = [string]$p.Value } }
    $bad = @()
    foreach ($id in @($conf.apps)) {
        $prev = [string]$res[$id]
        if ($prev -like 'ok*') { continue }
        $tries = 0
        if ($prev -match '^falhou \((\d+)x\)') { $tries = [int]$Matches[1] }
        if ($tries -ge 3) { $bad += $id; continue }  # desiste após 3 tentativas (não trava os outros)
        Set-UabStatus 'current' "apps: $id"
        Log "winget: $id"
        try { $r = Install-WingetId $w $id; $res[$id] = 'ok'; Log "winget: $id -> $r" }
        catch {
            $tries++
            $res[$id] = "falhou (${tries}x) $(Get-UabNow): $($_.Exception.Message)"
            Log "winget: $id -> FALHOU: $($_.Exception.Message)"
            $bad += $id
        }
        $s = Get-UabStatus; $s.apps = $res; Save-UabStatus $s
    }
    $pending = @($bad | Where-Object { $res[$_] -notmatch '^falhou \(3x\)' })
    if ($pending.Count) { throw ("não instalados ainda: " + ($pending -join ', ')) }
    if ($bad.Count) { return "ok (sem sucesso após 3 tentativas: $($bad -join ', '))" }
    return 'ok'
}

function Step-WireGuard {
    # Túnel até o hub (seu servidor). A chave privada é gerada AQUI e nunca sai do PC; o
    # painel recebe só a pública, pelo relatório, e devolve a configuração do túnel.
    Install-UabWireGuard
    $pub = Initialize-UabWgKeys
    Log "WireGuard: chave pública $($pub.Substring(0, 8))…"
    # registra no painel já agora (o relatório aplica o túnel quando o hub responder)
    & $UabPS -NoProfile -ExecutionPolicy Bypass -File (Join-Path $UabRoot 'report.ps1') 2>$null | Out-Null
    $w = Get-UabWgState
    if ((Get-Service -Name $UabWgService -ErrorAction SilentlyContinue) -and $w.state -ne 'erro') {
        Log "WireGuard: túnel configurado ($($w.address))"
        return 'ok'
    }
    $why = if ($w.message) { $w.message } else { 'aguardando o painel/hub' }
    Log "WireGuard: $why; o relatório (a cada 10 min) aplica o túnel assim que possível"
    return "ok (túnel ainda não aplicado: $why)"
}

function Step-TailscaleInstall { Install-UabTailscale }

function Step-TailscaleLogin {
    $h = if ($conf.tailscale_hostname) { [string]$conf.tailscale_hostname } else { [string]$conf.hostname }
    return (Invoke-UabTailscaleLogin $h)
}

function Get-UabWinLicense {
    # Licença do Windows instalada (ApplicationID do Windows; LicenseStatus 1 = ativado)
    Get-CimInstance SoftwareLicensingProduct -Filter "ApplicationID='55c92734-d682-4d71-983e-d6ec3f16059f' AND PartialProductKey IS NOT NULL" -ErrorAction SilentlyContinue |
        Select-Object -First 1
}

function Step-ClaudeCode {
    # O Claude Code instala no perfil do USUÁRIO (não do SYSTEM, que roda o provisionamento).
    # Agenda a instalação para o próximo logon do usuário via RunOnce (HKLM roda no contexto de
    # quem logar), com o script oficial da Anthropic. Roda uma vez; depois é só usar 'claude'.
    $cmd = 'powershell -NoProfile -ExecutionPolicy Bypass -Command "try { irm https://claude.ai/install.ps1 | iex } catch {}"'
    $k = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce'
    New-Item -Path $k -Force | Out-Null
    New-ItemProperty -Path $k -Name 'UABClaudeCode' -Value $cmd -PropertyType String -Force | Out-Null
    return 'ok (instala no próximo logon do usuário)'
}
function Step-Activation {
    # Ativa o Windows com a chave digitada no painel (aplicada na instalação). O Windows
    # também tentaria sozinho mais tarde; aqui a ativação é pedida já e o resultado vai
    # para o painel. Chave recusada: desiste após 3 tentativas, sem travar o resto.
    $lic = Get-UabWinLicense
    if ($lic -and $lic.LicenseStatus -eq 1) { return 'ok (Windows ativado)' }
    # contador de tentativas num arquivo próprio (o status.json só guarda os campos dele)
    $triesFile = Join-Path $UabRoot 'activation-tries'
    $tries = 0
    if (Test-Path -LiteralPath $triesFile) { $tries = [int](Get-Content -LiteralPath $triesFile -Raw) }
    if ($tries -ge 3) { return "ok (não ativado após 3 tentativas; confira a chave em Configurações > Sistema > Ativação)" }
    $out = (& cscript.exe //nologo "$env:SystemRoot\System32\slmgr.vbs" /ato 2>&1 | Out-String).Trim()
    $lic = Get-UabWinLicense
    if ($lic -and $lic.LicenseStatus -eq 1) { Log 'ativação: Windows ativado'; return 'ok (Windows ativado)' }
    $tries++
    Set-Content -LiteralPath $triesFile -Value $tries -Encoding ASCII
    $why = ($out -split "`r?`n" | Where-Object { $_.Trim() } | Select-Object -Last 1)
    if (-not $why) { $why = 'o servidor de ativação da Microsoft não confirmou' }
    Log "ativação: tentativa $tries falhou: $why"
    if ($tries -ge 3) { return "ok (não ativado após 3 tentativas: $why)" }
    throw "não ativado ainda ($why)"
}

function Step-Report {
    if (-not (Get-ScheduledTask -TaskName 'UAB Report' -ErrorAction SilentlyContinue)) {
        Register-UabTask -Name 'UAB Report' -Script (Join-Path $UabRoot 'report.ps1') -EveryMinutes 10 -AtBoot -LimitMinutes 5 `
            -Description 'ISO Builder: relatório ao painel (no boot e a cada 10 min).'
    }
    if (-not (Get-ScheduledTask -TaskName 'UAB Shutdown' -ErrorAction SilentlyContinue)) {
        Register-UabTask -Name 'UAB Shutdown' -Script (Join-Path $UabRoot 'report.ps1') -Arguments '-Shutdown' -OnShutdownEvent -LimitMinutes 2 `
            -Description 'ISO Builder: avisa o painel ao desligar/reiniciar.'
    }
}

# ---------------------------------------------------------------- execução
$plan = Get-UabPlan $conf
Set-UabStatus 'plan' $plan
Set-UabStatus 'provisioned' 'em andamento'

$net = $false
for ($i = 0; $i -lt 20 -and -not $net; $i++) { $net = Test-Internet; if (-not $net) { Start-Sleep 15 } }
if ($net) {
    if ($plan -contains 'openssh') { Invoke-Step 'openssh' { Step-OpenSSH } }
    if ($plan -contains 'wireguard') { Invoke-Step 'wireguard' { Step-WireGuard } }
    if ($plan -contains 'tailscale_install') {
        Invoke-Step 'tailscale_install' { Step-TailscaleInstall }
        if ((Get-UabStatus).steps['tailscale_install'] -like 'ok*') { Invoke-Step 'tailscale_login' { Step-TailscaleLogin } }
        else { $failed += 'tailscale_login' }
    }
    if ($plan -contains 'remote_agent') { Invoke-Step 'remote_agent' { Step-RemoteAgent } }
    if ($plan -contains 'apps') { Invoke-Step 'apps' { Step-Apps } }
    if ($plan -contains 'claude_code') { Invoke-Step 'claude_code' { Step-ClaudeCode } }
    if ($plan -contains 'activation') { Invoke-Step 'activation' { Step-Activation } }
} else {
    Log 'sem internet'
    $failed += 'network'
}
Invoke-Step 'report' { Step-Report }

if ($failed.Count) {
    Set-UabStatus 'provisioned' ("pendente: " + ($failed -join ' '))
    Log "=== etapas pendentes: $($failed -join ' ') — nova tentativa em 5 min ==="
} else {
    Set-UabStatus 'provisioned' ("ok " + (Get-UabNow))
    Log '=== pós-instalação concluída ==='
    Disable-UabAutoLogon  # tudo instalado: volta a pedir a senha e apaga a senha do registro
    Log 'login automático desligado; o Windows volta a pedir a senha no próximo boot'
    Disable-ScheduledTask -TaskName 'UAB Provision' -ErrorAction SilentlyContinue | Out-Null
}
& $UabPS -NoProfile -ExecutionPolicy Bypass -File (Join-Path $UabRoot 'report.ps1') 2>$null | Out-Null
exit 0
