<#
  raw2qfil.ps1 - zerlegt ein 1:1-eMMC-Abbild (z. B. full_emmc.bin des
  openstick-installer) in ein QFIL-Paket, das tools\flash.ps1 schreiben kann.

    .\tools\raw2qfil.ps1 -Image <full_emmc.bin> -OutDir <ordner> -BackupDir <backup\uz801-...>

  Warum: flash.ps1 streicht beim QFIL-Weg die geschuetzten Partitionen
  (IMEI/Kalibrierung) aus dem Programm und schreibt sie danach aus dem Backup
  zurueck. Ein Rohabbild per "edl wf" wuerde sie dagegen blind ueberschreiben.

  Prueft vor dem Zerlegen:
    - primaere GPT: Signatur, Header-CRC, CRC der Partitionseintraege
    - Sicherungs-GPT im letzten Sektor: dieselben Pruefungen, gleiche Eintraege
    - Abbild hat exakt so viele Sektoren wie die eMMC laut Backup
      (gpt_main0.bin des Backups, Feld "alternate LBA" + 1) - oder -DiskSectors
    - keine Partition ueberlappt eine andere oder die GPT-Bereiche

  Ergebnis in -OutDir:
    gpt_main0.bin (LBA 0-33), gpt_backup0.bin (letzte 33 Sektoren),
    <label>.bin je Partition, rawprogram0.xml, patch0.xml (leer),
    SHA256SUMS, quelle.txt
#>
param(
    [Parameter(Mandatory)] [string]$Image,
    [Parameter(Mandatory)] [string]$OutDir,
    [string]$BackupDir,
    [long]$DiskSectors = 0
)
$ErrorActionPreference = 'Stop'
$SS = 512

function Fail([string]$m) { Write-Host "ABBRUCH: $m" -ForegroundColor Red; throw $m }

# --- CRC32 (IEEE, wie in der GPT) ---------------------------------------
$script:crcTable = New-Object 'uint32[]' 256
for ($i = 0; $i -lt 256; $i++) {
    [uint32]$c = $i
    for ($k = 0; $k -lt 8; $k++) {
        if ($c -band 1) { $c = [uint32]([uint32]3988292384 -bxor ($c -shr 1)) } else { $c = [uint32]($c -shr 1) }
    }
    $script:crcTable[$i] = $c
}
function Get-Crc32([byte[]]$b, [int]$off = 0, [int]$len = -1) {
    if ($len -lt 0) { $len = $b.Length - $off }
    [uint32]$c = [uint32]::MaxValue
    $t = $script:crcTable
    for ($i = $off; $i -lt $off + $len; $i++) { $c = [uint32]($t[($c -bxor $b[$i]) -band 0xFF] -bxor ($c -shr 8)) }
    return [uint32]($c -bxor [uint32]::MaxValue)
}

function Read-At([IO.FileStream]$fs, [long]$pos, [int]$len) {
    $buf = New-Object byte[] $len
    [void]$fs.Seek($pos, 'Begin')
    $n = 0
    while ($n -lt $len) { $r = $fs.Read($buf, $n, $len - $n); if ($r -le 0) { Fail "Abbild zu kurz bei Offset $pos" }; $n += $r }
    return , $buf
}

