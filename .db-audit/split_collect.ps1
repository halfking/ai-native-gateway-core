<#
  split_collect.ps1 — 把通用采集脚本按 section 边界切成若干批

  为什么需要:252 是 4 核 / load 16 的生产机,完整 38 节脚本单次跑可能
  超过 SSH 超时上限并前功尽弃。切成小批后每批都能快速完成,即使某批超时
  也只损失那一批,且已完成的批次结果可以直接合并。

  切分点是 psql 元命令 `\echo '===SECTION:<name>==='`,采集脚本的约定。
  每批保留脚本头部的前导区(安全 SET + 头注释)。

  用法:
    powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\split_collect.ps1 `
        -Src sql\audit\2026-10-02-db-audit-collect.sql -OutDir .db-audit\sql\batches -PerBatch 10
#>
[CmdletBinding()]
param(
  [string]$Src = 'F:\workspace\llm-gateway-go\sql\audit\2026-10-02-db-audit-collect.sql',
  [string]$OutDir = 'F:\workspace\llm-gateway-go\.db-audit\sql\batches',
  [int]$PerBatch = 10
)

$ErrorActionPreference = 'Stop'

$lines = [System.IO.File]::ReadAllLines($Src)
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

# section 起始行索引
$starts = @()
for ($i = 0; $i -lt $lines.Length; $i++) {
  if ($lines[$i] -match "^\\echo\s+'?===SECTION:(?<n>.+?)===") { $starts += $i }
}
if ($starts.Count -eq 0) { throw "no section markers found in $Src" }

# 前导区:第一段代码之前的注释与安全 SET(到第一个 section 之前的最后一行非注释/非空行)
$head = @()
for ($i = 0; $i -lt $starts[0]; $i++) {
  $t = $lines[$i].Trim()
  if ($t -eq '' -or $t.StartsWith('--')) { $head += $lines[$i] }
  else { $head += $lines[$i] }   # SET / \pset 等前置命令要保留
}

$names = @()
for ($i = 0; $i -lt $starts.Count; $i++) {
  $n = [regex]::Match($lines[$starts[$i]], "===SECTION:(?<n>.+?)===").Groups['n'].Value
  $names += $n
}

if (-not (Test-Path $OutDir)) { New-Item -ItemType Directory -Path $OutDir -Force | Out-Null }
Get-ChildItem $OutDir -Filter 'batch-*.sql' -ErrorAction SilentlyContinue | Remove-Item -Force

$batchCount = [math]::Ceiling($starts.Count / $PerBatch)
$manifest = @("batch|file|sections")

for ($b = 0; $b -lt $batchCount; $b++) {
  $from = $b * $PerBatch
  $to = [math]::Min($from + $PerBatch, $starts.Count) - 1
  $file = Join-Path $OutDir ("batch-{0:d2}.sql" -f ($b + 1))

  $sb = New-Object System.Text.StringBuilder
  [void]$sb.AppendLine("-- batch $($b+1)/$batchCount  sections: $($from + 1)..$($to + 1) of $($starts.Count)")
  [void]$sb.AppendLine("-- 自动切分自: $Src")
  [void]$sb.AppendLine("")
  foreach ($h in $head) { [void]$sb.AppendLine($h) }
  [void]$sb.AppendLine("")

  for ($s = $from; $s -le $to; $s++) {
    $end = if ($s -lt $starts.Count - 1) { $starts[$s + 1] - 1 } else { $lines.Length - 1 }
    for ($i = $starts[$s]; $i -le $end; $i++) { [void]$sb.AppendLine($lines[$i]) }
  }

  [System.IO.File]::WriteAllText($file, $sb.ToString(), $utf8NoBom)
  $secList = ($names[$from..$to]) -join ','
  $manifest += "$($b+1)|$(Split-Path -Leaf $file)|$secList"
  Write-Host "batch $($b+1)/$batchCount -> $(Split-Path -Leaf $file)  sections: $secList"
}

$manifestFile = Join-Path $OutDir 'MANIFEST.txt'
[System.IO.File]::WriteAllText($manifestFile, ($manifest -join "`n") + "`n", $utf8NoBom)
Write-Host ""
Write-Host "total sections: $($starts.Count)   batches: $batchCount"
Write-Host "manifest: $manifestFile"
