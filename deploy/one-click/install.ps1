#Requires -Version 5.1
<#
.SYNOPSIS
  LLM Gateway 一键部署（Windows — PostgreSQL + Redis + Gateway via Docker）

.DESCRIPTION
  下载或本地使用 llm-gw-installer，通过 Docker Compose 部署全栈。

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File deploy\one-click\install.ps1
  powershell -ExecutionPolicy Bypass -File deploy\one-click\install.ps1 -NonInteractive
  powershell -ExecutionPolicy Bypass -File deploy\one-click\install.ps1 -DownloadVersion v2.4.6
#>
param(
  [switch]$NonInteractive,
  [string]$DownloadVersion = "",
  [string]$InstallDir = $(if ($env:LLM_GATEWAY_HOME) { $env:LLM_GATEWAY_HOME } else { "$env:USERPROFILE\llm-gateway" })
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$Arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { throw "64-bit Windows required" }

function Find-Installer([string]$Root) {
  $names = @(
    "llm-gw-installer-windows-$Arch.exe",
    "bin\llm-gw-installer.exe",
    "llm-gw-installer.exe"
  )
  foreach ($n in $names) {
    $p = Join-Path $Root $n
    if (Test-Path $p) { return $p }
  }
  if (Get-Command go -ErrorAction SilentlyContinue) {
    $out = Join-Path $env:TEMP "llm-gw-installer.exe"
    Write-Host "[one-click] building installer..."
    Push-Location $RepoRoot
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = $Arch
    go build -trimpath -ldflags="-s -w" -o $out ./installer/cmd/llm-gw-installer
    Pop-Location
    return $out
  }
  throw "llm-gw-installer not found; use offline package or install Go"
}

$WorkDir = $RepoRoot
if ($DownloadVersion) {
  $ver = $DownloadVersion.TrimStart("v")
  $base = if ($env:DOWNLOAD_BASE_URL) { $env:DOWNLOAD_BASE_URL } else { "https://download.kxpms.cn/llm-gateway-go" }
  $file = "llm-gateway-go-$ver-windows-$Arch-offline.zip"
  $url = "$base/v$ver/$file"
  $dl = Join-Path $env:TEMP "llm-gw-dl"
  New-Item -ItemType Directory -Force -Path $dl | Out-Null
  Write-Host "[one-click] downloading $url"
  Invoke-WebRequest -Uri $url -OutFile (Join-Path $dl $file)
  Expand-Archive -Path (Join-Path $dl $file) -DestinationPath $dl -Force
  $pkg = Get-ChildItem $dl -Directory -Filter "llm-gateway-go-*" | Select-Object -First 1
  if ($pkg) { $WorkDir = $pkg.FullName }
}

$Installer = Find-Installer $WorkDir
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$args = @("install", "--dir", $InstallDir)
if ($NonInteractive) { $args += "--skip-prompt" }

Write-Host "=== LLM Gateway one-click deploy ==="
Write-Host "Install dir: $InstallDir"
Write-Host "Installer:   $Installer"
& $Installer @args
