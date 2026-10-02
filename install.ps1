# install.ps1 — llm-gateway-go 安装引导（Windows，PowerShell 5.1 兼容）
#
# 与 install.sh 同构的入口：先问清楚要装哪种规模，再选安装方式。
# PowerShell 侧对应的业界标准写法是 `irm … | iex`，所以 maintain 通道走它。
#
# 两种规模：
#   lite  本地小规模：SQLite + 单机，不起 PostgreSQL / Redis
#   full  全量：PostgreSQL + Redis，面向生产 / 多副本
#
# 用法：
#   .\install.ps1                            # 交互：选安装方式 + 选规模
#   .\install.ps1 -Channel source            # 从当前源码树编译
#   .\install.ps1 -Channel npm               # npm install -g
#   .\install.ps1 -Channel binary            # 用 release 包自带二进制
#   .\install.ps1 -Channel maintain          # irm | iex 官方一键脚本
#   .\install.ps1 -Mode lite                 # 指定规模
#   .\install.ps1 doctor                     # 只看本机具备哪些安装条件
#   .\install.ps1 build                      # 只编译
#   .\install.ps1 version                    # 只查版本与可获取的更新
#   .\install.ps1 upgrade|uninstall|activate|doctor|heartbeat
#                                          # 透传给 llm-gw-installer
#
# 参数：
#   -Channel source|npm|binary|maintain|goinstall
#   -Mode    lite|full
#   -Dir     <path>       安装目录（默认 $env:LLM_GATEWAY_HOME 或 ~\llm-gateway）
#   -Yes                 全部用默认值，不再提问
#   -DryRun              只打印将要执行的命令
#
# 环境变量：
#   LLM_GATEWAY_HOME   安装目录
#   MAINTAIN_BASE      官方分发入口（默认 https://llmgateway.internal.example.com/maintain-api）
#   NO_INTERACTIVE=1   不提问

# PowerShell 5.1 不支持 if 作为表达式（$x = if (...) { ... }），
# 也不支持 && / || 短路与三元表达式。全部分支写成 if/else。
# param 块必须是脚本的第一条语句（注释之后），否则 PowerShell 直接报语法错。
param(
    [ValidateSet('install', 'build', 'doctor', 'version', 'help')]
    [string]$Action = 'install',
    [string]$Channel = '',
    [ValidateSet('lite', 'full')]
    [string]$Mode = '',
    [string]$Dir = '',
    [switch]$Yes,
    [switch]$DryRun,
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Rest
)

$ErrorActionPreference = 'Stop'

$ScriptDir = $PSScriptRoot
if ([string]::IsNullOrEmpty($ScriptDir)) { $ScriptDir = (Get-Location).Path }

# ── 默认值 ──────────────────────────────────────────────────────────
if ([string]::IsNullOrEmpty($Dir)) {
    if ($env:LLM_GATEWAY_HOME) {
        $Dir = $env:LLM_GATEWAY_HOME
    } else {
        $Dir = Join-Path $env:USERPROFILE 'llm-gateway'
    }
}
if ([string]::IsNullOrEmpty($env:MAINTAIN_BASE)) {
    $env:MAINTAIN_BASE = 'https://llmgateway.internal.example.com/maintain-api'
}

$InstallerName = 'llm-gw-installer.exe'

function Write-Info([string]$m) { Write-Host "[install] $m" }
function Write-Warn2([string]$m) { Write-Host "[install] WARN: $m" -ForegroundColor Yellow }
function Die([string]$m) { Write-Host "[install] ERROR: $m" -ForegroundColor Red; exit 1 }

function Invoke-Checked {
    param([string]$Exe, [string[]]$ArgList)
    Write-Info "执行: $Exe $($ArgList -join ' ')"
    if ($DryRun) { return }
    & $Exe @ArgList
    if ($LASTEXITCODE -ne 0) { Die "$Exe 退出码 $LASTEXITCODE" }
}

function Get-Arch {
    if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') {
        return 'arm64'
    }
    return 'amd64'
}

# ── 体检 ────────────────────────────────────────────────────────────
function Test-Command([string]$Name) {
    return $null -ne (Get-Command $Name -ErrorAction SilentlyContinue)
}

function Get-LocalBinary {
    $cands = @(
        (Join-Path $ScriptDir "llm-gw-installer-windows-$(Get-Arch).exe"),
        (Join-Path $ScriptDir $InstallerName),
        (Join-Path $Dir "bin\$InstallerName")
    )
    foreach ($c in $cands) {
        if (Test-Path $c) { return $c }
    }
    return $null
}

function Test-Docker {
    if ($env:KXMAINT_STUB_DOCKER -eq '1') { return $true }
    if (-not (Test-Command 'docker')) { return $false }
    & docker compose version *> $null
    return ($LASTEXITCODE -eq 0)
}

