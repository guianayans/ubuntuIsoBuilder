# ubuntu-autoinstall-builder (Windows) — relatório ao painel ("estou vivo").
# Mesmo JSON do Linux (payload/uab-report.sh), com platform = "windows". Sem segredos.
# Tarefas "UAB Report" (boot e a cada 10 min) e "UAB Shutdown" (-Shutdown, ao desligar).
# Com WireGuard: a resposta do painel traz a configuração do túnel, aplicada por vpn.ps1.
# Uso manual (PowerShell como administrador): & C:\ProgramData\uab\report.ps1 -Verbose
[CmdletBinding()]
param([switch]$Shutdown)
$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'
. (Join-Path $env:ProgramData 'uab\common.ps1')
. (Join-Path $env:ProgramData 'uab\vpn.ps1')
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.ServicePointManager]::SecurityProtocol

# No boot o report pode disparar antes da config estar pronta: sem ela, sai quieto (sem
# cuspir "não foi possível localizar ...uab.json" no console). A próxima repetição relata.
$conf = try { Get-UabConf } catch { $null }
$token = Read-UabSecret 'panel-token'
if (-not $conf -or -not $conf.panel_url -or -not $token) { exit 0 }
$url = $conf.panel_url.TrimEnd('/') + '/api/agent/report'
$hdr = @{ Authorization = "Bearer $token" }

