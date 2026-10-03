<#
  d252dump.ps1 — 抓取 252 生产库的 live schema dump(SQL 权威结构比对的输入)

  与 d252.ps1 的区别:这里不走 psql,而是在容器内跑 pg_dump,再把结果
  gzip + base64 回传、在本地解压。原因与 34 侧一致:schema dump 有数 MB,
  走 stdout 明文会被 SSH 缓冲与 PowerShell 编码破坏。

  用法:
    powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252dump.ps1 `
        -OutFile .db-audit\out\252_schema.sql
#>
[CmdletBinding()]
param(
  [string]$OutFile = 'F:\workspace\llm-gateway-go\.db-audit\out\252_schema.sql',
  [string]$Container = 'pg-252-pg17',
  [string]$DbUser = 'postgres',
  [string]$DbName = 'llm_gateway',
  [int]$SshPort = 25022,
  [string]$SshHost = 'root@115.29.212.252',
  [int]$ConnectTimeoutSec = 30
)

$ErrorActionPreference = 'Stop'

$outAbs = [System.IO.Path]::GetFullPath($OutFile)
$outDir = Split-Path -Parent $outAbs
if ($outDir -and -not (Test-Path $outDir)) { New-Item -ItemType Directory -Path $outDir -Force | Out-Null }

$runId = [guid]::NewGuid().ToString('N').Substring(0, 12)
$remoteDir = "/tmp/d252dump-$runId"

# 远端:pg_dump -> gzip -> base64,全部在容器内完成,宿主机只中转字符串。
$remoteScript = @"
set -e
mkdir -p $remoteDir
set +e
podman exec -e PGPAGER=cat $Container sh -c 'pg_dump -U $DbUser -d $DbName --schema-only --no-owner --no-acl --quote-all-identifiers 2>/$remoteDir/dump.err' \
  | gzip -9 -c > $remoteDir/schema.gz
rc=`$?
set -e
echo "PIPESTATUS_RC=`$rc"
echo "GZBYTES=`$(wc -c < $remoteDir/schema.gz)"
echo "DUMPERR_BEGIN"
podman exec $Container sh -c 'cat $remoteDir/dump.err' 2>/dev/null || true
echo "DUMPERR_END"
echo "B64_BEGIN"
base64 -w 0 $remoteDir/schema.gz
echo ""
echo "B64_END"
rm -rf $remoteDir
"@

$tmpSh = Join-Path $env:TEMP "d252dump-$runId.sh"
[System.IO.File]::WriteAllText($tmpSh, ($remoteScript -replace "`r`n", "`n"), (New-Object System.Text.UTF8Encoding($false)))

$sshArgs = @('-o', 'BatchMode=yes', '-o', "ConnectTimeout=$ConnectTimeoutSec",
             '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=40',
             '-p', "$SshPort", $SshHost, 'sh -s')
$cmdLine = 'ssh ' + ($sshArgs -join ' ') + ' < "' + $tmpSh + '" 2>&1'
Write-Host "=== fetching schema dump from $SshHost (this takes a while) ==="
$raw = & cmd.exe /c $cmdLine
Remove-Item $tmpSh -Force

$text = ($raw | ForEach-Object { $_.ToString() }) -join "`n"

# ── 解析标记 ──────────────────────────────────────────────────────────────
$gzMatch = [regex]::Match($text, '(?m)^GZBYTES=(\d+)\s*$')
$rcMatch = [regex]::Match($text, '(?m)^PIPESTATUS_RC=(\d+)\s*$')
$b64Match = [regex]::Match($text, '(?s)B64_BEGIN\s*(?<b64>[A-Za-z0-9+/=\r\n]+?)\s*B64_END')

if (-not $b64Match.Success) {
  Write-Host "--- remote output (first 40 lines) ---"
  ($text -split "`n" | Select-Object -First 40) | ForEach-Object { Write-Host $_ }
  throw "B64 payload not found in remote output"
}

$errBlock = [regex]::Match($text, '(?s)DUMPERR_BEGIN\s*(?<e>.*?)\s*DUMPERR_END')
if ($errBlock.Success -and $errBlock.Groups['e'].Value.Trim().Length -gt 0) {
  Write-Host "--- pg_dump stderr ---"
  Write-Host $errBlock.Groups['e'].Value.Trim()
}

# ── base64 -> gzip -> 原文,全程字节级,不做任何文本转换 ────────────────────
$b64 = ($b64Match.Groups['b64'].Value -replace '\s', '')
$gzipBytes = [Convert]::FromBase64String($b64)

# 用 ::new() 而非 New-Object(...),PS 5.1 下参数列表解析更可靠。
$inMs = [System.IO.MemoryStream]::new($gzipBytes, $false)
$gz = [System.IO.Compression.GZipStream]::new($inMs, [System.IO.Compression.CompressionMode]::Decompress)
$outMs = [System.IO.MemoryStream]::new()
$gz.CopyTo($outMs)
$gz.Dispose(); $inMs.Dispose()
$plain = $outMs.ToArray()
$outMs.Dispose()

[System.IO.File]::WriteAllBytes($outAbs, $plain)
$sha = (Get-FileHash -Algorithm SHA256 $outAbs).Hash.ToLower()

Write-Host "=== schema dump done ==="
if ($rcMatch.Success) { Write-Host "pg_dump pipeline rc : $($rcMatch.Groups[1].Value)" }
if ($gzMatch.Success) { Write-Host "gz bytes           : $($gzMatch.Groups[1].Value)" }
Write-Host "plain bytes        : $($plain.Length)"
Write-Host "output             : $outAbs"
Write-Host "sha256             : $sha"
