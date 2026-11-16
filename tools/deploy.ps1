<#
  deploy.ps1 - spielt das zuletzt gebaute Paket auf den Stick (eine scp- und
  eine ssh-Verbindung) und fuehrt install.sh aus.

    .\tools\deploy.ps1 -StickHost 192.168.226.1
    .\tools\deploy.ps1 -StickHost 192.168.68.1 -Check     nur Bestandsaufnahme

  Voraussetzung: SSH-Schluessel fuer root auf dem Stick hinterlegt.
#>
param(
    [Parameter(Mandatory)] [string]$StickHost,
    [string]$User = 'root',
    [string]$Package,                # Standard: dist\vpndongle-<git describe>.tar.gz
    [switch]$Check
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $Package) {
    # genau die Version des aktuellen Stands - nie stillschweigend ein aelteres Paket
    $version = (git -C $root describe --tags --always --dirty 2>$null)
    $Package = Join-Path $root "dist\vpndongle-$version.tar.gz"
}
if (-not (Test-Path $Package)) { throw "Paket fehlt: $Package - zuerst tools\build.ps1 (oder -Package angeben)" }
$pkg = Get-Item $Package
Write-Host "Paket: $($pkg.Name) -> $User@$StickHost"

$remote = '/root/vpndongle-pkg.tar.gz'
# PowerShell 5.1: mit EAP Stop wird JEDE stderr-Zeile von ssh/scp (apt, sysctl ...)
# zum Abbruch - die SSH-Sitzung riss dann mitten in install.sh ab (2026-11-16).
# Daher lokal Continue, ausgewertet wird nur der Exit-Code.
$ErrorActionPreference = 'Continue'
scp.exe -q $pkg.FullName "$User@${StickHost}:$remote" 2>&1 | ForEach-Object { "$_" }
$rc = $LASTEXITCODE
$ErrorActionPreference = 'Stop'
if ($rc) { throw 'scp fehlgeschlagen' }

$arg = if ($Check) { '--check' } else { '' }
$cmd = "set -e; rm -rf /root/vpndongle-pkg; mkdir -p /root/vpndongle-pkg; tar -xzf $remote -C /root/vpndongle-pkg; sh /root/vpndongle-pkg/install.sh $arg"
$ErrorActionPreference = 'Continue'
ssh.exe "$User@$StickHost" $cmd 2>&1 | ForEach-Object { "$_" }
$rc = $LASTEXITCODE
$ErrorActionPreference = 'Stop'
if ($rc) { throw "install.sh meldet Fehler ($rc)" }
Write-Host 'install.sh fertig (Exit 0)' -ForegroundColor Green
