<#
  flash.ps1 - Backup und Flashen des UZ801 (Qualcomm EDL + fastboot).

  GRUNDSATZ: Es wird NIE geflasht ohne ein frisches, geprueftes Komplett-Backup.
  Das Backup ist fest eingebaut und nicht abschaltbar.

    .\tools\flash.ps1                                  nur Backup (Standard)
    .\tools\flash.ps1 -Mode Flash -ImageDir <dir>      Backup + Flashen + Modem-Partitionen zurueck

  Image-Formate (automatisch erkannt):
    QFIL      rawprogram*.xml (+ patch*.xml)  -> wird per EDL geschrieben
    fastboot  flash*.sh des Images oder eigene Liste (-PlanFile) -> per fastboot
              Das Skript des Images wird NICHT ausgefuehrt: nur die Zeilen
              "fastboot flash|erase|reboot-bootloader" werden uebernommen,
              jede unbekannte Zeile fuehrt zum Abbruch VOR dem Backup.

  Ablauf:
    1. Plan pruefen (Image-Dateien vorhanden, Syntax verstanden)
    2. Backup im EDL-Modus: GPT lesen, alle Partitionen (ohne userdata), Laengen
       gegen GPT, geschuetzte Partitionen doppelt lesen, SHA256SUMS, schreibgeschuetzt,
       optional Kopie nach -CopyTo mit Hash-Pruefung
    3. Plan anzeigen (geschuetzte Partitionen GESTRICHEN), Bestaetigung FLASHEN
    4. QFIL: edl qfil  |  fastboot: Geraet in fastboot bringen, Schritte ausfuehren
    5. zurueck in EDL, geschuetzte Partitionen aus dem Backup schreiben,
       zuruecklesen, Hash vergleichen (auch wenn das Image neu partitioniert)
    6. Neustart

  Voraussetzungen: Python + edl (https://github.com/bkerler/edl), WinUSB-Treiber
  fuer das EDL-Geraet (05C6:9008, z. B. Zadig), fuer fastboot-Images die
  Android platform-tools (fastboot) + Treiber.
#>
param(
    [ValidateSet('Backup', 'Flash')] [string]$Mode = 'Backup',
    [string]$ImageDir,
    [string]$PlanFile,               # fastboot: eigene Liste statt flash*.sh des Images
    [ValidateSet('Ask', 'BootErase', 'Manual')] [string]$EnterFastboot = 'Ask',
    [string]$BackupRoot = (Join-Path (Split-Path -Parent $PSScriptRoot) 'backup'),
    [string]$CopyTo,                   # optional: zweite Kopie des Backups (z. B. Netzlaufwerk/Cloud-Ordner)
    [string[]]$Protected = @('modemst1', 'modemst2', 'fsg', 'fsc', 'persist', 'modem', 'sec'),
    [switch]$IncludeUserdata,
    [string]$EdlCmd = 'edl',
    [string]$FastbootCmd = 'fastboot',
    [string]$Loader,                 # optional: Firehose-Loader (.mbn), sonst Auto-Erkennung von edl
    [int]$WaitSec = 180,             # Wartezeit auf Moduswechsel (EDL/fastboot)
    [switch]$NoDeviceCheck           # nur fuer Tests mit Attrappen (tools\test)
)
$ErrorActionPreference = 'Stop'
$scriptVersion = '4'

# ---------------------------------------------------------------- Hilfen
$script:log = $null
$script:hint = $null   # Wiederherstellungshinweis fuer Abbrueche mitten im Flash
function Say([string]$m, [string]$c = 'Gray') {
    Write-Host $m -ForegroundColor $c
    if ($script:log) { Add-Content -Path $script:log -Value ("{0:HH:mm:ss} {1}" -f (Get-Date), $m) -Encoding UTF8 }
}
function Fail([string]$m) {
    Say "ABBRUCH: $m" 'Red'
    if ($script:hint) { Say $script:hint 'Yellow' }
    throw $m
}

# Externes Programm aufrufen. stderr wird mitgelesen, ohne dass PowerShell 5.1
# jede stderr-Zeile (Fortschrittsausgabe von edl/fastboot!) als Fehler wertet.
function Invoke-Tool([string]$tool, [string]$label, [string[]]$a, [switch]$NoFail) {
    Say ("> $label " + ($a -join ' ')) 'DarkGray'
    $ErrorActionPreference = 'Continue'
    $out = & $tool @a 2>&1 | ForEach-Object { "$_" }
    $code = $LASTEXITCODE
    $ErrorActionPreference = 'Stop'
    if ($script:log) { Add-Content -Path $script:log -Value $out -Encoding UTF8 }
    if ($NoFail) { return $out }
    if ($code) { Fail "$label $($a[0]) endete mit Code $code" }
    if ($out -match '(?i)\b(error|failed|fail)\b' -and $out -notmatch '(?i)no error') {
        Fail "$label $($a[0]) meldet einen Fehler: $(( $out | Select-String -Pattern '(?i)error|fail' | Select-Object -First 2) -join ' | ')"
    }
    return $out
}
function Edl {
    $a = @($args)
    if ($Loader) { $a += "--loader=$Loader" }
    Invoke-Tool $EdlCmd 'edl' $a
}
function Fastboot { Invoke-Tool $FastbootCmd 'fastboot' @($args) }

# edl reset: beim Neustart trennt sich das Geraet oft, bevor edl die Antwort
# liest (USBError 32 "Pipe error"). Fehler nur, wenn es danach noch in EDL ist.
function Reset-Edl {
    $a = @('reset')
    if ($Loader) { $a += "--loader=$Loader" }
    $out = Invoke-Tool $EdlCmd 'edl' $a -NoFail
    $code = $LASTEXITCODE
    $err = $out | Select-String -Pattern '(?i)\b(error|failed|fail)\b' | Select-Object -First 1
    if (-not $code -and -not $err) { return }
    $what = if ($err) { $err.Line.Trim() } else { "Code $code" }
    if ($NoDeviceCheck) { Say "Hinweis: edl reset meldet '$what' - beim Neustart ueblich (nicht pruefbar ohne Geraet)" 'Yellow'; return }
    $end = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $end) {
        if (-not (Test-Edl)) { Say "Hinweis: edl reset meldet '$what', Geraet hat EDL aber verlassen - Neustart ok" 'Yellow'; return }
        Start-Sleep -Seconds 1
    }
    Fail "edl reset fehlgeschlagen ($what), Geraet ist noch im EDL-Modus"
}

