<#
  d252async.ps1 — 252 生产库异步采集:提交 -> 轮询 -> 取回

  ── 为什么是这个形态(实测得出,不要改回同步) ──────────────────────────────
  252 是 4 核 / load 20~26 的生产机,宿主机跑着 30+ 个容器。实测三种模式:

    模式                          实测耗时            结果
    ──────────────────────────────────────────────────────────────
    同步 + 长连接(38 节采集)       >20 分钟            超时
    同步 + sh -s(stdin 传脚本)     5 行脚本 141 秒      不可用
    同步 + 命令字符串参数           28~38 秒            能连上但太慢
    scp 上传 + setsid 后台 + 短命令  38 秒提交,秒级取回  ✅ 可用

  关键点:
    1) 传脚本必须用 scp,不能用 `ssh sh -s` —— 后者在该主机上慢到不可用。
    2) 作业必须 setsid + 三个 fd 全部重定向,否则 SSH 通道不返回。
    3) 轮询与取回都是短命令,单次 ~30 秒,可接受。

  ── 用法 ─────────────────────────────────────────────────────────────────
    # 1) 提交(约 40 秒)
    powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252async.ps1 `
        -Action submit -RunId r1 `
        -SqlFile sql\audit\2026-10-02-db-audit-252-minimal.sql

    # 2) 轮询(约 30 秒/次;STATE=finished 即可取回)
    powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252async.ps1 `
        -Action status -RunId r1

    # 3) 取回
    powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252async.ps1 `
        -Action fetch -RunId r1 -OutFile .db-audit\out\inst252.txt

  安全:SET default_transaction_read_only = on(不是 BEGIN READ ONLY 包裹,原因见下)
        + 写语句闸门 + statement/lock timeout。
        远端目录 /tmp/d252async-<RunId>,-Action cleanup 可清除。
#>
[CmdletBinding()]
param(
  [string]$SqlFile = '',
  [string]$RunId = '',
  [ValidateSet('submit', 'status', 'fetch', 'cleanup')]
  [string]$Action = 'submit',
  [string]$OutFile = '',
  [string]$Container = 'pg-252-pg17',
  [string]$DbUser = 'postgres',
  [string]$DbName = 'llm_gateway',
  [int]$StatementTimeoutSec = 120,
  [int]$SshPort = 25022,
  [string]$SshHost = 'root@115.29.212.252'
)

$ErrorActionPreference = 'Stop'
$utf8 = New-Object System.Text.UTF8Encoding($false)
$tmpDir = Join-Path $env:TEMP ("d252async-" + $RunId)

if ([string]::IsNullOrWhiteSpace($RunId)) {
  if ($Action -ne 'submit') { throw "-RunId is required for action '$Action'" }
  $RunId = 'r' + [guid]::NewGuid().ToString('N').Substring(0, 8)
}
if ($RunId -notmatch '^[A-Za-z0-9_.-]{1,32}$') { throw "invalid -RunId: $RunId" }
$R = "/tmp/d252async-$RunId"

# 只走命令字符串通道 —— 该主机上 `ssh sh -s` 慢到不可用。
function Invoke-Cmd {
  param([string]$Cmd, [int]$TimeoutSec = 25)
  $args = @('-o', 'BatchMode=yes', "-oConnectTimeout=$TimeoutSec",
            '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=20',
            '-p', "$SshPort", $SshHost, $Cmd)
  $raw = & ssh @args 2>&1
  return (($raw | ForEach-Object { $_.ToString() }) -join "`n")
}