function Show-Doctor {
    $local = Get-LocalBinary
    Write-Info '体检报告'
    Write-Host ('  {0,-12} {1}' -f '平台', "windows/$(Get-Arch)")
    if (Test-Command 'go') { Write-Host ('  {0,-12} {1}' -f 'go', (go version)) }
    else { Write-Host ('  {0,-12} {1}' -f 'go', '缺失（source / goinstall 需要）') }
    if (Test-Command 'node') { Write-Host ('  {0,-12} {1}' -f 'node', (node --version)) }
    else { Write-Host ('  {0,-12} {1}' -f 'node', '缺失（npm 方式需要）') }
    if (Test-Command 'npm') { Write-Host ('  {0,-12} {1}' -f 'npm', (npm --version)) }
    else { Write-Host ('  {0,-12} {1}' -f 'npm', '缺失（npm 方式需要）') }
    if (Test-Docker) { Write-Host ('  {0,-12} {1}' -f 'docker', (docker --version)) }
    else { Write-Host ('  {0,-12} {1}' -f 'docker', '缺失（full 模式需要）') }
    Write-Host ('  {0,-12} {1}' -f '源码树', $ScriptDir)
    if ($local) { Write-Host ('  {0,-12} {1}' -f '本地二进制', $local) }
    else { Write-Host ('  {0,-12} {1}' -f '本地二进制', '无') }
    Write-Host ''

    Write-Info '可用安装方式：'
    $channels = @{
        'source'    = (Test-Path (Join-Path $ScriptDir 'installer'))
        'goinstall' = (Test-Command 'go')
        'npm'       = (Test-Command 'npm')
        'binary'    = ($null -ne $local)
        'maintain'  = $true
    }
    foreach ($k in @('source', 'goinstall', 'npm', 'binary', 'maintain')) {
        if ($channels[$k]) { Write-Host ('  {0,-12} 可用' -f $k) }
        else { Write-Host ('  {0,-12} 不可用' -f $k) }
    }
    Write-Host ''
}

# ── 交互 ────────────────────────────────────────────────────────────
function Test-Interactive {
    if ($env:NO_INTERACTIVE -eq '1') { return $false }
    if ($DryRun) { return $false }
    try { return [Console]::IsInputRedirected -eq $false } catch { return $false }
}

function Read-Answer([string]$Prompt) {
    Write-Host $Prompt -NoNewline
    return (Read-Host)
}

function Choose-Channel {
    if ($Channel) { Write-Info "安装方式由参数指定：$Channel"; return }
    if (-not (Test-Interactive)) {
        $local = Get-LocalBinary
        if ($local) { $script:Channel = 'binary' }
        elseif (Test-Path (Join-Path $ScriptDir 'installer')) { $script:Channel = 'source' }
        else { $script:Channel = 'maintain' }
        Write-Info "非交互，自动选择安装方式：$script:Channel"
        return
    }
    Write-Host '可选安装方式：'
    Write-Host ''
    Write-Host '  [1] source     从当前源码树编译（go build）'
    Write-Host '  [2] goinstall  go install ...@latest'
    Write-Host '  [3] npm        npm install -g —— Windows 上摩擦最小'
    Write-Host '  [4] binary     使用 release 包里自带的 llm-gw-installer.exe'
    Write-Host '  [5] maintain   官方一键脚本（irm … | iex）'
    Write-Host ''
    $a = Read-Answer '请选择安装方式 [1]: '
    switch ($a) {
        '2' { $script:Channel = 'goinstall' }
        '3' { $script:Channel = 'npm' }
        '4' { $script:Channel = 'binary' }
        '5' { $script:Channel = 'maintain' }
        default { $script:Channel = 'source' }
    }
    Write-Info "已选择安装方式：$script:Channel"
}

function Choose-Mode {
    if ($Mode) { Write-Info "规模由参数指定：$Mode"; return }
    if (-not (Test-Interactive)) {
        $script:Mode = 'full'
        Write-Info '非交互，规模默认 full（与 llm-gw-installer 的非交互默认一致）'
        return
    }
    Write-Host '可选规模：'
    Write-Host ''
    Write-Host '  [1] lite —— 本地小规模：SQLite + 单机，不起 PostgreSQL / Redis'
    Write-Host '  [2] full —— 全量：PostgreSQL + Redis，面向生产 / 多副本'
    Write-Host ''
    $a = Read-Answer '请选择规模 [1]: '
    if ($a -eq '2') { $script:Mode = 'full' } else { $script:Mode = 'lite' }
    Write-Info "已选择规模：$script:Mode"
}

# ── 各安装方式 ──────────────────────────────────────────────────────
function Build-FromSource {
    $mod = Join-Path $ScriptDir 'installer'
    if (-not (Test-Path $mod)) { Die "不在源码树内：找不到 $mod" }
    if (-not (Test-Command 'go')) { Die 'source 需要 Go 工具链' }
    $outDir = Join-Path $Dir 'bin'
    if (-not (Test-Path $outDir)) { New-Item -ItemType Directory -Path $outDir -Force | Out-Null }
    $out = Join-Path $outDir $InstallerName
    Write-Info "从源码编译：$mod → $out"
    if ($DryRun) { return $out }
    Push-Location $mod
    try {
        $env:CGO_ENABLED = '0'
        & go build -trimpath -o $out ./cmd/llm-gw-installer
        if ($LASTEXITCODE -ne 0) { Die "源码编译失败（退出码 $LASTEXITCODE）" }
    } finally {
        Pop-Location
    }
    if (-not (Test-Path $out)) { Die "编译未产出二进制：$out" }
    return $out
}

