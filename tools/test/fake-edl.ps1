<#
  fake-edl.ps1 - Attrappe von bkerler/edl fuer Tests von tools\flash.ps1 ohne Stick.
  Geraetezustand liegt in $env:FAKE_EDL_DEV (ein Ordner, je Partition eine Datei).
    $env:FAKE_EDL_FLAKY = 'modemst1'   -> jede Lesung dieser Partition liefert anderen Inhalt
    $env:FAKE_EDL_SHORT = 'boot'       -> rl schreibt diese Partition zu kurz
  Schreibt Fortschritt zusaetzlich auf stderr - wie das echte edl (Python-Logging).
#>
[Console]::Error.WriteLine('[LIB]: progress 100% (stderr wie beim echten edl)')
$ErrorActionPreference = 'Stop'
$dev = $env:FAKE_EDL_DEV
if (-not $dev) { Write-Output 'FAKE_EDL_DEV fehlt'; exit 2 }
New-Item -ItemType Directory -Force $dev | Out-Null

$layout = [ordered]@{
    sbl1 = 16384; aboot = 32768; boot = 65536; modem = 131072; modemst1 = 24576; modemst2 = 24576
    fsg = 24576; fsc = 1024; persist = 32768; sec = 16384; rootfs = 262144; userdata = 524288
}
$a = @($args | Where-Object { $_ -notlike '--loader=*' })

function Content([string]$name) {
    $p = Join-Path $dev "$name.bin"
    if (-not (Test-Path $p)) {
        $rnd = New-Object Random ($name.GetHashCode())
        $b = New-Object byte[] $layout[$name]
        $rnd.NextBytes($b)
        [IO.File]::WriteAllBytes($p, $b)
    }
    $b = [IO.File]::ReadAllBytes($p)
    if ($env:FAKE_EDL_FLAKY -eq $name) { $b[0] = [byte](Get-Random -Maximum 256) }
    return , $b
}

Write-Output 'Qualcomm Sahara / Firehose Client (Attrappe)'
# FAKE_EDL_NOSERIAL: wie echtes edl, wenn Firehose schon laeuft (keine Sahara-Phase)
if (-not $env:FAKE_EDL_NOSERIAL) { Write-Output 'Serial: 0x1a2b3c4d' }
switch ($a[0]) {
    'printgpt' {
        $off = 0
        foreach ($k in $layout.Keys) {
            Write-Output ("{0}: Offset 0x{1:x16}, Length 0x{2:x16}, Flags 0x00000000, UUID x, Type 0x0" -f $k, $off, $layout[$k])
            $off += $layout[$k]
        }
    }
    'rl' {
        $dir = $a[1]
        $skip = @(($a | Where-Object { $_ -like '--skip=*' }) -replace '^--skip=', '' -split ',')
        foreach ($k in $layout.Keys) {
            if ($skip -contains $k) { continue }
            $b = Content $k
            if ($env:FAKE_EDL_SHORT -eq $k) { $b = $b[0..99] }
            [IO.File]::WriteAllBytes((Join-Path $dir "$k.bin"), [byte[]]$b)
        }
        Set-Content (Join-Path $dir 'rawprogram0.xml') '<data/>'
    }
    'rs' { [IO.File]::WriteAllBytes($a[3], (New-Object byte[] (512 * [int]$a[2]))) }
    'r' { [IO.File]::WriteAllBytes($a[2], [byte[]](Content $a[1])) }
    'w' {
        $b = [IO.File]::ReadAllBytes($a[2])
        $full = New-Object byte[] $layout[$a[1]]
        [Array]::Copy($b, $full, $b.Length)
        [IO.File]::WriteAllBytes((Join-Path $dev "$($a[1]).bin"), $full)
    }
    'qfil' {
        # wie das echte edl: qfil <rawprogram> <patch> <imagedir> - alle drei Pflicht
        if ($a.Count -ne 4 -or -not (Test-Path $a[2]) -or -not (Test-Path $a[3] -PathType Container)) {
            Write-Output "Usage error: qfil <rawprogram> <patch> <imagedir> ($($a -join ' '))"; exit 1
        }
        [xml]$x = Get-Content $a[1] -Raw
        foreach ($n in @($x.data.program)) {
            # Image ueberschreibt alles, was im (gefilterten) Programm steht
            $src = Join-Path $a[-1] $n.filename
            [IO.File]::WriteAllBytes((Join-Path $dev "$($n.label).bin"), [IO.File]::ReadAllBytes($src))
        }
        Copy-Item $a[1] (Join-Path $dev 'qfil-programm.xml')
        if ($env:FAKE_EDL_WIPE) {
            # simuliert ein Image, das neu partitioniert und dabei die Modem-Daten loescht
            foreach ($k in $env:FAKE_EDL_WIPE -split ',') {
                [IO.File]::WriteAllBytes((Join-Path $dev "$k.bin"), (New-Object byte[] $layout[$k]))
            }
        }
    }
    'e' {
        $p = Join-Path $dev "$($a[1]).bin"
        [IO.File]::WriteAllBytes($p, (New-Object byte[] $layout[$a[1]]))
    }
    'reset' {
        # leere boot-Partition -> Bootloader faellt in fastboot (wie der echte aboot)
        $boot = Join-Path $dev 'boot.bin'
        $mode = 'normal'
        if ((Test-Path $boot) -and -not ([IO.File]::ReadAllBytes($boot) | Where-Object { $_ -ne 0 } | Select-Object -First 1)) { $mode = 'fastboot' }
        Set-Content (Join-Path $dev 'mode.txt') $mode
        # FAKE_EDL_RESET_PIPE: Geraet trennt sich, bevor edl die Antwort liest (wie am echten UZ801)
        if ($env:FAKE_EDL_RESET_PIPE) { Write-Output "DeviceClass - USBError(32, 'Pipe error')"; exit 1 }
        Write-Output "Reset ok -> $mode"
    }
    default { Write-Output "unbekannt: $($a -join ' ')"; exit 1 }
}
exit 0