# Seriennummer aus einem aelteren Backup desselben Geraets (gleiche fsg.bin).
# Noetig, wenn edl schon im Firehose-Modus lief: dann gibt es keine Sahara-Phase
# und keine "Serial:"-Zeile.
function Find-OlderSerial([string]$fsg) {
    if (-not (Test-Path $fsg)) { return $null }
    $h = Get-Sha $fsg
    foreach ($d in (Get-ChildItem $BackupRoot -Directory -Filter 'uz801-0x*' -ErrorAction SilentlyContinue | Sort-Object Name -Descending)) {
        $old = Join-Path $d.FullName 'fsg.bin'
        if ((Test-Path $old) -and (Get-Sha $old) -eq $h -and $d.Name -match '^uz801-(0x[0-9a-fA-F]+)-') { return $Matches[1] }
    }
    return $null
}

function Get-Sha([string]$p) { (Get-FileHash -Algorithm SHA256 -LiteralPath $p).Hash.ToLower() }

# SHA-256 ueber die ersten $len Bytes einer Datei
function Get-ShaPrefix([string]$p, [long]$len) {
    $sha = [Security.Cryptography.SHA256]::Create()
    $fs = [IO.File]::OpenRead($p)
    try {
        $buf = New-Object byte[] 1048576
        $left = $len
        while ($left -gt 0) {
            $n = $fs.Read($buf, 0, [int][Math]::Min([long]$buf.Length, [long]$left))   # long: Partitionen > 2 GB
            if ($n -le 0) { break }
            [void]$sha.TransformBlock($buf, 0, $n, $null, 0)
            $left -= $n
        }
        [void]$sha.TransformFinalBlock($buf, 0, 0)
        return (($sha.Hash | ForEach-Object { $_.ToString('x2') }) -join '')
    } finally { $fs.Dispose(); $sha.Dispose() }
}

# printgpt-Ausgabe -> @{ name = laenge }
function Parse-Gpt([string[]]$lines) {
    $parts = [ordered]@{}
    foreach ($l in $lines) {
        if ($l -match '^\s*([A-Za-z0-9_\-\.]+):\s+Offset\s+0x([0-9a-fA-F]+),\s+Length\s+0x([0-9a-fA-F]+)') {
            $parts[$Matches[1]] = [Convert]::ToInt64($Matches[3], 16)
        }
    }
    return $parts
}

