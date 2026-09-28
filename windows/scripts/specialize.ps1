# ubuntu-autoinstall-builder (Windows) — passo "specialize" (antes do OOBE), como SYSTEM.
# Os arquivos de C:\ProgramData\uab já foram extraídos do unattend.xml pelo comando que
# chama este script. Aqui: protege a pasta (o token do painel não pode ser lido por
# usuários comuns), cria as tarefas agendadas e ajustes do OOBE.
$ErrorActionPreference = 'Continue'
. (Join-Path $env:ProgramData 'uab\common.ps1')
Write-UabLog 'specialize: início' 'specialize'

# pasta só para SYSTEM e Administradores (por SID: nomes de grupo mudam com o idioma)
# (só na pasta; os arquivos herdam — (OI)(CI) em arquivo com /T deixaria o arquivo sem
# permissão nenhuma, nem para o SYSTEM)
& icacls.exe $UabRoot /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' /Q | Out-Null
$rc1 = $LASTEXITCODE
& icacls.exe "$UabRoot\*" /reset /T /C /Q | Out-Null
Write-UabLog "specialize: ACL de $UabRoot (códigos $rc1 $LASTEXITCODE)" 'specialize'

# conta Microsoft/rede não obrigatórias no OOBE (as telas já estão ocultas; garantia extra)
& reg.exe add 'HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\OOBE' /v BypassNRO /t REG_DWORD /d 1 /f | Out-Null

$conf = Get-UabConf
$plan = Get-UabPlan $conf
$st = Get-UabStatus
$st.plan = $plan
$st.provisioned = 'aguardando o fim da instalação do Windows'
Save-UabStatus $st

$prov = Join-Path $UabRoot 'provision.ps1'
$rep = Join-Path $UabRoot 'report.ps1'
foreach ($t in @(
        @{ n = 'UAB Provision'; s = $prov; a = ''; m = 5; boot = $true; sd = $false; lim = 240; d = 'ISO Builder: pós-instalação (OpenSSH, VPN, Remoto, programas). Repete até concluir.' },
        @{ n = 'UAB Report'; s = $rep; a = ''; m = 10; boot = $true; sd = $false; lim = 5; d = 'ISO Builder: relatório ao painel (no boot e a cada 10 min).' },
        @{ n = 'UAB Shutdown'; s = $rep; a = '-Shutdown'; m = 0; boot = $false; sd = $true; lim = 2; d = 'ISO Builder: avisa o painel ao desligar/reiniciar.' })) {
    try {
        Register-UabTask -Name $t.n -Script $t.s -Arguments $t.a -EveryMinutes $t.m -AtBoot:$t.boot `
            -OnShutdownEvent:$t.sd -LimitMinutes $t.lim -Description $t.d
        Write-UabLog "specialize: tarefa '$($t.n)' criada" 'specialize'
    } catch {
        Write-UabLog "specialize: ERRO ao criar a tarefa '$($t.n)': $_" 'specialize'
    }
}
Write-UabLog 'specialize: fim' 'specialize'
exit 0