$machineId = ((Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Cryptography').MachineGuid -replace '-', '').ToLower()

if ($Shutdown) {
    $reason = 'poweroff'
    try {
        $ev = Get-WinEvent -FilterHashtable @{ LogName = 'System'; ProviderName = 'User32'; Id = 1074 } -MaxEvents 1
        # tipo do desligamento (texto no idioma do Windows): reiniciar/restart
        if ($ev -and (($ev.Properties | ForEach-Object { [string]$_.Value }) -join ' ') -match 'restart|reinici') { $reason = 'reboot' }
    } catch { }
    $body = ConvertTo-Json -InputObject ([ordered]@{ machine_id = $machineId; reason = $reason }) -Compress
    try {
        Invoke-RestMethod -UseBasicParsing -Method Post -Uri $url -Headers ($hdr + @{ 'X-UAB-Event' = 'shutdown' }) `
            -ContentType 'application/json' -Body ([Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 6 | Out-Null
    } catch { }
    exit 0
}

function Get-HwId {
    <# Identidade do HARDWARE (não muda ao formatar), mesma regra do Linux: UUID da
       placa-mãe (SMBIOS) ou, se genérico/vazio, o MAC da 1ª placa de rede física. #>
    $ident = ''
    try {
        $u = ([string](Get-CimInstance Win32_ComputerSystemProduct).UUID).Trim().ToLower()
        $h = $u -replace '-', ''
        $distinct = @($h.ToCharArray() | Sort-Object -Unique).Count
        if ($h -match '^[0-9a-f]{32}$' -and $distinct -gt 2 -and
            @('03000200-0400-0500-0006-000700080009', '00020003-0004-0005-0006-000700080009') -notcontains $u) {
            $ident = "smbios:$u"
        }
    } catch { }
    if (-not $ident) {
        try {
            $a = Get-NetAdapter -Physical -ErrorAction Stop | Sort-Object InterfaceIndex |
                Where-Object { $_.MacAddress -and $_.MacAddress -ne '00-00-00-00-00-00' } | Select-Object -First 1
            if ($a) { $ident = 'mac:' + ($a.MacAddress -replace '-', ':').ToLower() }
        } catch { }
    }
    if (-not $ident) { return '' }
    $sha = [Security.Cryptography.SHA256]::Create()
    return (($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes("uab-hw:$ident")) | ForEach-Object { $_.ToString('x2') }) -join '')
}

function Test-Svc([string]$n) {
    $s = Get-Service -Name $n -ErrorAction SilentlyContinue
    return [bool]($s -and $s.Status -eq 'Running')
}

$os = Get-CimInstance Win32_OperatingSystem
$cv = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
$kernel = '{0}.{1}.{2}' -f $os.Version.Split('.')[0], $os.Version.Split('.')[1], $cv.CurrentBuildNumber
if ($null -ne $cv.UBR) { $kernel += ".$($cv.UBR)" }
if ($cv.DisplayVersion) { $kernel += " ($($cv.DisplayVersion))" }

$local = @()
try {
    foreach ($ip in (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction Stop | Sort-Object InterfaceIndex)) {
        if ($ip.IPAddress -like '127.*' -or $ip.IPAddress -like '169.254.*') { continue }
        if ($ip.InterfaceAlias -match '^(vEthernet|Loopback|Tailscale|ZeroTier)' -or $ip.InterfaceAlias -eq $UabWgName) { continue }
        $local += "$($ip.InterfaceAlias) $($ip.IPAddress)/$($ip.PrefixLength)"
    }
} catch { }
$gw = ''
try { $gw = [string](Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction Stop | Sort-Object RouteMetric | Select-Object -First 1).NextHop } catch { }

$st = Get-UabStatus
$steps = [ordered]@{}
foreach ($k in $st.steps.Keys) { $steps[$k] = $st.steps[$k] }

$services = [ordered]@{ ssh = (Test-Svc 'sshd') }
if ($conf.remote_agent) { $services.meshagent = (Test-Svc 'Mesh Agent') }
if ($conf.wireguard) { $services.wireguard = (Test-Svc $UabWgService) }
if ($conf.tailscale) { $services.tailscaled = (Test-Svc 'Tailscale') }
$pt = Get-ScheduledTask -TaskName 'UAB Provision' -ErrorAction SilentlyContinue
# a própria pós-instalação manda o último relatório ainda rodando: concluída = parada
$services.'uab-provision' = [bool]($pt -and $pt.State -eq 'Running' -and $st.provisioned -notlike 'ok*')

$cpus = [Environment]::ProcessorCount
$load = 0
try { $load = [math]::Round(((Get-CimInstance Win32_Processor | Measure-Object LoadPercentage -Average).Average / 100) * $cpus, 2) } catch { }
$disk = Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='$($env:SystemDrive)'"
$ssh_port = 22
try {
    $cfg = Get-Content -LiteralPath (Join-Path $env:ProgramData 'ssh\sshd_config') -ErrorAction Stop |
        Where-Object { $_ -match '^\s*Port\s+(\d+)' } | Select-Object -First 1
    if ($cfg -match '(\d+)') { $ssh_port = [int]$Matches[1] }
} catch { }

$rep = [ordered]@{
    machine_id        = $machineId
    hw_id             = (Get-HwId)
    hostname          = $env:COMPUTERNAME.ToLower()
    build_id          = [string]$conf.build_id
    platform          = 'windows'
    admin_user        = [string]$conf.admin_user
    claude            = [bool](Test-Path ("$env:SystemDrive\Users\" + [string]$conf.admin_user + "\.local\bin\claude.exe"))
    ssh_auth          = 'key'
    ssh_port          = $ssh_port
    os                = [string]$os.Caption
    kernel            = $kernel
    uptime_seconds    = [int][math]::Floor(((Get-Date) - $os.LastBootUpTime).TotalSeconds)
    tailscale         = $(if ($conf.tailscale) { Get-UabTsReport } else { @{} })
    local_ips         = @($local)
    gateway           = $gw
    provisioned       = [string]$st.provisioned
    provision_plan    = @($st.plan)
    provision_current = [string]$st.current
    steps             = $steps
    services          = $services
    docker_containers = $null
    wg                = $(if ($conf.wireguard) { Get-UabWgReport } else { $null })
    resources         = [ordered]@{
        cpus = $cpus; load1 = $load
        mem_total = [int64]$os.TotalVisibleMemorySize * 1024; mem_available = [int64]$os.FreePhysicalMemory * 1024
        disk_total = [int64]$disk.Size; disk_free = [int64]$disk.FreeSpace
    }
}
if ($st.apps) { $rep.apps = $st.apps }
$body = ConvertTo-Json -InputObject $rep -Depth 5 -Compress
Write-Verbose $body

$ok = $false
for ($i = 0; $i -lt 3 -and -not $ok; $i++) {
    try {
        $resp = Invoke-RestMethod -UseBasicParsing -Method Post -Uri $url -Headers $hdr -ContentType 'application/json' `
            -Body ([Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 20
        $ok = $true
        Write-Verbose ("resposta: " + ($resp | ConvertTo-Json -Compress -Depth 4))  # sem segredos
    } catch {
        $code = 0
        try { $code = [int]$_.Exception.Response.StatusCode } catch { }
        Write-Verbose "falhou: $($_.Exception.Message)"
        if ($code -eq 429) { Start-Sleep 6 } elseif ($code -ge 400 -and $code -lt 500) { break } else { Start-Sleep 5 }
    }
}
if ($ok) {
    Set-Content -LiteralPath (Join-Path $UabRoot 'last-report') -Value (Get-UabNow) -Encoding ASCII
    # túnel WireGuard até o hub: a resposta do painel traz a configuração
    if ($conf.wireguard -and $resp -and ($resp.PSObject.Properties.Name -contains 'wg')) {
        try { Invoke-UabWgApply $resp.wg } catch { Write-UabLog "wg: erro ao aplicar: $($_.Exception.Message)" 'report' }
    }
}
else { Write-UabLog "relatório não enviado a $url" 'report' }
exit 0
