# ubuntu-autoinstall-builder (Windows) — funções comuns dos scripts em C:\ProgramData\uab.
# Carregado com ". C:\ProgramData\uab\common.ps1" por specialize.ps1, provision.ps1 e report.ps1.
# Roda como SYSTEM (tarefas agendadas) no PowerShell 5.1 do Windows.

$UabRoot = Join-Path $env:ProgramData 'uab'
$UabLogs = Join-Path $UabRoot 'logs'
$UabStatusFile = Join-Path $UabRoot 'status.json'
$UabPS = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
New-Item -ItemType Directory -Force -Path $UabLogs | Out-Null

function Write-UabLog {
    param([string]$Message, [string]$Name = 'provision')
    $line = '{0} {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Message
    $f = Join-Path $UabLogs "$Name.log"
    try {
        # log não cresce sem limite: guarda só o último ~1 MB
        if ((Test-Path $f) -and (Get-Item $f).Length -gt 1MB) { Move-Item -Force $f "$f.old" }
        Add-Content -LiteralPath $f -Value $line -Encoding UTF8
    } catch { }
}

function Get-UabConf {
    Get-Content -LiteralPath (Join-Path $UabRoot 'uab.json') -Raw -Encoding UTF8 | ConvertFrom-Json
}

function Read-UabSecret([string]$Name) {
    $f = Join-Path $UabRoot $Name
    if (Test-Path -LiteralPath $f) { return ((Get-Content -LiteralPath $f -Raw -Encoding UTF8) -replace '\s', '') }
    return ''
}

function Write-UabFileAtomic([string]$Path, [string]$Text, [switch]$Bom) {
    $tmp = "$Path.tmp"
    [IO.File]::WriteAllText($tmp, $Text, (New-Object Text.UTF8Encoding ([bool]$Bom)))
    Move-Item -Force -LiteralPath $tmp -Destination $Path
}

# ---------------------------------------------------------------- status.json
# { "plan": [...], "current": "", "provisioned": "", "steps": { "openssh": "ok", ... } }
function Get-UabStatus {
    $st = @{ plan = @(); current = ''; provisioned = ''; steps = @{} }
    try {
        $j = Get-Content -LiteralPath $UabStatusFile -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($j.plan) { $st.plan = @($j.plan) }
        if ($j.current) { $st.current = [string]$j.current }
        if ($j.provisioned) { $st.provisioned = [string]$j.provisioned }
        if ($j.steps) { foreach ($p in $j.steps.PSObject.Properties) { $st.steps[$p.Name] = [string]$p.Value } }
        if ($j.apps) { $st.apps = $j.apps }
    } catch { }
    return $st
}

function Save-UabStatus($st) {
    $out = [ordered]@{ plan = @($st.plan); current = $st.current; provisioned = $st.provisioned; steps = $st.steps }
    if ($st.apps) { $out.apps = $st.apps }
    Write-UabFileAtomic $UabStatusFile (ConvertTo-Json -InputObject $out -Depth 5)
}

function Set-UabStatus([string]$Key, $Value) {
    $st = Get-UabStatus
    $st[$Key] = $Value
    Save-UabStatus $st
}

function Set-UabStep([string]$Step, [string]$Value) {
    $st = Get-UabStatus
    $st.steps[$Step] = $Value
    Save-UabStatus $st
}

function Get-UabNow { Get-Date -Format 'yyyy-MM-dd HH:mm:ss' }

# ---------------------------------------------------------------- tarefas agendadas
function Register-UabTask {
    <# Registra (ou substitui) uma tarefa que roda um script como SYSTEM.
       -EveryMinutes: repete a cada N min para sempre; -AtBoot: no boot (com atraso);
       -OnShutdownEvent: quando o Windows registra "desligando/reiniciando" (User32 1074). #>
    param([string]$Name, [string]$Script, [string]$Arguments = '', [int]$EveryMinutes = 0,
          [switch]$AtBoot, [switch]$OnShutdownEvent, [string]$Description = '', [int]$LimitMinutes = 240)
    $esc = { param($s) [Security.SecurityElement]::Escape([string]$s) }
    $trig = ''
    if ($AtBoot) { $trig += '<BootTrigger><Enabled>true</Enabled><Delay>PT1M</Delay></BootTrigger>' }
    if ($EveryMinutes -gt 0) {
        $trig += "<TimeTrigger><Repetition><Interval>PT${EveryMinutes}M</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition>" +
                 '<StartBoundary>2020-01-01T00:00:00</StartBoundary><Enabled>true</Enabled></TimeTrigger>'
    }
    if ($OnShutdownEvent) {
        $q = "<QueryList><Query Id=`"0`" Path=`"System`"><Select Path=`"System`">*[System[Provider[@Name='User32'] and EventID=1074]]</Select></Query></QueryList>"
        $trig += '<EventTrigger><Enabled>true</Enabled><Subscription>' + (& $esc $q) + '</Subscription></EventTrigger>'
    }
    $args_ = "-NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$Script`" $Arguments".Trim()
    $xml = @"
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Author>ISO Builder</Author><Description>$(& $esc $Description)</Description></RegistrationInfo>
  <Triggers>$trig</Triggers>
  <Principals><Principal id="Author"><UserId>S-1-5-18</UserId><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <ExecutionTimeLimit>PT${LimitMinutes}M</ExecutionTimeLimit>
    <Enabled>true</Enabled>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>$(& $esc $UabPS)</Command><Arguments>$(& $esc $args_)</Arguments></Exec></Actions>
</Task>
"@
    try {
        Register-ScheduledTask -TaskName $Name -Xml $xml -Force -ErrorAction Stop | Out-Null
    } catch {
        # alternativa: schtasks.exe com o mesmo XML
        $f = Join-Path $env:TEMP "uab-task-$([guid]::NewGuid()).xml"
        [IO.File]::WriteAllText($f, $xml, [Text.Encoding]::Unicode)
        & schtasks.exe /Create /F /TN $Name /XML $f | Out-Null
        $rc = $LASTEXITCODE
        Remove-Item -Force $f -ErrorAction SilentlyContinue
        if ($rc -ne 0) { throw "não foi possível criar a tarefa '$Name': $($_.Exception.Message)" }
    }
}