# fastboot-Skript/Liste -> Schritte. Versteht nur einfache Zeilen; alles
# andere (Variablen, Schleifen, unbekannte Befehle) bricht ab.
function Read-FastbootPlan([string]$path) {
    $steps = @()
    $notes = @()
    $dirs = @((Split-Path -Parent $path), $ImageDir) | Select-Object -Unique
    $n = 0
    foreach ($raw in (Get-Content -LiteralPath $path)) {
        $n++
        $line = ($raw -replace '\s+#.*$', '').Trim()
        if (-not $line -or $line.StartsWith('#')) { continue }
        if ($line -match '^(set\s+-[a-z]+|echo\b|printf\b|sleep\s+\d+$|true$|exit\s+0$|cd\s+"?\$\(dirname\s+"?\$0"?\)"?$)') { continue }
        $tok = @([regex]::Matches($line, '"[^"]*"|''[^'']*''|\S+') | ForEach-Object { $_.Value.Trim('"', "'") })
        if ($tok[0] -eq 'sudo') { $tok = $tok[1..($tok.Count - 1)] }
        if ($tok[0] -eq 'adb') { $notes += "Zeile ${n}: $line (uebersprungen - Moduswechsel macht flash.ps1)"; continue }
        if ($tok[0] -notmatch '(^|[\\/])fastboot(\.exe)?$') { Fail "Zeile $n nicht unterstuetzt: $line" }
        if ($line -match '[\$`;|&<>]') { Fail "Zeile $n enthaelt Shell-Syntax (Variable/Umleitung): $line" }
        $i = 1
        $opts = @()
        while ($i -lt $tok.Count -and $tok[$i].StartsWith('-')) {
            switch -CaseSensitive ($tok[$i]) {
                '-S' { $opts += '-S', $tok[$i + 1]; $i += 2 }
                '-s' { $i += 2 }   # Seriennummer des Autors ignorieren
                default { Fail "Zeile ${n}: fastboot-Option $($tok[$i]) nicht unterstuetzt" }
            }
        }
        $cmd = $tok[$i]
        switch ($cmd) {
            'flash' {
                $part = $tok[$i + 1]; $file = $tok[$i + 2]
                if (-not $part -or -not $file) { Fail "Zeile ${n}: flash ohne Partition/Datei" }
                $full = $dirs | ForEach-Object { Join-Path $_ $file } | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
                if (-not $full) { Fail "Zeile ${n}: Image-Datei fehlt: $file" }
                $steps += [pscustomobject]@{ Op = 'flash'; Part = $part; File = (Resolve-Path -LiteralPath $full).Path; Opts = $opts; Line = $n }
            }
            'erase' {
                if (-not $tok[$i + 1]) { Fail "Zeile ${n}: erase ohne Partition" }
                $steps += [pscustomobject]@{ Op = 'erase'; Part = $tok[$i + 1]; File = $null; Opts = @(); Line = $n }
            }
            'reboot-bootloader' { $steps += [pscustomobject]@{ Op = 'reboot-bootloader'; Part = $null; File = $null; Opts = @(); Line = $n } }
            'reboot' {
                if ($tok[$i + 1] -eq 'bootloader') {
                    $steps += [pscustomobject]@{ Op = 'reboot-bootloader'; Part = $null; File = $null; Opts = @(); Line = $n }
                } else { $notes += "Zeile ${n}: reboot (uebersprungen - danach geht es zur Wiederherstellung in EDL)" }
            }
            { $_ -in 'devices', 'getvar', 'wait-for-device' } { continue }
            default { Fail "Zeile ${n}: fastboot $cmd nicht unterstuetzt" }
        }
    }
    if (-not ($steps | Where-Object Op -eq 'flash')) { Fail "Keine fastboot-flash-Zeilen in $path gefunden" }
    return [pscustomobject]@{ Steps = $steps; Notes = $notes }
}