switch ($Action) {

  'submit' {
    if (-not $SqlFile) { throw '-SqlFile is required for submit' }
    $sqlAbs = (Resolve-Path $SqlFile).Path
    $body = [System.IO.File]::ReadAllText($sqlAbs)

    # 写语句闸门(与 d252.ps1 同口径):默认只允许只读脚本碰生产库。
    $stripped = ($body -split "`n" | ForEach-Object { $_ -replace '--.*$', '' }) -join "`n"
    foreach ($t in @('\bDROP\b', '\bDELETE\b', '\bTRUNCATE\b', '\bUPDATE\b', '\bINSERT\b',
                     '\bALTER\b', '\bCREATE\b', '\bREINDEX\b', '\bVACUUM\b', '\bCLUSTER\b',
                     '\bCOPY\b', '\bGRANT\b', '\bREVOKE\b', 'pg_terminate_backend\b')) {
      if ($stripped -match $t) {
        throw "Refusing to submit a write statement to the 252 PRODUCTION database. Matched: $t"
      }
    }

    # 刻意不用 BEGIN READ ONLY 包整个脚本。
    # 踩过的坑:一条语句报错会让事务进入 aborted 状态,其后所有语句被一并拒绝
    # (round2 实测:jit_time 一处列名错误,9 个 section 全空)。
    # 改用 default_transaction_read_only —— 每条语句独立且都是只读,
    # 单条失败不影响其余,且同样挡住误写。
    $wrapped = "\set ON_ERROR_STOP off`n" +
               "SET default_transaction_read_only = on;`n" +
               "SET statement_timeout = '${StatementTimeoutSec}s';`n" +
               "SET lock_timeout = '5s';`n" +
               "SET idle_in_transaction_session_timeout = '600s';`n" +
               "SET client_min_messages = warning;`n" + $body

    if (-not (Test-Path $tmpDir)) { New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null }
    $localSql = Join-Path $tmpDir 'wrapped.sql'
    [System.IO.File]::WriteAllText($localSql, $wrapped, $utf8)

    # runner 路径写死,便于用极短命令启动
    $runner = @"
#!/bin/sh
D=$R
podman exec -i -e PGPAGER=cat $Container psql -U $DbUser -d $DbName -X -q -A -t -f - < `$D/wrapped.sql > `$D/out.txt 2> `$D/err.txt
echo `$? > `$D/rc
touch `$D/done
"@
    $localSh = Join-Path $tmpDir 'run.sh'
    [System.IO.File]::WriteAllText($localSh, ($runner -replace "`r`n", "`n"), $utf8)

    Write-Host "=== 1/3 建远端目录 ==="
    Invoke-Cmd -Cmd "mkdir -p $R" | Write-Host

    Write-Host "=== 2/3 scp 上传 SQL 与 runner ==="
    $scpArgs = @('-o', 'BatchMode=yes', '-oConnectTimeout=25', '-P', "$SshPort", $localSql, $localSh, "${SshHost}:$R/")
    & scp @scpArgs 2>&1 | ForEach-Object { Write-Host "  $_" }
    if ($LASTEXITCODE -ne 0) { throw "scp failed (exit $LASTEXITCODE)" }

    Write-Host "=== 3/3 setsid 后台启动 ==="
    # 三个 fd 全部重定向,SSH 才会立即返回;否则通道被后台进程占住不放。
    $out = Invoke-Cmd -Cmd "chmod +x $R/run.sh; setsid $R/run.sh </dev/null >/dev/null 2>&1 & echo LAUNCHED"
    Write-Host "  $out"
    Write-Host ""
    Write-Host "RunId    : $RunId"
    Write-Host "远端目录 : $R"
    Write-Host "下一步   : -Action status -RunId $RunId   (STATE=finished 后 fetch)"
  }

  'status' {
    $cmd = "if [ -f $R/done ]; then echo STATE=finished; echo RC=`$(cat $R/rc 2>/dev/null); echo BYTES=`$(wc -c < $R/out.txt 2>/dev/null); else echo STATE=running; echo BYTES=`$(wc -c < $R/out.txt 2>/dev/null || echo 0); fi"
    Write-Host (Invoke-Cmd -Cmd $cmd)
  }

  'fetch' {
    if (-not $OutFile) { throw '-OutFile is required for fetch' }
    $cmd = "if [ ! -f $R/done ]; then echo NOT_FINISHED; exit 0; fi; " +
           "echo RC=`$(cat $R/rc 2>/dev/null); " +
           "echo OUTB64_BEGIN; base64 -w 0 $R/out.txt; echo ''; echo OUTB64_END; " +
           "echo ERRB64_BEGIN; base64 -w 0 $R/err.txt 2>/dev/null || true; echo ''; echo ERRB64_END"
    $res = Invoke-Cmd -Cmd $cmd -TimeoutSec 120
    if ($res -match 'NOT_FINISHED') { Write-Host "still running; poll with -Action status"; exit 2 }

    $om = [regex]::Match($res, '(?s)OUTB64_BEGIN\s*(?<b>[A-Za-z0-9+/=\r\n]*?)\s*OUTB64_END')
    if (-not $om.Success) { Write-Host "--- response head ---"; ($res -split "`n" | Select-Object -First 10) | Write-Host; throw "OUTB64 block not found" }
    $bytes = [Convert]::FromBase64String(($om.Groups['b'].Value -replace '\s', ''))

    $outAbs = [System.IO.Path]::GetFullPath($OutFile)
    $od = Split-Path -Parent $outAbs
    if ($od -and -not (Test-Path $od)) { New-Item -ItemType Directory -Path $od -Force | Out-Null }
    [System.IO.File]::WriteAllBytes($outAbs, $bytes)

    $rc = [regex]::Match($res, '(?m)^RC=(\d+)\s*$').Groups[1].Value
    Write-Host "=== fetched ==="
    Write-Host "rc     : $rc"
    Write-Host "output : $outAbs ($($bytes.Length) bytes)"
    Write-Host "sha256 : $((Get-FileHash -Algorithm SHA256 $outAbs).Hash.ToLower())"

    $em = [regex]::Match($res, '(?s)ERRB64_BEGIN\s*(?<b>[A-Za-z0-9+/=\r\n]*?)\s*ERRB64_END')
    if ($em.Success -and $em.Groups['b'].Value -match '[A-Za-z0-9+/=]{4}') {
      $errTxt = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(($em.Groups['b'].Value -replace '\s','')))
      $lines = @($errTxt -split "`n" | Where-Object { $_ -match '(psql:|ERROR|FATAL|WARNING:)' })
      if ($lines.Count -gt 0) {
        Write-Host "--- diagnostics ($($lines.Count)) ---"
        $lines | Select-Object -First 20 | ForEach-Object { Write-Host $_ }
      }
    }
    $sec = (Select-String -Path $outAbs -Pattern '^===SECTION:(.+)===' -ErrorAction SilentlyContinue)
    Write-Host "sections: $($sec.Count)"

    # R89-DX（211 号）新增：把「跑全了吗」变成**会被报出来的事实**。
    #
    # 为什么要加：本脚本在 :95 刻意注入 `\set ON_ERROR_STOP off`（:93-94 写明了理由：
    # 「单条失败不影响其余」）。这个取舍本身是合理的，但它有一个**未被兜住的后果** ——
    # 某一段炸了之后，报表**看起来依然是完整的**，只是少了那一段。
    # 而 :95 的注入同时让 psql **以 0 退出**（:160 的 rc），错误只出现在 :167-171 的
    # diagnostics 里、且只印前 20 行、**不落进报表**。
    # ⇒ 210 号登记的 A1/A2（STORAGE_MIX 整行塌成 NULL、CHECKPOINTS 列不存在）就是
    # 这样一路静默到没人发现的。
    #
    # 期望值**从源文件自己数**，不写死 38：写死会在有人增删段落时立刻变成一句谎话，
    # 而谎话比没有更坏 —— 它会把「不完整」重新伪装成「已核对」。
    $expected = 0
    $expectedNames = @()
    if ($SqlFile -and (Test-Path $SqlFile)) {
      $expectedNames = @(Select-String -Path $SqlFile -Pattern '===SECTION:([A-Za-z0-9_]+)===' -AllMatches -ErrorAction SilentlyContinue |
                         ForEach-Object { $_.Matches[0].Groups[1].Value })
      $expected = $expectedNames.Count
    }
    if ($expected -gt 0) {
      Write-Host "expected: $expected (from $(Split-Path $SqlFile -Leaf))"
      $gotNames = @($sec | ForEach-Object { $_.Matches[0].Groups[1].Value })
      $missing = @($expectedNames | Where-Object { $gotNames -notcontains $_ })
      if ($missing.Count -gt 0) {
        Write-Host "*** INCOMPLETE: 缺 $($missing.Count) 段 -> $($missing -join ', ') ***"
        Write-Host "*** 这些段的错误只在本机 diagnostics 里，不在报表里；请回查 err 输出。 ***"
      } else {
        Write-Host "complete: 全部 $expected 段都在。"
      }
    } else {
      Write-Host "expected: (无法从 '$SqlFile' 数出，跳过完整性校验)"
    }

    if ($sec.Count -gt 0) { $sec | ForEach-Object { Write-Host ("  " + $_.Matches[0].Groups[1].Value) } }
  }

  'cleanup' {
    # 回收站语义由本机 rm 处理;远端是 /tmp 下的纯临时目录,用 rmdir/删除是安全的运维动作。
    Write-Host (Invoke-Cmd -Cmd "rm -rf $R; if [ -d $R ]; then echo STILL_THERE; else echo CLEANED $R; fi")
  }
}