# ---------------------------------------------------------------- winget como SYSTEM
function Get-UabWinget {
    <# winget.exe não está no PATH do SYSTEM (é um app MSIX por usuário). O executável
       fica na pasta do pacote provisionado em Program Files\WindowsApps; roda como
       SYSTEM direto dali. Devolve o caminho da versão mais nova, ou $null. #>
    $dirs = Get-ChildItem -Path (Join-Path $env:ProgramFiles 'WindowsApps') -Directory -Filter 'Microsoft.DesktopAppInstaller_*_x64__8wekyb3d8bbwe' -ErrorAction SilentlyContinue
    $best = $null; $bestV = [version]'0.0'
    foreach ($d in $dirs) {
        $exe = Join-Path $d.FullName 'winget.exe'
        if (-not (Test-Path -LiteralPath $exe)) { continue }
        $v = [version]'0.0'
        [void][version]::TryParse(($d.Name -split '_')[1], [ref]$v)
        if ($v -gt $bestV) { $bestV = $v; $best = $exe }
    }
    if ($best) { Add-UabWingetDeps }
    return $best
}

function Add-UabWingetDeps {
    <# Como SYSTEM o pacote não está registrado e o Windows não resolve as dependências do
       MSIX: sem isto o winget.exe morre com 0xC0000135 (DLL não encontrada). Põe as pastas
       das dependências (VCLibs, Windows App Runtime, UI.Xaml) no PATH deste processo; os
       processos filhos (winget) herdam. #>
    $apps = Join-Path $env:ProgramFiles 'WindowsApps'
    $add = @()
    foreach ($pat in 'Microsoft.VCLibs.140.00.UWPDesktop_*_x64__8wekyb3d8bbwe', 'Microsoft.VCLibs.140.00_*_x64__8wekyb3d8bbwe',
                     'Microsoft.WindowsAppRuntime.*_x64__8wekyb3d8bbwe', 'Microsoft.UI.Xaml.*_x64__8wekyb3d8bbwe') {
        $d = Get-ChildItem -Path $apps -Directory -Filter $pat -ErrorAction SilentlyContinue |
            Sort-Object { $v = [version]'0.0'; [void][version]::TryParse(($_.Name -split '_')[1], [ref]$v); $v } |
            Select-Object -Last 1
        if ($d -and ($env:PATH -split ';') -notcontains $d.FullName) { $add += $d.FullName }
    }
    if ($add) { $env:PATH = (($add -join ';') + ';' + $env:PATH) }
}

function Invoke-UabExe {
    <# Roda um programa de console escondido, com limite de tempo, e devolve
       @{ code = <exit code, -1 se estourou o tempo>; out = <stdout + stderr> }. #>
    param([string]$Exe, [string[]]$Arguments, [int]$TimeoutSeconds = 300)
    $o = Join-Path $env:TEMP "uab-exe-$([guid]::NewGuid()).txt"
    $e = "$o.err"
    $p = Start-Process -FilePath $Exe -ArgumentList $Arguments -NoNewWindow -PassThru `
        -RedirectStandardOutput $o -RedirectStandardError $e -WorkingDirectory (Split-Path $Exe)
    $null = $p.Handle  # sem isto o ExitCode volta vazio (Start-Process -PassThru com redirecionamento)
    if (-not $p.WaitForExit($TimeoutSeconds * 1000)) {
        try { $p.Kill() } catch { }
        $code = -1
    } else { $p.WaitForExit(); $code = $p.ExitCode }
    $out = ''
    foreach ($f in @($o, $e)) {
        if (Test-Path $f) { $out += [string](Get-Content -LiteralPath $f -Raw -Encoding UTF8 -ErrorAction SilentlyContinue); Remove-Item -Force $f -ErrorAction SilentlyContinue }
    }
    return @{ code = $code; out = $out }
}

function Invoke-UabWinget {
    <# Roda o winget e devolve @{ code = <exit code>; out = <saída> }. #>
    param([string]$Exe, [string[]]$Arguments, [int]$TimeoutMinutes = 45)
    $r = Invoke-UabExe $Exe $Arguments ($TimeoutMinutes * 60)
    $code = $r.code; $out = $r.out
    # tira as animações de progresso (barras e "\|/-") da saída
    $out = ($out -split "`r?`n" | ForEach-Object { ($_ -split "`r")[-1] } | Where-Object { $_ -match '\w' -and $_ -notmatch '^\s*[-\\|/]\s*$' -and $_ -notmatch '[█▒]' }) -join "`n"
    return @{ code = $code; out = $out }
}

# ---------------------------------------------------------------- plano da pós-instalação
function Get-UabPlan($conf) {
    <# Etapas previstas (mesma ordem em specialize.ps1 e provision.ps1). #>
    $plan = @()
    if ($conf.panel_access) { $plan += 'openssh' }
    if ($conf.wireguard) { $plan += 'wireguard' }
    if ($conf.tailscale) { $plan += 'tailscale_install'; $plan += 'tailscale_login' }
    if ($conf.remote_agent -and $conf.remote_url) { $plan += 'remote_agent' }
    if (@($conf.apps).Count -gt 0) { $plan += 'apps' }
    if ($conf.activate) { $plan += 'activation' }  # só com a chave informada no painel
    $plan += 'report'
    return , $plan
}
