<#
  build.ps1 - baut vpndongled fuer den UZ801 (linux/arm64, statisch) und
  packt das Installationspaket dist\vpndongle-<version>.tar.gz.

    .\tools\build.ps1             Tests + Build + Paket (Entwicklungsstand)
    .\tools\build.ps1 -NoTest     ohne Tests
    .\tools\build.ps1 -Release    nur aus sauberem, getaggtem Stand (vX.Y.Z)

  Version = git describe:
    v0.2.0                    Release (Commit hat genau dieses Tag)
    v0.2.0-3-gabc1234         3 Commits nach v0.2.0
    v0.2.0-3-gabc1234-dirty   ... mit nicht eingecheckten Aenderungen

  Paketinhalt: bin/vpndongled, install.sh, system/, VERSION, LICENSE,
  THIRD_PARTY_NOTICES (Lizenz der Go-Standardbibliothek, steckt im Binary).

  Go: nimmt go aus PATH, sonst $env:USERPROFILE\go-sdk\go\bin\go.exe
#>
param([switch]$NoTest, [switch]$Release)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { $go = Join-Path $env:USERPROFILE 'go-sdk\go\bin\go.exe' }
if (-not (Test-Path $go)) { throw 'Go nicht gefunden' }

$changes = @(git status --porcelain)
if ($Release) {
    if ($changes) { throw "Release nur aus sauberem Stand - nicht eingecheckt:`n$($changes -join "`n")" }
    $version = (git describe --exact-match --tags HEAD 2>$null)
    if (-not $version -or $version -notmatch '^v\d+\.\d+\.\d+$') {
        throw 'Release: HEAD traegt kein Tag vX.Y.Z (git tag -a vX.Y.Z -m ...)'
    }
} else {
    $version = (git describe --tags --always --dirty 2>$null)
    if (-not $version) { $version = 'dev' }
    if ($changes) { Write-Warning "Nicht eingecheckte Aenderungen - Version $version (kein Release)" }
}
Write-Host "Version: $version"

if (-not $NoTest) {
    & $go vet ./...
    if ($LASTEXITCODE) { throw 'go vet fehlgeschlagen' }
    & $go test ./...
    if ($LASTEXITCODE) { throw 'Tests fehlgeschlagen' }
}

$stage = Join-Path $root 'dist\stage'
if (Test-Path $stage) { Remove-Item -Recurse -Force $stage -Confirm:$false }
New-Item -ItemType Directory -Force (Join-Path $stage 'bin') | Out-Null

$env:GOOS = 'linux'; $env:GOARCH = 'arm64'; $env:CGO_ENABLED = '0'
try {
    & $go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$version" -o (Join-Path $stage 'bin\vpndongled') ./cmd/vpndongled
    if ($LASTEXITCODE) { throw 'Build fehlgeschlagen' }
} finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
}

Copy-Item -Recurse (Join-Path $root 'system') (Join-Path $stage 'system')
Move-Item (Join-Path $stage 'system\install.sh') (Join-Path $stage 'install.sh')
Copy-Item (Join-Path $root 'LICENSE') (Join-Path $stage 'LICENSE')
$u8 = New-Object Text.UTF8Encoding $false
[IO.File]::WriteAllText((Join-Path $stage 'VERSION'), "$version`n", $u8)
$goLicense = [IO.File]::ReadAllText((Join-Path (& $go env GOROOT) 'LICENSE'))
$goVer = (& $go env GOVERSION)
$notice = @"
OpenStick-VPN-Dongle $version - Hinweise zu Fremdsoftware
=============================================================

bin/vpndongled ist mit Go ($goVer) gebaut und enthaelt Teile der
Go-Standardbibliothek. Weitere Bibliotheken sind nicht enthalten.

Go-Standardbibliothek - https://go.dev
Lizenz (BSD-3-Clause):

$goLicense
"@
[IO.File]::WriteAllText((Join-Path $stage 'THIRD_PARTY_NOTICES'), ($notice -replace "`r`n", "`n"), $u8)

# Shell-Skripte mit LF (Windows-Checkout koennte CRLF haben)
Get-ChildItem $stage -Recurse -Include *.sh, *.service, *.timer, *.conf | ForEach-Object {
    $t = [IO.File]::ReadAllText($_.FullName) -replace "`r`n", "`n"
    [IO.File]::WriteAllText($_.FullName, $t, $u8)
}

$pkg = Join-Path $root "dist\vpndongle-$version.tar.gz"
# Windows-bsdtar explizit (aus Git-Bash heraus wuerde sonst GNU-tar greifen,
# das "C:" als Remote-Host deutet)
& (Join-Path $env:SystemRoot 'System32\tar.exe') -czf $pkg -C $stage .
if ($LASTEXITCODE) { throw 'tar fehlgeschlagen' }
$h = (Get-FileHash $pkg -Algorithm SHA256).Hash.ToLower()
Write-Host ("Paket: {0} ({1:N0} KB)`nSHA256 {2}" -f $pkg, ((Get-Item $pkg).Length / 1KB), $h) -ForegroundColor Green
