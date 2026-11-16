<#
  fake-fastboot.ps1 - Attrappe von fastboot fuer Tests von tools\flash.ps1.
  Teilt den Geraetezustand mit fake-edl.ps1 ($env:FAKE_EDL_DEV, mode.txt).
    $env:FAKE_FB_REPART_WIPE = 'modemst1,fsg'   -> "flash partition" loescht diese
                                                    (simuliert neue Partitionstabelle)
  Schreibt wie das echte fastboot Fortschritt auf stderr.
#>
$ErrorActionPreference = 'Stop'
$dev = $env:FAKE_EDL_DEV
$modeFile = Join-Path $dev 'mode.txt'
$mode = if (Test-Path $modeFile) { (Get-Content $modeFile -Raw).Trim() } else { 'normal' }
$a = @($args)
# Optionen wie -S 64M ueberspringen
while ($a.Count -and $a[0].StartsWith('-')) { $a = if ($a.Count -gt 2) { $a[2..($a.Count - 1)] } else { @() } }
Add-Content (Join-Path $dev 'fastboot-log.txt') ($args -join ' ')

if ($a[0] -eq 'devices') {
    if ($mode -eq 'fastboot') { Write-Output "FAKE1234`tfastboot" }
    exit 0
}
if ($mode -ne 'fastboot') { [Console]::Error.WriteLine('< waiting for any device >'); exit 1 }

function Zero([string]$part) {
    $p = Join-Path $dev "$part.bin"
    $len = if (Test-Path $p) { (Get-Item $p).Length } else { 4096 }
    [IO.File]::WriteAllBytes($p, (New-Object byte[] $len))
}

switch ($a[0]) {
    'flash' {
        [Console]::Error.WriteLine("Sending '$($a[1])' (OKAY)")
        if ($a[1] -eq 'partition') {
            if ($env:FAKE_FB_REPART_WIPE) { $env:FAKE_FB_REPART_WIPE -split ',' | ForEach-Object { Zero $_ } }
        } else {
            $p = Join-Path $dev "$($a[1]).bin"
            $len = if (Test-Path $p) { (Get-Item $p).Length } else { 0 }
            $img = [IO.File]::ReadAllBytes($a[2])
            $full = New-Object byte[] ([Math]::Max($len, $img.Length))
            [Array]::Copy($img, $full, $img.Length)
            [IO.File]::WriteAllBytes($p, $full)
        }
        [Console]::Error.WriteLine("Writing '$($a[1])' OKAY")
    }
    'erase' { Zero $a[1]; [Console]::Error.WriteLine("Erasing '$($a[1])' OKAY") }
    'reboot-bootloader' { [Console]::Error.WriteLine('Rebooting into bootloader OKAY') }
    'oem' {
        if ($a[1] -eq 'reboot-edl') { Set-Content $modeFile 'edl'; [Console]::Error.WriteLine('OKAY') }
        else { [Console]::Error.WriteLine('FAILED (remote: unknown command)'); exit 1 }
    }
    default { [Console]::Error.WriteLine("FAILED: unbekannt $($a -join ' ')"); exit 1 }
}
exit 0
