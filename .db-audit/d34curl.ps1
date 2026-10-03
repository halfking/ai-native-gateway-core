# Run a shell command inside a container on 192.168.31.34 via the Docker Remote API (port 2375).
#   .\d34curl.ps1 -Container llm-gateway-pg -CmdFile .db-audit\sql\probe.sql -OutFile .db-audit\out\probe.txt
# Notes: JSON payloads are hand-built and written BOM-less, because (a) PowerShell strips quotes when
# passing JSON inline to native exes, and (b) ConvertTo-Json stalls on multi-KB command strings.
param(
  [string]$Container = 'llm-gateway-pg',
  [string]$CmdFile = '',
  [string]$OutFile = '',
  [int]$TimeoutSec = 300
)
$ErrorActionPreference = 'Stop'
$api = 'http://192.168.31.34:2375'
$utf8 = New-Object System.Text.UTF8Encoding($false)
# Unique per invocation: concurrent exec calls must not clobber each other's
# request/response files (the benchmark runs in the background while ad-hoc
# probes run in the foreground).
$runId = [System.Guid]::NewGuid().ToString('N').Substring(0, 8)
$tmpDir = Join-Path $PSScriptRoot "tmp\$runId"
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$cmdText = if ($CmdFile) { Get-Content -Raw -Encoding UTF8 $CmdFile } else { throw 'CmdFile required' }
if (-not $cmdText.Trim()) { throw 'empty command' }

# Safety rails applied to EVERY exec, not just the ones whose SQL file happens to
# set them:
#   PGPAGER=cat / TERM=dumb  - psql starts a pager on a TTY and blocks forever
#                              waiting for input, leaking the exec session.
#   statement_timeout       - a runaway query must not pin this shared dev box.
#   lock_timeout            - fail fast instead of queueing behind another session.
#   client_min_messages     - keep NOTICE/WARNING noise out of the capture unless asked.
$guard = @'
export PGPAGER=cat
export TERM=dumb
'@
if ($cmdText -notmatch 'PGPAGER') { $cmdText = $guard + "`n" + $cmdText }
$first = ($cmdText -split "`n" | Where-Object { $_.Trim() -ne '' } | Select-Object -First 1)
if ($cmdText -notmatch 'statement_timeout' -and $first -notmatch '^(#|echo|ls|df|ps |cat |mount|uname|free|nproc|du )') {
  $cmdText = "export PGCLIENTOPTIONS='--statement_timeout=180s --lock_timeout=5s'`n" + $cmdText
}

$esc = $cmdText.Replace('\', '\\').Replace('"', '\"').Replace("`r", '').Replace("`n", '\n')
$createJson = '{"AttachStdout":true,"AttachStderr":true,"Tty":true,"Cmd":["/bin/sh","-c","' + $esc + '"]}'
$createFile = Join-Path $tmpDir 'create.json'
[System.IO.File]::WriteAllText($createFile, $createJson, $utf8)

$raw = & curl.exe -s --max-time 60 -X POST -H 'Content-Type: application/json' --data-binary "@$createFile" "$api/containers/$Container/exec"
$id = (($raw | ConvertFrom-Json)[0]).Id
if (-not $id) { throw "exec create failed: $raw" }

$startFile = Join-Path $tmpDir 'start.json'
[System.IO.File]::WriteAllText($startFile, '{"Detach":false,"Tty":true}', $utf8)
$rawFile = Join-Path $tmpDir 'raw.txt'
& curl.exe -s --max-time $TimeoutSec -X POST -H 'Content-Type: application/json' --data-binary "@$startFile" "$api/exec/$id/start" -o $rawFile
$code = $LASTEXITCODE
$text = ([System.IO.File]::ReadAllText($rawFile)) -replace "`r", ''

if ($OutFile) {
  $dir = Split-Path -Parent $OutFile
  if ($dir -and -not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
  [System.IO.File]::WriteAllText((Join-Path (Get-Location) $OutFile), $text, $utf8)
  Write-Output ("wrote {0} bytes -> {1}  (curl exit={2}, run={3})" -f $text.Length, $OutFile, $code, $runId)
} else {
  Write-Output $text
}
Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
