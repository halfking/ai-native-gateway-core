<#
  d252sh.ps1 — 把本地 .sh 送到 252 执行并取回 stdout/stderr/rc

  存在的理由:PowerShell 5.1 在处理含 $(...)、单引号、双引号混排的远端
  命令行时会先在本地展开,导致远端收到的命令被改坏(本项目已踩两次)。
  与 d252.ps1 一样,全部经由本地临时文件 + cmd.exe 字节流重定向传输。

  用法:
    powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252sh.ps1 `
        -ShFile .db-audit\sql\probe.sh -OutFile .db-audit\out\probe_out.txt
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$ShFile,
  [Parameter(Mandatory = $true)][string]$OutFile,
  [int]$ConnectTimeoutSec = 30,
  [int]$SshPort = 25022,
  [string]$SshHost = 'root@115.29.212.252'
)

$ErrorActionPreference = 'Stop'

$shAbs = (Resolve-Path $ShFile).Path
$outAbs = [System.IO.Path]::GetFullPath($OutFile)
$outDir = Split-Path -Parent $outAbs
if ($outDir -and -not (Test-Path $outDir)) { New-Item -ItemType Directory -Path $outDir -Force | Out-Null }

# 归一化为 LF 并强制 UTF-8 BOM 无关的可执行脚本
$script = [System.IO.File]::ReadAllText($shAbs) -replace "`r`n", "`n"
$tmpSh = Join-Path $env:TEMP ("d252sh-" + [guid]::NewGuid().ToString('N').Substring(0, 10) + ".sh")
[System.IO.File]::WriteAllText($tmpSh, $script, (New-Object System.Text.UTF8Encoding($false)))

$sshArgs = @('-o', 'BatchMode=yes', "-oConnectTimeout=$ConnectTimeoutSec",
             '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=60',
             '-p', "$SshPort", $SshHost, 'sh -s')
$cmdLine = 'ssh ' + ($sshArgs -join ' ') + ' < "' + $tmpSh + '" 2>&1'
$raw = & cmd.exe /c $cmdLine
Remove-Item $tmpSh -Force

$joined = ($raw | ForEach-Object { $_.ToString() }) -join "`n"
$joined = [regex]::Replace($joined, '(?m)^__D252SH_RC__=\d+\s*\r?\n?', '')
$joined = $joined -replace "`r`n", "`n"
[System.IO.File]::WriteAllText($outAbs, $joined, (New-Object System.Text.UTF8Encoding($false)))

Write-Host "=== d252sh done ==="
Write-Host "script: $shAbs"
Write-Host "output: $outAbs ($((Get-Item $outAbs).Length) bytes)"
$rc = [regex]::Match($joined, 'RC=(\d+)')
if ($rc.Success) { Write-Host "remote rc: $($rc.Groups[1].Value)" }
