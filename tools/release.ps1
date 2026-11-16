<#
  release.ps1 - baut ein Release, signiert es und legt es (mit -Publish) auf GitHub an.

    git tag -a v0.2.0 -m "v0.2.0"
    .\tools\release.ps1 -KeyFile <privater-schluessel>             nur bauen + signieren
    .\tools\release.ps1 -KeyFile <privater-schluessel> -Publish    + GitHub-Release

  Der Schluessel kann auch in $env:VPNDONGLE_SIGNING_KEY stehen (Pfad zur Datei).
  Er gehoert NIE ins Repo. Erzeugt wird er einmalig mit
    go run ./cmd/relsign genkey -out <datei>
  und der ausgegebene oeffentliche Schluessel kommt nach internal/swupdate/pubkey.go.

  Ergebnis in dist\release-<tag>\:
    vpndongle-<tag>.tar.gz            Installationspaket (Web-UI oder Selbst-Update)
    template_openstick_vpn_dongle.json  Zabbix-7.0-Vorlage
    SHA256SUMS, SHA256SUMS.sig        Pruefsummen + Ed25519-Signatur (prueft der Stick)

  Release-Notizen: Abschnitt "## <tag>" aus CHANGELOG.md.
#>
param(
    [string]$KeyFile = $env:VPNDONGLE_SIGNING_KEY,
    [switch]$Publish
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
if (-not $KeyFile -or -not (Test-Path $KeyFile)) { throw 'Signaturschluessel fehlt (-KeyFile oder $env:VPNDONGLE_SIGNING_KEY)' }

$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { $go = Join-Path $env:USERPROFILE 'go-sdk\go\bin\go.exe' }

& (Join-Path $PSScriptRoot 'build.ps1') -Release
$tag = (git describe --exact-match --tags HEAD)
$pkg = Join-Path $root "dist\vpndongle-$tag.tar.gz"

$out = Join-Path $root "dist\release-$tag"
if (Test-Path $out) { Remove-Item -Recurse -Force $out -Confirm:$false }
New-Item -ItemType Directory $out | Out-Null
Copy-Item $pkg $out
Copy-Item (Join-Path $root 'zabbix\template_openstick_vpn_dongle.json') $out

$sums = Get-ChildItem $out -File | Sort-Object Name | ForEach-Object {
    "{0}  {1}" -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name }
$sumsFile = Join-Path $out 'SHA256SUMS'
[IO.File]::WriteAllText($sumsFile, (($sums -join "`n") + "`n"), (New-Object Text.UTF8Encoding $false))

& $go run ./cmd/relsign sign -key $KeyFile $sumsFile
if ($LASTEXITCODE) { throw 'Signieren fehlgeschlagen' }
& $go run ./cmd/relsign verify $sumsFile
if ($LASTEXITCODE) { throw 'Signatur laesst sich nicht pruefen' }

# Release-Notizen aus CHANGELOG.md
$notes = Join-Path $out 'NOTES.md'
$cl = Get-Content (Join-Path $root 'CHANGELOG.md') -Raw -Encoding UTF8
$m = [regex]::Match($cl, "(?ms)^## $([regex]::Escape($tag))\b[^\n]*\n(.*?)(?=^## |\z)")
if (-not $m.Success) { throw "CHANGELOG.md hat keinen Abschnitt '## $tag'" }
[IO.File]::WriteAllText($notes, $m.Groups[1].Value.Trim() + "`n", (New-Object Text.UTF8Encoding $false))

Write-Host "Release $tag fertig: $out" -ForegroundColor Green
Get-ChildItem $out -File | Where-Object Name -ne 'NOTES.md' | ForEach-Object { "  {0,-40} {1,10:N0} Bytes" -f $_.Name, $_.Length }

if ($Publish) {
    $files = Get-ChildItem $out -File | Where-Object Name -ne 'NOTES.md' | ForEach-Object FullName
    git push origin $tag
    if ($LASTEXITCODE) { throw 'Tag-Push fehlgeschlagen' }
    gh release create $tag @files --title "$tag" --notes-file $notes --verify-tag
    if ($LASTEXITCODE) { throw 'gh release create fehlgeschlagen' }
    Write-Host "Auf GitHub veroeffentlicht: $tag" -ForegroundColor Green
}