function Test-Edl {
    if ($NoDeviceCheck) { return $true }
    return [bool](Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue | Where-Object { $_.InstanceId -match 'VID_05C6&PID_9008' })
}
function Wait-Edl([int]$sec) {
    $end = (Get-Date).AddSeconds($sec)
    while ((Get-Date) -lt $end) { if (Test-Edl) { return $true }; Start-Sleep -Seconds 2 }
    return $false
}
function Wait-Fastboot([int]$sec) {
    $end = (Get-Date).AddSeconds($sec)
    while ((Get-Date) -lt $end) {
        $out = Invoke-Tool $FastbootCmd 'fastboot' @('devices') -NoFail
        $m = $out | Select-String -Pattern '^(\S+)\s+fastboot' | Select-Object -First 1
        if ($m) { return $m.Matches[0].Groups[1].Value }
        Start-Sleep -Seconds 2
    }
    return $null
}

# Geschuetzte Partitionen aus dem Backup schreiben und verifizieren (EDL).
function Restore-Protected([string]$backup, [string[]]$parts, [string]$work) {
    $newGpt = Parse-Gpt (Edl printgpt)
    if ($newGpt.Count -lt 5) { Fail 'Neue Partitionstabelle nicht lesbar - Wiederherstellung manuell!' }
    foreach ($p in $parts) {
        if (-not $newGpt.Contains($p)) { Say "WARNUNG: ${p} gibt es im neuen Layout nicht - nicht zurueckgeschrieben" 'Yellow'; continue }
        $src = Join-Path $backup "$p.bin"
        if ((Get-Item $src).Length -gt $newGpt[$p]) { Fail "${p}: Backup groesser als neue Partition" }
        Edl w $p $src | Out-Null
        $chk = Join-Path $work "$p.check.bin"
        Edl r $p $chk | Out-Null
        # neue Partition kann groesser sein -> nur die Laenge des Backups vergleichen
        $len = (Get-Item $src).Length
        if ((Get-Item $chk).Length -lt $len -or (Get-ShaPrefix $chk $len) -ne (Get-Sha $src)) {
            Fail "${p}: Rueckschreiben nicht verifiziert!"
        }
        Say "  ${p}: zurueckgeschrieben und verifiziert" 'Green'
    }
}

# ---------------------------------------------------------------- 1. Vorpruefung + Plan
if (-not (Get-Command $EdlCmd -ErrorAction SilentlyContinue)) {
    Fail "edl nicht gefunden ($EdlCmd). Installation: git clone https://github.com/bkerler/edl; cd edl; pip install ."
}
$format = $null
if ($Mode -eq 'Flash') {
    if (-not $ImageDir -or -not (Test-Path $ImageDir)) { Fail '-ImageDir fehlt oder existiert nicht' }
    $ImageDir = (Resolve-Path $ImageDir).Path
    $raws = @(Get-ChildItem $ImageDir -Filter 'rawprogram*.xml')
    if ($PlanFile) {
        $format = 'fastboot'; $planSrc = (Resolve-Path $PlanFile).Path
    } elseif ($raws) {
        $format = 'qfil'
    } else {
        $sh = @(Get-ChildItem $ImageDir -Filter 'flash*.sh' -Recurse -Depth 2)
        if ($sh.Count -gt 1) { Fail "Mehrere flash*.sh gefunden ($($sh.Name -join ', ')) - mit -PlanFile eine waehlen" }
        if (-not $sh) { Fail "Weder rawprogram*.xml noch flash*.sh in $ImageDir - mit -PlanFile eine eigene Liste angeben" }
        $format = 'fastboot'; $planSrc = $sh[0].FullName
    }
    if ($format -eq 'fastboot') {
        if (-not (Get-Command $FastbootCmd -ErrorAction SilentlyContinue)) { Fail "fastboot nicht gefunden ($FastbootCmd) - Android platform-tools installieren" }
        $fbPlan = Read-FastbootPlan $planSrc
        Say "fastboot-Plan aus $planSrc verstanden ($(@($fbPlan.Steps).Count) Schritte)." 'Green'
    }
}
if (-not $NoDeviceCheck) {
    $dev = Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue | Where-Object { $_.InstanceId -match 'VID_05C6&PID_9008' }
    if (-not $dev) { Fail 'Kein Geraet im EDL-Modus (USB 05C6:9008) gefunden. Fuer das Backup muss der Stick in EDL sein.' }
    Say "EDL-Geraet: $($dev[0].FriendlyName) [$($dev[0].Status)]"
}

