<#
.SYNOPSIS
  Instala (o actualiza) Tótem Wi-Fi como servicio de Windows.

.DESCRIPTION
  - Crea %ProgramData%\TotemWifi (configuración, datos y log) con permisos solo
    para SYSTEM y Administradores, porque config.json guarda la API key.
  - Copia totem-wifi.exe a %ProgramFiles%\TotemWifi.
  - Registra el servicio «TotemWifi» (arranque automático, reinicio si falla).
  - Abre el puerto en el Firewall de Windows SOLO para las IPs de allowed_clients
    y admin_allowed_clients.
  - Arranca el servicio y abre http://localhost:8080/admin.
  No descarga nada de Internet. Conserva config.json si ya existe.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .\install.ps1
#>
#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [switch]$NoBrowser
)
$ErrorActionPreference = 'Stop'

$ServiceName = 'TotemWifi'
$RuleName    = 'Totem Wi-Fi (HTTP)'
$Src     = Join-Path $PSScriptRoot 'totem-wifi.exe'
$ProgDir = Join-Path $env:ProgramFiles 'TotemWifi'
$DataDir = Join-Path $env:ProgramData 'TotemWifi'
$Exe     = Join-Path $ProgDir 'totem-wifi.exe'
$Cfg     = Join-Path $DataDir 'config.json'

if (-not (Test-Path $Src)) {
    throw "No se encuentra totem-wifi.exe junto a este script ($PSScriptRoot)."
}

Write-Host '1/6 Parando la versión anterior (si existe)'
$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($svc) {
    if ($svc.Status -ne 'Stopped') {
        Stop-Service -Name $ServiceName -Force
        $svc.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
    if (Test-Path $Exe) {
        & $Exe uninstall -config $Cfg -data $DataDir | Out-Null
    } else {
        sc.exe delete $ServiceName | Out-Null
    }
    Start-Sleep -Seconds 1
}

Write-Host "2/6 Copiando el programa a $ProgDir"
New-Item -ItemType Directory -Force -Path $ProgDir | Out-Null
Copy-Item -Path $Src -Destination $Exe -Force
Unblock-File -Path $Exe -ErrorAction SilentlyContinue

Write-Host "3/6 Preparando $DataDir (solo SYSTEM y Administradores)"
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
# S-1-5-18 = SYSTEM, S-1-5-32-544 = Administradores (independiente del idioma de Windows)
& icacls.exe $DataDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'No se han podido ajustar los permisos de la carpeta de datos.' }
if (-not (Test-Path $Cfg)) {
    & $Exe -config $Cfg -data $DataDir init
    if ($LASTEXITCODE -ne 0) { throw 'No se ha podido crear config.json.' }
}

Write-Host "4/6 Registrando el servicio $ServiceName"
& $Exe install -config $Cfg -data $DataDir
if ($LASTEXITCODE -ne 0) { throw 'No se ha podido registrar el servicio.' }

Write-Host '5/6 Firewall de Windows'
$port = '8080'
$addrs = @()
foreach ($line in (& $Exe -config $Cfg -data $DataDir allowlist)) {
    if ($line -like 'port *') { $port = $line.Substring(5).Trim() }
    elseif ($line -like 'addr *') { $addrs += $line.Substring(5).Trim() }
}
Get-NetFirewallRule -DisplayName $RuleName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
if ($addrs.Count -gt 0) {
    New-NetFirewallRule -DisplayName $RuleName -Description 'Quiosco Tótem Wi-Fi: solo los equipos de allowed_clients y admin_allowed_clients' `
        -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port `
        -RemoteAddress $addrs -Program $Exe -Profile Any | Out-Null
    Write-Host ("    Puerto {0} abierto solo para: {1}" -f $port, ($addrs -join ', '))
} else {
    Write-Host '    Lista vacía: solo este equipo (localhost) puede usar el quiosco; no se abre el puerto.'
}

Write-Host '6/6 Arrancando el servicio'
& $Exe start -config $Cfg -data $DataDir
if ($LASTEXITCODE -ne 0) {
    throw "El servicio no ha arrancado. Revise $DataDir\totem-wifi.log y el Visor de eventos."
}
Start-Sleep -Seconds 2

Write-Host ''
Write-Host 'Instalación terminada (modo simulación hasta que configure UniFi).' -ForegroundColor Green
Write-Host "  Administración: http://localhost:$port/admin"
Write-Host "  Quiosco:        http://IP_DE_ESTE_EQUIPO:$port/"
Write-Host "  Log:            $DataDir\totem-wifi.log"
if (-not $NoBrowser) {
    Start-Process "http://localhost:$port/admin"
}
