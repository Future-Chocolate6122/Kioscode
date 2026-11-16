<#
.SYNOPSIS
  Desinstala Tótem Wi-Fi.

.DESCRIPTION
  Detiene y elimina el servicio, la regla del Firewall y %ProgramFiles%\TotemWifi.
  Conserva %ProgramData%\TotemWifi (configuración, contador y log) salvo con -Purge.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .\uninstall.ps1
  powershell -ExecutionPolicy Bypass -File .\uninstall.ps1 -Purge
#>
#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [switch]$Purge
)
$ErrorActionPreference = 'Stop'

$ServiceName = 'TotemWifi'
$RuleName    = 'Totem Wi-Fi (HTTP)'
$ProgDir = Join-Path $env:ProgramFiles 'TotemWifi'
$DataDir = Join-Path $env:ProgramData 'TotemWifi'
$Exe     = Join-Path $ProgDir 'totem-wifi.exe'

$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($svc) {
    if ($svc.Status -ne 'Stopped') {
        Stop-Service -Name $ServiceName -Force
        $svc.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
    if (Test-Path $Exe) {
        & $Exe uninstall | Out-Null
    } else {
        sc.exe delete $ServiceName | Out-Null
    }
    Write-Host 'Servicio eliminado.'
}

Get-NetFirewallRule -DisplayName $RuleName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
Write-Host 'Regla del Firewall eliminada.'

Start-Sleep -Seconds 1
if (Test-Path $ProgDir) {
    Remove-Item -Recurse -Force $ProgDir
    Write-Host "Programa eliminado ($ProgDir)."
}

if ($Purge) {
    if (Test-Path $DataDir) { Remove-Item -Recurse -Force $DataDir }
    Write-Host "Configuración y datos eliminados ($DataDir)."
} else {
    Write-Host "Se conserva $DataDir (use -Purge para borrarlo)."
}