# ---------------------------------------------------------------- 2. Backup
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$tmpDir = Join-Path $BackupRoot "laufend-$stamp"
New-Item -ItemType Directory -Force $tmpDir | Out-Null
$script:log = Join-Path $tmpDir 'flash.log'
Say "== Backup nach $tmpDir" 'Cyan'

$gptOut = Edl printgpt
Set-Content -Path (Join-Path $tmpDir 'gpt.txt') -Value $gptOut -Encoding UTF8
$gpt = Parse-Gpt $gptOut
if ($gpt.Count -lt 5) { Fail "Partitionstabelle nicht erkannt ($($gpt.Count) Eintraege) - siehe gpt.txt" }
Say ("Partitionen: " + (($gpt.Keys | ForEach-Object { $_ }) -join ', '))
$serial = ($gptOut | Select-String -Pattern 'Serial:\s*(0x[0-9a-fA-F]+)' | Select-Object -First 1)
$serial = if ($serial) { $serial.Matches[0].Groups[1].Value } else { 'unbekannt' }

$missingProt = @($Protected | Where-Object { -not $gpt.Contains($_) })
if ($missingProt) { Say "Hinweis: nicht in der GPT: $($missingProt -join ', ')" 'Yellow' }

$skip = if ($IncludeUserdata) { @() } else { @('userdata') }
$rlArgs = @('rl', $tmpDir, '--genxml')
if ($skip) { $rlArgs += "--skip=$($skip -join ',')" }
Edl @rlArgs | Out-Null
Edl rs 0 34 (Join-Path $tmpDir 'gpt_main0.bin') | Out-Null

$bad = @()
foreach ($name in $gpt.Keys) {
    if ($skip -contains $name) { continue }
    $f = Join-Path $tmpDir "$name.bin"
    if (-not (Test-Path $f)) { $bad += "$name fehlt"; continue }
    $len = (Get-Item $f).Length
    if ($len -ne $gpt[$name]) { $bad += "$name Groesse $len statt $($gpt[$name])" }
}
if ($bad) { Fail ("Backup unvollstaendig: " + ($bad -join '; ')) }
Say "Alle $($gpt.Count - $skip.Count) Partitionen vollstaendig gelesen." 'Green'

$verify = Join-Path $tmpDir '_zweitlesung'
New-Item -ItemType Directory -Force $verify | Out-Null
foreach ($p in $Protected) {
    if (-not $gpt.Contains($p)) { continue }
    $f2 = Join-Path $verify "$p.bin"
    Edl r $p $f2 | Out-Null
    if ((Get-Sha $f2) -ne (Get-Sha (Join-Path $tmpDir "$p.bin"))) { Fail "${p}: zweite Lesung weicht ab - Verbindung instabil?" }
    Say "  ${p}: zweimal gelesen, identisch" 'Green'
}
Remove-Item -Recurse -Force $verify -Confirm:$false

$serialNote = ''
if ($serial -eq 'unbekannt') {
    $older = Find-OlderSerial (Join-Path $tmpDir 'fsg.bin')
    if ($older) {
        $serial = $older; $serialNote = ' (aus aelterem Backup mit gleicher fsg.bin)'
        Say "Seriennummer nicht gemeldet (Firehose lief schon) - aus aelterem Backup: $serial" 'Yellow'
    } else {
        Say 'Seriennummer nicht gemeldet (Firehose lief schon) und kein aelteres Backup dieses Geraets - Ordner heisst "unbekannt"' 'Yellow'
    }
}

$files = Get-ChildItem $tmpDir -File | Where-Object { $_.Name -ne 'flash.log' } | Sort-Object Name
$sums = $files | ForEach-Object { "{0}  {1}" -f (Get-Sha $_.FullName), $_.Name }
Set-Content -Path (Join-Path $tmpDir 'SHA256SUMS') -Value $sums -Encoding ASCII
$prot = @($Protected | Where-Object { $gpt.Contains($_) })
Set-Content -Path (Join-Path $tmpDir 'info.txt') -Encoding UTF8 -Value @(
    "UZ801-Backup $stamp", "Seriennummer (EDL): $serial$serialNote", "flash.ps1 Version $scriptVersion",
    "Partitionen: $($gpt.Count) (uebersprungen: $($skip -join ', '))",
    "Geschuetzt (doppelt gelesen): $($prot -join ', ')",
    'ENTHAELT IMEI/KALIBRIERUNG - nicht weitergeben, nie ins Repo.')