# GPT-Header an LBA $lba lesen und pruefen -> Objekt mit Eintraegen
function Read-Gpt([IO.FileStream]$fs, [long]$lba, [string]$what) {
    $h = Read-At $fs ($lba * $SS) $SS
    if ([Text.Encoding]::ASCII.GetString($h, 0, 8) -ne 'EFI PART') { Fail "${what}: keine GPT-Signatur in LBA $lba" }
    $hsize = [BitConverter]::ToUInt32($h, 12)
    if ($hsize -lt 92 -or $hsize -gt $SS) { Fail "${what}: Headergroesse $hsize unplausibel" }
    $crcStored = [BitConverter]::ToUInt32($h, 16)
    $hc = New-Object byte[] $hsize; [Array]::Copy($h, $hc, $hsize)
    $hc[16] = 0; $hc[17] = 0; $hc[18] = 0; $hc[19] = 0
    if ((Get-Crc32 $hc) -ne $crcStored) { Fail "${what}: Header-CRC falsch" }
    $g = [pscustomobject]@{
        Current    = [BitConverter]::ToUInt64($h, 24)
        Alternate  = [BitConverter]::ToUInt64($h, 32)
        FirstUsable = [BitConverter]::ToUInt64($h, 40)
        LastUsable = [BitConverter]::ToUInt64($h, 48)
        EntriesLba = [BitConverter]::ToUInt64($h, 72)
        Count      = [BitConverter]::ToUInt32($h, 80)
        EntrySize  = [BitConverter]::ToUInt32($h, 84)
        Parts      = @()
        Raw        = $null
    }
    if ($g.Current -ne $lba) { Fail "${what}: Header verweist auf LBA $($g.Current) statt $lba" }
    if ($g.EntrySize -lt 128 -or $g.Count -gt 1024) { Fail "${what}: Eintragsformat unplausibel" }
    $ents = Read-At $fs ([long]$g.EntriesLba * $SS) ([int]($g.Count * $g.EntrySize))
    if ((Get-Crc32 $ents) -ne [BitConverter]::ToUInt32($h, 88)) { Fail "${what}: CRC der Partitionseintraege falsch" }
    $g.Raw = $ents
    for ($i = 0; $i -lt $g.Count; $i++) {
        $o = $i * $g.EntrySize
        $empty = $true
        for ($j = 0; $j -lt 16; $j++) { if ($ents[$o + $j]) { $empty = $false; break } }
        if ($empty) { continue }
        $name = [Text.Encoding]::Unicode.GetString($ents, $o + 56, 72).TrimEnd([char]0)
        $g.Parts += [pscustomobject]@{
            Name  = $name
            First = [BitConverter]::ToUInt64($ents, $o + 32)
            Last  = [BitConverter]::ToUInt64($ents, $o + 40)
        }
    }
    return $g
}

# ------------------------------------------------------------------ Pruefen
if (-not (Test-Path -LiteralPath $Image)) { Fail "Abbild fehlt: $Image" }
$Image = (Resolve-Path -LiteralPath $Image).Path
$len = (Get-Item -LiteralPath $Image).Length
if ($len % $SS) { Fail "Abbildgroesse $len ist kein Vielfaches von $SS" }
$sectors = $len / $SS

if ($BackupDir) {
    $bg = Join-Path $BackupDir 'gpt_main0.bin'
    if (-not (Test-Path -LiteralPath $bg)) { Fail "gpt_main0.bin fehlt im Backup $BackupDir" }
    $b = [IO.File]::ReadAllBytes($bg)
    if ([Text.Encoding]::ASCII.GetString($b, $SS, 8) -ne 'EFI PART') { Fail 'gpt_main0.bin des Backups hat keine GPT-Signatur' }
    $DiskSectors = [long]([BitConverter]::ToUInt64($b, $SS + 32)) + 1
}
if ($DiskSectors -le 0) { Fail 'Groesse der eMMC unbekannt: -BackupDir oder -DiskSectors angeben' }
if ($sectors -ne $DiskSectors) { Fail "Abbild hat $sectors Sektoren, die eMMC $DiskSectors - passt nicht zu diesem Stick" }

