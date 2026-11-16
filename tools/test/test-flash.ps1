<#
  test-flash.ps1 - prueft tools\flash.ps1 gegen die edl-Attrappe (ohne Stick).
    powershell -ExecutionPolicy Bypass -File tools\test\test-flash.ps1
#>
$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$flash = Join-Path (Split-Path -Parent $here) 'flash.ps1'
$fake = Join-Path $here 'fake-edl.ps1'
# .cmd-Wrapper: Attrappen laufen als eigene Prozesse, stderr verhaelt sich wie bei echtem edl/fastboot
$fakeEdlCmd = Join-Path $here 'fake-edl.cmd'
$fakeFbCmd = Join-Path $here 'fake-fastboot.cmd'
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("flashtest-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory $tmp | Out-Null
$fails = 0
function Check([bool]$ok, [string]$what) {
    if ($ok) { Write-Host "  ok     $what" -ForegroundColor Green } else { Write-Host "  FEHLER $what" -ForegroundColor Red; $script:fails++ }
}
# --- kleines GPT-Rohabbild fuer raw2qfil-Tests -------------------------
$crcT = New-Object 'uint32[]' 256
for ($i = 0; $i -lt 256; $i++) {
    [uint32]$c = $i
    for ($k = 0; $k -lt 8; $k++) { if ($c -band 1) { $c = [uint32]([uint32]3988292384 -bxor ($c -shr 1)) } else { $c = [uint32]($c -shr 1) } }
    $crcT[$i] = $c
}
function Crc32([byte[]]$b) {
    [uint32]$c = [uint32]::MaxValue
    foreach ($x in $b) { $c = [uint32]($crcT[($c -bxor $x) -band 0xFF] -bxor ($c -shr 8)) }
    [uint32]($c -bxor [uint32]::MaxValue)
}
function Put([byte[]]$buf, [int]$off, [byte[]]$val) { [Array]::Copy($val, 0, $buf, $off, $val.Length) }
# $layout: geordnet Name -> Sektoren; Partitionen liegen lueckenlos ab LBA 34.
# Rueckgabe: Name -> Inhalt (Zufallsbytes)
function New-GptImage([string]$path, [long]$n, $layout) {
    $img = New-Object byte[] ($n * 512)
    $ents = New-Object byte[] (128 * 128)
    $rnd = New-Object Random 42
    $content = @{}
    $lba = 34; $i = 0
    foreach ($name in $layout.Keys) {
        $cnt = $layout[$name]
        $o = $i * 128
        $g = New-Object byte[] 16; $rnd.NextBytes($g); Put $ents $o $g          # Typ
        $g = New-Object byte[] 16; $rnd.NextBytes($g); Put $ents ($o + 16) $g   # eindeutige ID
        Put $ents ($o + 32) ([BitConverter]::GetBytes([uint64]$lba))
        Put $ents ($o + 40) ([BitConverter]::GetBytes([uint64]($lba + $cnt - 1)))
        Put $ents ($o + 56) ([Text.Encoding]::Unicode.GetBytes($name))
        $data = New-Object byte[] ($cnt * 512); $rnd.NextBytes($data)
        Put $img ($lba * 512) $data
        $content[$name] = $data
        $lba += $cnt; $i++
    }
    $entCrc = Crc32 $ents
    foreach ($h in @(@{ cur = 1; alt = $n - 1; ent = 2 }, @{ cur = $n - 1; alt = 1; ent = $n - 33 })) {
        $hd = New-Object byte[] 92
        Put $hd 0 ([Text.Encoding]::ASCII.GetBytes('EFI PART'))
        Put $hd 8 ([BitConverter]::GetBytes([uint32]0x00010000))
        Put $hd 12 ([BitConverter]::GetBytes([uint32]92))
        Put $hd 24 ([BitConverter]::GetBytes([uint64]$h.cur))
        Put $hd 32 ([BitConverter]::GetBytes([uint64]$h.alt))
        Put $hd 40 ([BitConverter]::GetBytes([uint64]34))
        Put $hd 48 ([BitConverter]::GetBytes([uint64]($n - 34)))
        Put $hd 72 ([BitConverter]::GetBytes([uint64]$h.ent))
        Put $hd 80 ([BitConverter]::GetBytes([uint32]128))
        Put $hd 84 ([BitConverter]::GetBytes([uint32]128))
        Put $hd 88 ([BitConverter]::GetBytes([uint32]$entCrc))
        Put $hd 16 ([BitConverter]::GetBytes([uint32](Crc32 $hd)))
        Put $img ($h.cur * 512) $hd
        Put $img ($h.ent * 512) $ents
    }
    [IO.File]::WriteAllBytes($path, $img)
    return $content
}

function Run([hashtable]$p) {
    $p.EdlCmd = $fakeEdlCmd; $p.FastbootCmd = $fakeFbCmd; $p.NoDeviceCheck = $true; $p.WaitSec = 10
    try { & $flash @p *> $null; return $true } catch { return $false }
}

try {
    Write-Host '== 1. Backup'
    $env:FAKE_EDL_DEV = Join-Path $tmp 'dev1'
    $root = Join-Path $tmp 'backup1'
    $copy = Join-Path $tmp 'zweitkopie\UZ801'
    New-Item -ItemType Directory (Split-Path $copy) | Out-Null
    Check (Run @{ BackupRoot = $root; CopyTo = $copy }) 'Backup laeuft durch'
    $b = Get-ChildItem $root -Directory | Select-Object -First 1
    Check ($b.Name -like 'uz801-0x1a2b3c4d-*') "Ordnername mit Seriennummer ($($b.Name))"
    Check (Test-Path (Join-Path $b.FullName 'modemst1.bin')) 'modemst1 gesichert'
    Check (-not (Test-Path (Join-Path $b.FullName 'userdata.bin'))) 'userdata standardmaessig uebersprungen'
    Check ((Get-Content (Join-Path $b.FullName 'SHA256SUMS')).Count -ge 12) 'SHA256SUMS vollstaendig'
    Check ((Get-Item (Join-Path $b.FullName 'modemst1.bin')).IsReadOnly) 'Dateien schreibgeschuetzt'
    Check (Test-Path (Join-Path $copy "$($b.Name)\modemst1.bin")) 'Zweitkopie (-CopyTo) vorhanden'

    Write-Host '== 2. wackelige Verbindung'
    $env:FAKE_EDL_FLAKY = 'modemst2'
    Check (-not (Run @{ BackupRoot = (Join-Path $tmp 'backup2'); CopyTo = '' })) 'Abbruch bei abweichender Zweitlesung'
    $env:FAKE_EDL_FLAKY = ''

    Write-Host '== 3. unvollstaendige Lesung'
    $env:FAKE_EDL_SHORT = 'boot'
    Check (-not (Run @{ BackupRoot = (Join-Path $tmp 'backup3'); CopyTo = '' })) 'Abbruch bei zu kurzer Partition'
    $env:FAKE_EDL_SHORT = ''

    Write-Host '== 4. Flashen: Image will modemst1 ueberschreiben'
    $env:FAKE_EDL_DEV = Join-Path $tmp 'dev4'
    $img = Join-Path $tmp 'image'
    New-Item -ItemType Directory $img | Out-Null
    [IO.File]::WriteAllBytes((Join-Path $img 'boot.img'), (New-Object byte[] 65536))
    [IO.File]::WriteAllBytes((Join-Path $img 'evil.bin'), (New-Object byte[] 24576))
    Set-Content (Join-Path $img 'rawprogram0.xml') @'
<?xml version="1.0" ?>
<data>
  <program filename="boot.img" label="boot" num_partition_sectors="128" start_sector="0" SECTOR_SIZE_IN_BYTES="512" physical_partition_number="0"/>
  <program filename="evil.bin" label="modemst1" num_partition_sectors="48" start_sector="0" SECTOR_SIZE_IN_BYTES="512" physical_partition_number="0"/>
</data>
'@
    Set-Content (Join-Path $img 'patch0.xml') '<patches/>'
    # Original-Inhalt der Modem-Partition merken (Attrappe erzeugt ihn deterministisch)
    & $fake r modemst1 (Join-Path $tmp 'orig-modemst1.bin') | Out-Null
    $orig = (Get-FileHash (Join-Path $tmp 'orig-modemst1.bin')).Hash

    # Abbruch ohne Bestaetigung
    $ok = Run @{ Mode = 'Flash'; ImageDir = $img; BackupRoot = (Join-Path $tmp 'backup4a'); CopyTo = '' }
    Check (-not (Test-Path (Join-Path $env:FAKE_EDL_DEV 'qfil-programm.xml'))) 'ohne FLASHEN wird nichts geschrieben'

    # mit Bestaetigung (Read-Host per Funktion ersetzt); Image loescht beim
    # Neupartitionieren modemst1/modemst2/fsg -> Wiederherstellung muss greifen
    function global:Read-Host { 'FLASHEN' }
    $env:FAKE_EDL_WIPE = 'modemst1,modemst2,fsg'
    Check (Run @{ Mode = 'Flash'; ImageDir = $img; BackupRoot = (Join-Path $tmp 'backup4b'); CopyTo = '' }) 'Flash laeuft durch'
    $env:FAKE_EDL_WIPE = ''
    Remove-Item function:\Read-Host
    Check ((Get-Content (Join-Path $env:FAKE_EDL_DEV 'fsg.bin') -Encoding Byte -TotalCount 64 | Measure-Object -Sum).Sum -gt 0) 'fsg nach Loeschung wiederhergestellt'
    $prog = Get-Content (Join-Path $env:FAKE_EDL_DEV 'qfil-programm.xml') -Raw
    Check ($prog -match 'label="boot"') 'boot im Programm'
    Check ($prog -notmatch 'modemst1') 'modemst1 aus dem Programm gestrichen'
    Check ((Get-FileHash (Join-Path $env:FAKE_EDL_DEV 'modemst1.bin')).Hash -eq $orig) 'modemst1 nach dem Flashen unveraendert/wiederhergestellt'
    Check (Test-Path (Get-ChildItem (Join-Path $tmp 'backup4b') -Directory | Select-Object -First 1 | ForEach-Object { Join-Path $_.FullName 'modemst1.bin' })) 'vor dem Flashen frisches Backup angelegt'

    Write-Host '== 5. fastboot-Image (flash.sh wie bei OpenStick), neue Partitionstabelle'
    $env:FAKE_EDL_DEV = Join-Path $tmp 'dev5'
    $img5 = Join-Path $tmp 'image5\base'
    New-Item -ItemType Directory $img5 | Out-Null
    foreach ($f in 'gpt_both0.bin', 'evil.bin') { [IO.File]::WriteAllBytes((Join-Path $img5 $f), (New-Object byte[] 1024)) }
    $aboot = New-Object byte[] 4096; (New-Object Random 7).NextBytes($aboot)
    [IO.File]::WriteAllBytes((Join-Path $img5 'emmc_appsboot.mbn'), $aboot)
    $rootfs = New-Object byte[] 8192; (New-Object Random 8).NextBytes($rootfs)
    [IO.File]::WriteAllBytes((Join-Path $img5 'rootfs.img'), $rootfs)
    $sh5 = @'
#!/bin/sh
set -e
cd "$(dirname "$0")"
# Kommentar
echo "Flashe OpenStick"
adb reboot bootloader
fastboot flash partition gpt_both0.bin
fastboot flash aboot emmc_appsboot.mbn
fastboot flash modemst1 evil.bin
fastboot erase fsg
fastboot reboot-bootloader
fastboot -S 64M flash rootfs rootfs.img
fastboot reboot
'@
    [IO.File]::WriteAllText((Join-Path $img5 'flash.sh'), $sh5)
    & $fake r modemst1 (Join-Path $tmp 'o5-modemst1.bin') | Out-Null
    & $fake r fsg (Join-Path $tmp 'o5-fsg.bin') | Out-Null
    $o5m = (Get-FileHash (Join-Path $tmp 'o5-modemst1.bin')).Hash
    $o5f = (Get-FileHash (Join-Path $tmp 'o5-fsg.bin')).Hash

    function global:Read-Host { 'FLASHEN' }
    $env:FAKE_FB_REPART_WIPE = 'modemst1,modemst2,fsg,persist'
    Check (Run @{ Mode = 'Flash'; ImageDir = (Split-Path $img5); EnterFastboot = 'BootErase'; BackupRoot = (Join-Path $tmp 'backup5'); CopyTo = '' }) 'fastboot-Flash laeuft durch'
    $env:FAKE_FB_REPART_WIPE = ''
    Remove-Item function:\Read-Host
    $fblog = Get-Content (Join-Path $env:FAKE_EDL_DEV 'fastboot-log.txt') -Raw
    Check ($fblog -match 'flash partition') 'neue Partitionstabelle geflasht'
    Check ($fblog -match '-S 64M flash rootfs') 'Option -S an fastboot durchgereicht'
    Check ($fblog -notmatch 'flash modemst1' -and $fblog -notmatch 'erase fsg') 'modemst1-Flash und fsg-Erase gestrichen'
    Check ($fblog -match 'oem reboot-edl') 'nach dem Flashen in EDL statt normal gebootet'
    $ab = [IO.File]::ReadAllBytes((Join-Path $env:FAKE_EDL_DEV 'aboot.bin'))
    Check ([Convert]::ToBase64String($ab[0..4095]) -eq [Convert]::ToBase64String($aboot)) 'aboot aus dem Image geschrieben'
    Check ((Get-FileHash (Join-Path $env:FAKE_EDL_DEV 'modemst1.bin')).Hash -eq $o5m) 'modemst1 nach Neupartitionierung wiederhergestellt'
    Check ((Get-FileHash (Join-Path $env:FAKE_EDL_DEV 'fsg.bin')).Hash -eq $o5f) 'fsg nach Neupartitionierung wiederhergestellt'

    Write-Host '== 6. fastboot-Skript mit Shell-Logik wird abgelehnt (vor dem Backup)'
    $img6 = Join-Path $tmp 'image6'
    New-Item -ItemType Directory $img6 | Out-Null
    [IO.File]::WriteAllText((Join-Path $img6 'flash.sh'), "#!/bin/sh`nfor f in *.img; do fastboot flash `${f%.img} `$f; done`n")
    $root6 = Join-Path $tmp 'backup6'
    Check (-not (Run @{ Mode = 'Flash'; ImageDir = $img6; BackupRoot = $root6; CopyTo = '' })) 'Abbruch bei Schleife im Skript'
    Check (-not (Test-Path $root6)) 'kein Backup begonnen (Abbruch schon bei der Planpruefung)'

    Write-Host '== 7. fehlende Image-Datei'
    [IO.File]::WriteAllText((Join-Path $img6 'flash.sh'), "fastboot flash boot fehlt.img`n")
    Check (-not (Run @{ Mode = 'Flash'; ImageDir = $img6; BackupRoot = $root6; CopyTo = '' })) 'Abbruch bei fehlender Datei'

    Write-Host '== 8. Rohabbild (full_emmc.bin) -> raw2qfil -> flash.ps1'
    $raw2qfil = Join-Path (Split-Path -Parent $here) 'raw2qfil.ps1'
    $layout8 = [ordered]@{ modemst1 = 48; boot = 128; rootfs = 256 }   # Sektoren
    $n8 = 34 + ($layout8.Values | Measure-Object -Sum).Sum + 33
    $raw8 = Join-Path $tmp 'full_emmc.bin'
    $content8 = New-GptImage $raw8 $n8 $layout8
    function R2Q([hashtable]$p) { try { & $raw2qfil @p *> $null; return $true } catch { return $false } }

    $q8 = Join-Path $tmp 'qfil8'
    Check (R2Q @{ Image = $raw8; OutDir = $q8; DiskSectors = $n8 }) 'raw2qfil zerlegt ein gueltiges Abbild'
    $rp = Get-Content (Join-Path $q8 'rawprogram0.xml') -Raw
    Check ($rp -match 'label="PrimaryGPT"[^>]*start_sector="0"') 'primaere GPT ab Sektor 0 im Programm'
    Check ($rp -match "label=`"BackupGPT`"[^>]*start_sector=`"$($n8 - 33)`"") 'Sicherungs-GPT in den letzten 33 Sektoren'
    Check (([regex]::Matches($rp, '<program ')).Count -eq 5) '5 Programmeintraege (2x GPT + 3 Partitionen)'
    Check ([Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $q8 'rootfs.bin'))) -eq [Convert]::ToBase64String($content8['rootfs'])) 'rootfs.bin byte-genau aus dem Abbild'
    Check ((Get-Item (Join-Path $q8 'gpt_main0.bin')).Length -eq 34 * 512) 'gpt_main0.bin = 34 Sektoren'

    Check (-not (R2Q @{ Image = $raw8; OutDir = (Join-Path $tmp 'qfil8b'); DiskSectors = ($n8 + 1) })) 'Abbruch, wenn die eMMC-Groesse nicht passt'
    Check (-not (R2Q @{ Image = $raw8; OutDir = $q8; DiskSectors = $n8 })) 'Abbruch bei nicht leerem Zielordner'
    $bk8 = Join-Path $tmp 'bk8'; New-Item -ItemType Directory $bk8 | Out-Null
    $fsb = [IO.File]::ReadAllBytes($raw8); [IO.File]::WriteAllBytes((Join-Path $bk8 'gpt_main0.bin'), $fsb[0..(34 * 512 - 1)])
    Check (R2Q @{ Image = $raw8; OutDir = (Join-Path $tmp 'qfil8c'); BackupDir = $bk8 }) 'eMMC-Groesse aus gpt_main0.bin des Backups'
    $bad8 = Join-Path $tmp 'bad_emmc.bin'
    $fsb[2 * 512 + 60] = $fsb[2 * 512 + 60] -bxor 0xFF    # Partitionsname der primaeren GPT verfaelscht
    [IO.File]::WriteAllBytes($bad8, $fsb)
    Check (-not (R2Q @{ Image = $bad8; OutDir = (Join-Path $tmp 'qfil8d'); DiskSectors = $n8 })) 'Abbruch bei falscher CRC der Partitionseintraege'

    # Flashen des erzeugten Pakets (ohne patch*.xml aus dem Image -> leere patch-Datei von flash.ps1)
    Remove-Item (Join-Path $q8 'patch0.xml')
    $env:FAKE_EDL_DEV = Join-Path $tmp 'dev8'
    & $fake r modemst1 (Join-Path $tmp 'o8-modemst1.bin') | Out-Null
    $o8 = (Get-FileHash (Join-Path $tmp 'o8-modemst1.bin')).Hash
    function global:Read-Host { 'FLASHEN' }
    Check (Run @{ Mode = 'Flash'; ImageDir = $q8; BackupRoot = (Join-Path $tmp 'backup8'); CopyTo = '' }) 'Flash des QFIL-Pakets laeuft durch (ohne patch*.xml)'
    Remove-Item function:\Read-Host
    $prog8 = Get-Content (Join-Path $env:FAKE_EDL_DEV 'qfil-programm.xml') -Raw
    Check ($prog8 -notmatch 'label="modemst1"') 'modemst1 aus dem Programm gestrichen'
    Check ($prog8 -match 'label="PrimaryGPT"' -and $prog8 -match 'label="BackupGPT"') 'beide GPT-Kopien im Programm'
    Check ([Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $env:FAKE_EDL_DEV 'rootfs.bin'))) -eq [Convert]::ToBase64String($content8['rootfs'])) 'rootfs geschrieben'
    Check ((Get-FileHash (Join-Path $env:FAKE_EDL_DEV 'modemst1.bin')).Hash -eq $o8) 'modemst1 unveraendert'

    Write-Host '== 9. keine Seriennummer (Firehose lief schon)'
    $env:FAKE_EDL_DEV = Join-Path $tmp 'dev1'          # gleiches Geraet wie Test 1
    $env:FAKE_EDL_NOSERIAL = '1'
    Check (Run @{ BackupRoot = $root; CopyTo = '' }) 'Backup ohne Serial-Zeile laeuft durch'
    $b9 = @(Get-ChildItem $root -Directory | Sort-Object Name)
    Check ($b9.Count -eq 2 -and @($b9 | Where-Object Name -like 'uz801-0x1a2b3c4d-*').Count -eq 2) 'Seriennummer aus aelterem Backup (gleiche fsg.bin) uebernommen'
    $neu9 = $b9 | Sort-Object LastWriteTime | Select-Object -Last 1
    Check ((Get-Content (Join-Path $neu9.FullName 'info.txt') -Raw) -match 'aus aelterem Backup') 'Herkunft der Seriennummer in info.txt'
    $env:FAKE_EDL_DEV = Join-Path $tmp 'dev9'          # anderes Geraet, kein aelteres Backup
    $root9 = Join-Path $tmp 'backup9'
    Check (Run @{ BackupRoot = $root9; CopyTo = '' }) 'Backup eines unbekannten Geraets laeuft durch'
    Check ((Get-ChildItem $root9 -Directory | Select-Object -First 1).Name -like 'uz801-unbekannt-*') 'ohne aelteres Backup: Ordner "unbekannt"'
    $env:FAKE_EDL_NOSERIAL = ''

    Write-Host '== 10. edl reset meldet USB-Fehler (Geraet trennt sich beim Neustart)'
    $env:FAKE_EDL_DEV = Join-Path $tmp 'dev10'
    $env:FAKE_EDL_RESET_PIPE = '1'
    function global:Read-Host { 'FLASHEN' }
    Check (Run @{ Mode = 'Flash'; ImageDir = $img; BackupRoot = (Join-Path $tmp 'backup10'); CopyTo = '' }) 'Flash endet trotz Pipe-Error beim Reset erfolgreich'
    Remove-Item function:\Read-Host
    $env:FAKE_EDL_RESET_PIPE = ''
    Check (Test-Path (Join-Path $env:FAKE_EDL_DEV 'mode.txt')) 'Neustart wurde trotzdem ausgeloest'
} finally {
    Get-ChildItem $tmp -Recurse -File | ForEach-Object { $_.IsReadOnly = $false }
    Remove-Item -Recurse -Force $tmp -Confirm:$false
    Remove-Item Env:FAKE_EDL_DEV, Env:FAKE_EDL_FLAKY, Env:FAKE_EDL_SHORT, Env:FAKE_EDL_WIPE, Env:FAKE_FB_REPART_WIPE, Env:FAKE_EDL_NOSERIAL, Env:FAKE_EDL_RESET_PIPE -ErrorAction SilentlyContinue
}
if ($fails) { Write-Host "$fails Pruefung(en) fehlgeschlagen" -ForegroundColor Red; exit 1 }
Write-Host 'Alle Pruefungen bestanden' -ForegroundColor Green