$final = Join-Path $BackupRoot "uz801-$serial-$stamp"
Rename-Item $tmpDir $final
$script:log = Join-Path $final 'flash.log'
Get-ChildItem $final -File | Where-Object { $_.Name -ne 'flash.log' } | ForEach-Object { $_.IsReadOnly = $true }
Say "Backup fertig: $final" 'Green'

if ($CopyTo -and (Test-Path (Split-Path -Parent $CopyTo))) {
    $dst = Join-Path $CopyTo (Split-Path -Leaf $final)
    New-Item -ItemType Directory -Force $dst | Out-Null
    Copy-Item (Join-Path $final '*') $dst
    foreach ($line in $sums) {
        $h, $n = $line -split '  ', 2
        if ((Get-Sha (Join-Path $dst $n)) -ne $h) { Fail "Kopie in -CopyTo fehlerhaft: $n" }
    }
    Say "Kopie geprueft: $dst" 'Green'
} else {
    Say "Hinweis: keine Zweitkopie (-CopyTo nicht angegeben oder Ziel fehlt) - Backup nur lokal in $final. Bitte selbst sichern!" 'Yellow'
}

if ($Mode -eq 'Backup') {
    Say 'Nur Backup angefordert - nichts geschrieben.' 'Cyan'
    return
}