$fs = [IO.File]::OpenRead($Image)
try {
    $p = Read-Gpt $fs 1 'primaere GPT'
    if ($p.Alternate -ne $sectors - 1) { Fail "primaere GPT erwartet die Sicherung in LBA $($p.Alternate), Abbildende ist $($sectors - 1)" }
    $s = Read-Gpt $fs ($sectors - 1) 'Sicherungs-GPT'
    if ([Convert]::ToBase64String($p.Raw) -ne [Convert]::ToBase64String($s.Raw)) { Fail 'primaere und Sicherungs-GPT haben verschiedene Eintraege' }
    $mainEnd = $p.EntriesLba + [Math]::Ceiling($p.Count * $p.EntrySize / $SS) - 1   # i. d. R. 33
    if ($s.EntriesLba -le $p.LastUsable) { Fail 'Sicherungs-Eintraege liegen im nutzbaren Bereich' }
    $backupFirst = [long]$s.EntriesLba
    $backupCount = $sectors - $backupFirst

    $parts = @($p.Parts | Sort-Object First)
    if ($parts.Count -lt 3) { Fail "nur $($parts.Count) Partitionen - kein sinnvolles Abbild" }
    $names = @{}
    $prevEnd = [long]$mainEnd
    foreach ($x in $parts) {
        if (-not $x.Name -or $x.Name -notmatch '^[A-Za-z0-9_\-\.]+$') { Fail "Partitionsname unbrauchbar: '$($x.Name)'" }
        if ($names.ContainsKey($x.Name)) { Fail "Partitionsname doppelt: $($x.Name)" }
        $names[$x.Name] = 1
        if ($x.Last -lt $x.First) { Fail "$($x.Name): Ende vor Anfang" }
        if ($x.First -lt $p.FirstUsable -or $x.Last -gt $p.LastUsable) { Fail "$($x.Name): ausserhalb des nutzbaren Bereichs" }
        if ($x.First -le $prevEnd) { Fail "$($x.Name): ueberlappt die vorige Partition bzw. die GPT" }
        $prevEnd = [long]$x.Last
    }

    # ------------------------------------------------------------------ Schreiben
    if (Test-Path -LiteralPath $OutDir) {
        if (Get-ChildItem -LiteralPath $OutDir -Force | Select-Object -First 1) { Fail "Zielordner ist nicht leer: $OutDir" }
    } else { New-Item -ItemType Directory $OutDir | Out-Null }
    $OutDir = (Resolve-Path -LiteralPath $OutDir).Path

    function Copy-Range([long]$first, [long]$count, [string]$file) {
        $out = [IO.File]::Create((Join-Path $OutDir $file))
        try {
            [void]$fs.Seek($first * $SS, 'Begin')
            $buf = New-Object byte[] (4MB)
            $left = $count * $SS
            while ($left -gt 0) {
                $n = $fs.Read($buf, 0, [int][Math]::Min([long]$buf.Length, [long]$left))
                if ($n -le 0) { Fail "Lesefehler bei $file" }
                $out.Write($buf, 0, $n); $left -= $n
            }
        } finally { $out.Dispose() }
    }

    function New-Prog([string]$label, [string]$file, [long]$first, [long]$count) {
        '  <program SECTOR_SIZE_IN_BYTES="512" file_sector_offset="0" filename="{0}" label="{1}" num_partition_sectors="{2}" partofsingleimage="false" physical_partition_number="0" readbackverify="false" size_in_KB="{3}" sparse="false" start_byte_hex="0x{4:x}" start_sector="{5}"/>' -f
        $file, $label, $count, ($count * $SS / 1024).ToString('0.0', [Globalization.CultureInfo]::InvariantCulture), ($first * $SS), $first
    }
    $prog = @()
    Write-Host "GPT ok: $($parts.Count) Partitionen, $sectors Sektoren" -ForegroundColor Green
    Copy-Range 0 ($mainEnd + 1) 'gpt_main0.bin'
    $prog += New-Prog 'PrimaryGPT' 'gpt_main0.bin' 0 ($mainEnd + 1)
    foreach ($x in $parts) {
        $cnt = [long]($x.Last - $x.First + 1)
        Write-Host ("  {0,-12} LBA {1,9}  {2,8:N1} MB" -f $x.Name, $x.First, ($cnt * $SS / 1MB))
        Copy-Range ([long]$x.First) $cnt "$($x.Name).bin"
        $prog += New-Prog $x.Name "$($x.Name).bin" ([long]$x.First) $cnt
    }
    Copy-Range $backupFirst $backupCount 'gpt_backup0.bin'
    $prog += New-Prog 'BackupGPT' 'gpt_backup0.bin' $backupFirst $backupCount
} finally { $fs.Dispose() }

Set-Content -Encoding ASCII (Join-Path $OutDir 'rawprogram0.xml') (@('<?xml version="1.0" ?>', '<data>') + $prog + '</data>')
# leer: alle Sektoren stehen absolut im Programm, die eMMC-Groesse ist geprueft
Set-Content -Encoding ASCII (Join-Path $OutDir 'patch0.xml') @('<?xml version="1.0" ?>', '<patches>', '</patches>')
$srcHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $Image).Hash.ToLower()
Set-Content -Encoding UTF8 (Join-Path $OutDir 'quelle.txt') @(
    "Quelle: $Image", "SHA256: $srcHash", "Sektoren: $sectors", "Erzeugt: $(Get-Date -Format s) mit tools\raw2qfil.ps1")
$sums = Get-ChildItem -LiteralPath $OutDir -File | Sort-Object Name | ForEach-Object {
    "{0}  {1}" -f (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLower(), $_.Name }
Set-Content -Encoding ASCII (Join-Path $OutDir 'SHA256SUMS') $sums
Write-Host "QFIL-Paket fertig: $OutDir" -ForegroundColor Green
