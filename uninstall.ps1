# uninstall.ps1 — 卸载 llm-gateway-go（保留数据）
# 用法: .\uninstall.ps1 [-Purge]

param(
    [switch]$Purge
)

$ErrorActionPreference = "Stop"

$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $SCRIPT_DIR

if (-not (Test-Path "compose.yml")) {
    Write-Host "❌ 当前目录未发现 compose.yml，请 cd 到安装目录" -ForegroundColor Red
    exit 1
}

Write-Host ""
Write-Host "═══ 卸载 llm-gateway-go ═══"
Write-Host ""

if (Get-Command docker -ErrorAction SilentlyContinue) {
    Write-Host "▶ 停止 Docker 容器 ..."
    docker compose down 2>$null
} else {
    Write-Host "⚠️  docker 未安装，跳过容器停止" -ForegroundColor Yellow
}

if ($Purge) {
    Write-Host ""
    Write-Host "⚠️  -Purge 模式将删除所有持久化数据（数据库、Redis、附件、日志、.env）" -ForegroundColor Yellow
    $confirm = Read-Host "确认彻底清理? (yes/no)"
    if ($confirm -ne "yes") {
        Write-Host "已取消"
        exit 1
    }
    Write-Host "▶ 删除数据卷 ..."
    docker compose down -v 2>$null
    Write-Host "▶ 删除所有持久化目录 ..."
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue db, redis, attachments, app, backups, config
    Write-Host "▶ 删除配置文件 ..."
    Remove-Item -Force -ErrorAction SilentlyContinue .env, compose.yml, README.md
    Write-Host ""
    Write-Host "✅ 已彻底清理（仅保留 installer 和卸载脚本）" -ForegroundColor Green
} else {
    Write-Host ""
    Write-Host "✅ 已停止容器（数据保留在 db/data、redis/data、attachments、app/logs）" -ForegroundColor Green
    Write-Host "   如需彻底清理数据: .\uninstall.ps1 -Purge"
}
