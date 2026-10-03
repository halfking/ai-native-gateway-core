<#
  d252.ps1 — 252 侧 PostgreSQL 只读采集执行通道

  用途:把本地 SQL 文件送入 252 的 pg-252-pg17 容器执行,结果取回本地。
  设计沿用 34 侧 d34curl.ps1 已验证的方案(base64 传输 + SHA-256 校验),
  因为 PowerShell 5.1 的管道会给 native 命令注入 BOM,SQL 走 stdin 不可靠。

  安全约束(252 是生产库):
    - 默认只读:强制包裹在只读事务中,SET TRANSACTION READ ONLY
    - 注入 statement_timeout / lock_timeout,避免长查询拖垮生产
    - 注入 PGPAGER=cat,避免 psql TTY pager 卡死
    - 不接受 DROP/DELETE/TRUNCATE/ALTER/CREATE 等写操作(可选 -AllowWrite 放开,
      但仍需显式指定)

  用法:
    powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252.ps1 `
        -SqlFile sql\audit\2026-10-02-db-audit-collect.sql `
        -OutFile .db-audit\out\inst252.txt
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$SqlFile,
  [Parameter(Mandatory = $true)][string]$OutFile,
  [int]$TimeoutSec = 600,
  [string]$Container = 'pg-252-pg17',
  [switch]$AllowWrite
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $SqlFile)) { throw "SQL file not found: $SqlFile" }
$SqlAbs = (Resolve-Path $SqlFile).Path
$OutAbs = [System.IO.Path]::GetFullPath($OutFile)
$OutDir = Split-Path -Parent $OutAbs
if ($OutDir -and -not (Test-Path $OutDir)) { New-Item -ItemType Directory -Path $OutDir -Force | Out-Null }

# ── 安全闸门:默认拒绝写语句 ────────────────────────────────────────────────
$sqlText = [System.IO.File]::ReadAllText($SqlAbs)
if (-not $AllowWrite) {
  $writeTokens = @(
    '\bDROP\b', '\bDELETE\b', '\bTRUNCATE\b', '\bUPDATE\b', '\bINSERT\b',
    '\bALTER\b', '\bCREATE\b', '\bREINDEX\b', '\bVACUUM\b', '\bCLUSTER\b',
    '\bCOPY\b', '\bGRANT\b', '\bREVOKE\b', '\bpg_terminate_backend\b'
  )
  foreach ($t in $writeTokens) {
    # 去掉行注释后再匹配,避免注释里的词误伤
    $stripped = ($sqlText -split "`n" | ForEach-Object { ($_ -replace '--.*$', '') }) -join "`n"
    if ($stripped -match $t) {
      throw ("Refusing to run a write statement against the 252 PRODUCTION database. " +
             "Matched token: $t . Re-run with -AllowWrite only if you have explicit authorization.")
    }
  }
}

# ── 生成远端包装脚本:只读事务 + 超时 + 统计 ──────────────────────────────
$runId = [guid]::NewGuid().ToString('N').Substring(0, 12)
$remoteDir = "/tmp/d252-$runId"

$header = @"
\set ON_ERROR_STOP on
SET default_transaction_read_only = on;
SET statement_timeout = '${TimeoutSec}s';
SET lock_timeout = '5s';
SET idle_in_transaction_session_timeout = '60s';
"@

# 刻意不用 BEGIN READ ONLY / COMMIT 包整个脚本:一条语句报错会让事务进入
# aborted 状态,其后所有语句被一并拒绝,导致后面所有 section 变空。
# default_transaction_read_only 同样能挡住误写,但每条语句彼此独立。
$footer = ""

$wrapped = $header + "`n" + $sqlText + "`n" + $footer

$tmpSql = Join-Path $env:TEMP "d252-$runId.sql"
[System.IO.File]::WriteAllText($tmpSql, $wrapped, (New-Object System.Text.UTF8Encoding($false)))
$b64 = [Convert]::ToBase64String([System.IO.File]::ReadAllBytes($tmpSql))
$sha = (Get-FileHash -Algorithm SHA256 $tmpSql).Hash.ToLower()
Remove-Item $tmpSql -Force

# ── 远端执行 ──────────────────────────────────────────────────────────────
# stderr 与 stdout 合并取回(psql 的 NOTICE/ERROR 也需要留存),rc 原样透传。
$remoteScript = @"
set -e
mkdir -p $remoteDir
printf '%s' '$b64' | base64 -d > $remoteDir/q.sql
# SQL 文件已在宿主机上,直接用 stdin 管道送进容器 —— 避开 podman cp 的路径/权限坑。
set +e
cat $remoteDir/q.sql | podman exec -i -e PGPAGER=cat $Container \
  psql -U postgres -d llm_gateway -X -q -A -t -f - > $remoteDir/out.txt 2> $remoteDir/err.txt
rc=`$?
set -e
cat $remoteDir/err.txt || true
cat $remoteDir/out.txt || true
podman exec $Container sh -c 'rm -rf $remoteDir' >/dev/null 2>&1 || true
rm -rf $remoteDir
# 本地 cmd.exe 会吞掉退出码,显式回传标记由调用方解析(不要再 exit,否则标记发不出去)。
echo "__D252_RC__=`$rc"
"@

$tmpSh = Join-Path $env:TEMP "d252-$runId.sh"
[System.IO.File]::WriteAllText($tmpSh, ($remoteScript -replace "`r`n", "`n"), (New-Object System.Text.UTF8Encoding($false)))

# PowerShell 5.1 不支持 `< file` 输入重定向(那是 bash 语法),且其管道会注入 BOM。
# 改用 cmd.exe 做字节流原样重定向 —— 与 34 侧 d34curl.ps1 踩过的坑一致。
$sshArgs = @('-o', 'BatchMode=yes', '-o', 'ConnectTimeout=20', '-p', '25022',
             'root@115.29.212.252', 'sh -s')
$cmdLine = 'ssh ' + ($sshArgs -join ' ') + ' < "' + $tmpSh + '" 2>&1'
$outText = & cmd.exe /c $cmdLine
Remove-Item $tmpSh -Force

# cmd.exe /c 会吞掉 ssh 的真实退出码,改为解析远端回传的 __D252_RC__ 标记。
$joined = ($outText | ForEach-Object { $_.ToString() }) -join "`n"
$rcMatch = [regex]::Match($joined, '(?m)^__D252_RC__=(\d+)\s*$')
$psqlRc = if ($rcMatch.Success) { [int]$rcMatch.Groups[1].Value } else { -1 }
# 标记行不是 SQL 输出,必须剔除,否则会污染与 34 侧的逐行 diff。
$joined = [regex]::Replace($joined, '(?m)^__D252_RC__=\d+\s*\r?\n?', '')
$joined = $joined -replace "`r`n", "`n"
[System.IO.File]::WriteAllText($OutAbs, $joined, (New-Object System.Text.UTF8Encoding($false)))

$errLines = @($joined -split "`n" | Where-Object { $_ -match '^(psql:|ERROR|FATAL|WARNING:)' })
Write-Host "=== 252 exec done (psql rc=$psqlRc) ==="
Write-Host "SQL   : $SqlAbs (sha256 $sha)"
Write-Host "Output: $OutAbs ($((Get-Item $OutAbs).Length) bytes, $((Get-Content $OutAbs).Count) lines)"
if ($errLines.Count -gt 0) {
  Write-Host "--- diagnostics ($($errLines.Count)) ---"
  $errLines | Select-Object -First 25 | ForEach-Object { Write-Host $_ }
} else {
  Write-Host "--- no ERROR/FATAL in output ---"
}
exit $psqlRc