function Install-ViaNpm {
    if (-not (Test-Command 'npm')) { Die 'npm 方式需要 Node.js / npm' }
    Write-Info 'npm install -g @kaixuan/llm-gw-installer'
    if ($DryRun) { return (Join-Path (npm prefix -g) $InstallerName) }
    Invoke-Checked 'npm' @('install', '-g', '@kaixuan/llm-gw-installer')
    return (Join-Path (npm prefix -g) $InstallerName)
}

function Install-ViaMaintain {
    Write-Info '转交官方一键脚本（irm … | iex）'
    if ($DryRun) { Write-Info '(dry-run) irm …/install-scripts/install | iex'; return $null }
    $url = "$($env:MAINTAIN_BASE)/distribution/install-scripts/install"
    Write-Info "下载并执行：$url"
    & (New-Object System.Net.WebClient).DownloadString($url) | Invoke-Expression
    return $null
}

# ── 主流程 ──────────────────────────────────────────────────────────
$subcommands = @('activate', 'completion', 'doctor', 'heartbeat', 'help', 'uninstall', 'upgrade', 'version')

if ($Action -eq 'help') {
    Get-Help $PSCommandPath
    exit 0
}

if ($Action -eq 'doctor') {
    Show-Doctor
    exit 0
}

if ($Action -eq 'version') {
    $local = Get-LocalBinary
    if ($local) {
        Write-Info '本机已安装的 installer 版本：'
        & $local version
    } else {
        Write-Warn2 '本机没有已安装的 llm-gw-installer'
    }
    Write-Host ''
    Write-Info '可获取的最新版本与安装方式：'
    Write-Host '  交互安装（推荐，会先问规模）：'
    Write-Host "    irm $($env:MAINTAIN_BASE)/distribution/install-scripts/install | iex"
    Write-Host '  本地小规模（lite，主机二进制 + SQLite）：'
    Write-Host "    irm $($env:MAINTAIN_BASE)/distribution/install-scripts/host | iex"
    Write-Host '  全量（full，Docker + PostgreSQL + Redis）：'
    Write-Host "    irm $($env:MAINTAIN_BASE)/distribution/install-scripts/docker | iex"
    Write-Host '  已有实例的本机升级：'
    Write-Host "    irm $($env:MAINTAIN_BASE)/distribution/install-scripts/upgrade | iex"
    Write-Host "  当前平台：windows/$(Get-Arch)"
    exit 0
}

if ($Action -eq 'build') {
    if (-not $Channel) { $Channel = 'source' }
    $bin = Build-FromSource
    Write-Info "已编译：$bin"
    exit 0
}

# 未识别的首个参数当作 llm-gw-installer 自己的子命令透传，保持旧用法可用。
if ($Action -eq 'install' -and $Rest -and $Rest.Count -gt 0 -and $subcommands -contains $Rest[0]) {
    $local = Get-LocalBinary
    if (-not $local) { Die '找不到 llm-gw-installer（先跑 .\install.ps1 -Channel source）' }
    & $local @Rest
    exit $LASTEXITCODE
}

Show-Doctor
Choose-Channel
Choose-Mode
Write-Info "开始安装（方式=$Channel 规模=$Mode 目录=$Dir）"

$installer = $null
switch ($Channel) {
    'source' { $installer = Build-FromSource }
    'goinstall' {
        if (-not (Test-Command 'go')) { Die 'goinstall 需要 Go 工具链' }
        Write-Info 'go install github.com/kaixuan/llm-gateway-go/installer/cmd/llm-gw-installer@latest'
        if (-not $DryRun) {
            & go install 'github.com/kaixuan/llm-gateway-go/installer/cmd/llm-gw-installer@latest'
            if ($LASTEXITCODE -ne 0) { Die 'go install 失败' }
        }
        $gobin = (& go env GOBIN)
        if ([string]::IsNullOrEmpty($gobin)) { $gobin = (& go env GOPATH) + '\bin' }
        $installer = Join-Path $gobin $InstallerName
    }
    'npm' { $installer = Install-ViaNpm }
    'binary' { $installer = Get-LocalBinary; if (-not $installer) { Die '找不到 release 包自带的 llm-gw-installer.exe' } }
    'maintain' { $installer = Install-ViaMaintain }
    default { Die "未知安装方式 '$Channel'" }
}

if ($Channel -eq 'maintain') {
    Write-Info '官方一键脚本已执行完毕'
    exit 0
}
if (-not $installer) { Die '没有解析出可执行的 llm-gw-installer' }
if (-not (Test-Path $installer)) { Die "installer 二进制不存在：$installer" }
if (-not (Test-Path $Dir)) { New-Item -ItemType Directory -Path $Dir -Force | Out-Null }

Write-Info "启动安装向导：$installer install --dir $Dir --mode $Mode"
& $installer install --dir $Dir --mode $Mode
exit $LASTEXITCODE