# ---------------------------------------------------------------- 3. Plan anzeigen
$work = Join-Path $final '_flashplan'
New-Item -ItemType Directory -Force $work | Out-Null
$removed = @()
$planned = @()
if ($format -eq 'qfil') {
    $filtered = @()
    $imgLabels = @()      # Partitionen laut Image (fuer den Abgleich nach dem Schreiben)
    $imgHasGpt = $false
    foreach ($raw in $raws) {
        [xml]$x = Get-Content -LiteralPath $raw.FullName -Raw
        foreach ($node in @($x.data.program)) {
            $label = $node.label
            if ($label -in 'PrimaryGPT', 'BackupGPT') { $imgHasGpt = $true } elseif ($label) { $imgLabels += $label }
            if ($Protected -contains $label) {
                $removed += $label
                [void]$node.ParentNode.RemoveChild($node)
            } elseif ($node.filename) {
                if (-not (Test-Path (Join-Path $ImageDir $node.filename))) { Fail "Image-Datei fehlt: $($node.filename)" }
                $t = if ($label -in 'PrimaryGPT', 'BackupGPT') { '  <- NEUE PARTITIONSTABELLE' } else { '' }
                $planned += "{0,-14} <- {1}{2}" -f $label, $node.filename, $t
            }
        }
        $out = Join-Path $work $raw.Name
        $x.Save($out)
        $filtered += $out
    }
    # edl qfil verlangt immer eine patch-Datei; ohne patch*.xml im Image eine leere
    $patch = Get-ChildItem $ImageDir -Filter 'patch*.xml' | Select-Object -First 1
    if (-not $patch) {
        $pp = Join-Path $work 'patch0-leer.xml'
        Set-Content -Encoding ASCII $pp @('<?xml version="1.0" ?>', '<patches>', '</patches>')
        $patch = Get-Item $pp
    }
} else {
    $fbSteps = @()
    foreach ($s in $fbPlan.Steps) {
        if ($s.Part -and $Protected -contains $s.Part) { $removed += "$($s.Part) ($($s.Op), Zeile $($s.Line))"; continue }
        $fbSteps += $s
        $planned += switch ($s.Op) {
            'flash' {
                $t = if ($s.Part -eq 'partition') { '  <- NEUE PARTITIONSTABELLE' } else { '' }
                "{0,-14} <- {1}{2}" -f $s.Part, (Split-Path -Leaf $s.File), $t
            }
            'erase' { "{0,-14}    loeschen" -f $s.Part }
            default { "(fastboot reboot-bootloader)" }
        }
    }
    $fbPlan.Notes | ForEach-Object { Say "  Hinweis: $_" 'DarkGray' }
}
Say "== Flash-Plan ($format) aus $ImageDir" 'Cyan'
Say 'Wird geschrieben:'
$planned | ForEach-Object { Say "  $_" }
if ($removed) { Say ("Aus dem Image GESTRICHEN (geschuetzt): " + (($removed | Sort-Object -Unique) -join ', ')) 'Yellow' }
Say ("Danach aus dem Backup zurueck (per EDL, verifiziert): " + ($prot -join ', ')) 'Yellow'
Say "Backup: $final"
$answer = Read-Host 'Zum Flashen FLASHEN eintippen (alles andere bricht ab)'
if ($answer -cne 'FLASHEN') { Say 'Abgebrochen - nichts geschrieben.' 'Cyan'; return }
$script:hint = "Backup liegt in $final. Wiederherstellung einzelner Partitionen im EDL-Modus: edl w <partition> `"$final\<partition>.bin`""

# ---------------------------------------------------------------- 4. Flashen
if ($format -eq 'qfil') {
    foreach ($f in $filtered) {
        $qa = @('qfil', $f, $patch.FullName, $ImageDir)
        Edl @qa | Out-Null
    }
    if ($imgHasGpt) {
        # Image bringt eine eigene Partitionstabelle mit -> ist sie angekommen?
        $newNames = @((Parse-Gpt (Edl printgpt)).Keys | ForEach-Object { $_ })
        $diff = @(Compare-Object @($imgLabels | Sort-Object -Unique) @($newNames | Sort-Object -Unique))
        if ($diff) {
            Say ("WARNUNG: neue Partitionstabelle weicht vom Image ab: " + (($diff | ForEach-Object { "$($_.InputObject)$($_.SideIndicator)" }) -join ', ')) 'Yellow'
        } else {
            Say "Neue Partitionstabelle entspricht dem Image ($($newNames.Count) Partitionen)." 'Green'
        }
    }
} else {
    # in den fastboot-Modus wechseln
    $how = $EnterFastboot
    if ($how -eq 'Ask') {
        Say 'Wie kommt der Stick in den fastboot-Modus?'
        Say '  1 = automatisch: boot-Partition per EDL loeschen und neu starten (boot ist gesichert, das Image ersetzt sie)'
        Say '  2 = manuell (z. B. Original-Firmware + "adb reboot bootloader"), Skript wartet'
        $how = if ((Read-Host 'Auswahl 1/2') -eq '1') { 'BootErase' } else { 'Manual' }
    }
    if ($how -eq 'BootErase') {
        if (-not $gpt.Contains('boot')) { Fail 'keine boot-Partition - bitte manuell in fastboot wechseln (-EnterFastboot Manual)' }
        Edl e boot | Out-Null
        Reset-Edl
    } else {
        Reset-Edl
        Say "Stick jetzt in den fastboot-Modus bringen (warte bis $WaitSec s) ..." 'Yellow'
    }
    $fb = Wait-Fastboot $WaitSec
    if (-not $fb) { Fail "kein fastboot-Geraet nach $WaitSec s" }
    Say "fastboot-Geraet: $fb" 'Green'

    foreach ($s in $fbSteps) {
        switch ($s.Op) {
            'flash' {
                $fa = @($s.Opts) + @('flash', $s.Part, $s.File)   # Splatting nur ueber Variable (@fa)
                Fastboot @fa | Out-Null
            }
            'erase' { Fastboot erase $s.Part | Out-Null }
            'reboot-bootloader' {
                Fastboot reboot-bootloader | Out-Null
                if (-not (Wait-Fastboot $WaitSec)) { Fail 'fastboot-Geraet kommt nach reboot-bootloader nicht wieder' }
            }
        }
    }
    Say 'Image geschrieben. Wechsel in EDL fuer die Wiederherstellung ...' 'Cyan'
    # Nicht normal booten: das Modem wuerde sonst seine EFS (modemst*) veraendern,
    # bevor die gesicherten Daten zurueck sind.
    Invoke-Tool $FastbootCmd 'fastboot' @('oem', 'reboot-edl') -NoFail | Out-Null
    if (-not (Wait-Edl 30)) {
        Say "Automatischer Wechsel nach EDL ging nicht. Bitte manuell in EDL bringen (Taste/Testpunkt), NICHT normal booten. Warte bis $WaitSec s ..." 'Yellow'
        if (-not (Wait-Edl $WaitSec)) { Fail 'kein EDL-Geraet - geschuetzte Partitionen sind noch NICHT wiederhergestellt' }
    }
}

# ---------------------------------------------------------------- 5./6. Wiederherstellen + Neustart
Say 'Stelle geschuetzte Partitionen wieder her ...' 'Cyan'
Restore-Protected $final $prot $work
$script:hint = $null
Reset-Edl
Say "Fertig. Log: $script:log" 'Green'
