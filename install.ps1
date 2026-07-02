# install.ps1 — Windows PowerShell 一键安装入口
# 自动选用匹配的 llm-gw-installer.exe 二进制
# 默认部署到 $HOME\llm-gateway\

$ErrorActionPreference = 'Stop'

$ScriptDir = $PSScriptRoot

# ── 探测当前 OS / arch ─────────────────────────────────────────
$OS = 'windows'
# 注意：PowerShell 5.1 不支持 if 作为表达式（$x = if (...) { ... }）
# 用 if/else 分支赋值更兼容
if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') {
    $Arch = 'arm64'
} else {
    $Arch = 'amd64'
}

# ── 选用二进制 ─────────────────────────────────────────────────
$Binary = Join-Path $ScriptDir "llm-gw-installer-${OS}-${Arch}.exe"

if (-not (Test-Path $Binary)) {
    # 兜底：使用不带平台后缀的
    $Binary = Join-Path $ScriptDir 'llm-gw-installer.exe'
}

if (-not (Test-Path $Binary)) {
    Write-Host "❌ 未找到 installer 二进制（期望 $Binary）" -ForegroundColor Red
    Write-Host "请重新下载 release 包"
    exit 1
}

# ── 计算默认安装目录 ──────────────────────────────────────────
if ($env:LLM_GATEWAY_HOME) {
    $DefaultHome = $env:LLM_GATEWAY_HOME
} else {
    $DefaultHome = Join-Path $env:USERPROFILE 'llm-gateway'
}

Write-Host "╔════════════════════════════════════════════════════════════════╗"
Write-Host "║  LLM Gateway 一键安装器                                       ║"
Write-Host "╠════════════════════════════════════════════════════════════════╣"
Write-Host "║  默认部署到: $DefaultHome"
Write-Host "║  (可用 LLM_GATEWAY_HOME 环境变量覆盖)"
Write-Host "╚════════════════════════════════════════════════════════════════╝"
Write-Host ""

# ── 执行 ────────────────────────────────────────────────────────
& $Binary --dir $DefaultHome @args
exit $LASTEXITCODE
