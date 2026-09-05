# SOURCE_OF_TRUTH: ~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/deploy/windows/install-service.ps1
# SYNC_POLICY: 修改本文件时同步改 maintain 对应位置。service identity (binary / config / paths) 已 sed 适配 llm-gateway-go。

[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][ValidateSet('install','uninstall')][string]$Action,
  [string]$ReleaseDir = (Join-Path $PSScriptRoot '..\..'),
  [string]$InstallRoot = "$env:ProgramFiles\LLM-Gateway-Go",
  [string]$ConfigFile = "$env:ProgramData\LLM-Gateway-Go\gateway.env",
  [switch]$Purge,
  [switch]$ConfirmAction
)
$ErrorActionPreference = 'Stop'
$ServiceName = 'LLM-Gateway-Go'
$DataRoot = "$env:ProgramData\LLM-Gateway-Go"
$LogRoot = Join-Path $DataRoot 'logs'

function Require-Admin {
  $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
  if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Administrator privileges are required.' }
}
function Confirm-Destructive([string]$Token) {
  if (-not $ConfirmAction) { throw "Pass -ConfirmAction to authorize $Token." }
}
function Get-Version([string]$Dir) {
  $v = Get-Content (Join-Path $Dir 'version.json') -Raw | ConvertFrom-Json
  if (-not $v.version) { throw 'version.json has no version.' }
  return [string]$v.version
}
function Set-EnvFile([string]$Path) {
  if (-not (Test-Path $Path)) {
    New-Item -ItemType Directory -Force (Split-Path $Path) | Out-Null
    "MAINTAIN_ENV=production`r`nMAINTAIN_LISTEN_ADDR=127.0.0.1:8082`r`nMAINTAIN_DATABASE_URL=`r`n" | Set-Content -Encoding UTF8 $Path
    Write-Warning "Created config template at $Path; set secrets before starting."
  }
  $acl = Get-Acl $Path
  $acl.SetAccessRuleProtection($true, $false)
  Set-Acl $Path $acl
}
function Install-Service {
  $version = Get-Version $ReleaseDir
  $destination = Join-Path $InstallRoot "releases\$version"
  $arch = if ([Environment]::Is64BitOperatingSystem -and $env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
  $binary = Join-Path $ReleaseDir "bin\maintain-windows-$arch.exe"
  if (-not (Test-Path $binary)) { throw "Missing $binary" }
  New-Item -ItemType Directory -Force $destination, $LogRoot | Out-Null
  Copy-Item "$ReleaseDir\*" $destination -Recurse -Force
  Copy-Item $binary (Join-Path $destination 'bin\gateway.exe') -Force
  Set-EnvFile $ConfigFile
  $current = Join-Path $InstallRoot 'current'
  if (Test-Path $current) { Remove-Item $current -Force -Recurse }
  New-Item -ItemType Junction -Path $current -Target $destination | Out-Null
  $runner = Join-Path $current 'bin\gateway.exe'
  $sc = Get-Command sc.exe -ErrorAction SilentlyContinue
  if ($sc) {
    & $sc.Source stop $ServiceName 2>$null | Out-Null
    & $sc.Source delete $ServiceName 2>$null | Out-Null
    & $sc.Source create $ServiceName binPath= "`"$runner`"" start= auto DisplayName= "AI Native Maintain" | Out-Null
    & $sc.Source failure $ServiceName reset= 86400 actions= restart/5000 | Out-Null
    & $sc.Source start $ServiceName | Out-Null
  } else {
    $nssm = Get-Command nssm.exe -ErrorAction SilentlyContinue
    if (-not $nssm) { throw 'Neither sc.exe nor nssm.exe is available.' }
    & $nssm.Source install $ServiceName $runner | Out-Null
    & $nssm.Source set $ServiceName AppDirectory (Join-Path $current 'bin') | Out-Null
    & $nssm.Source set $ServiceName AppStdout (Join-Path $LogRoot 'maintain.log') | Out-Null
    & $nssm.Source set $ServiceName AppStderr (Join-Path $LogRoot 'maintain.error.log') | Out-Null
    & $nssm.Source start $ServiceName | Out-Null
  }
  Write-Host "Installed $version. Config: $ConfigFile; logs: $LogRoot"
}
function Uninstall-Service {
  if ($Purge) { Confirm-Destructive 'PURGE-ALL-DATA' } else { Confirm-Destructive 'UNINSTALL-SERVICE' }
  $sc = Get-Command sc.exe -ErrorAction SilentlyContinue
  if ($sc) { & $sc.Source stop $ServiceName 2>$null | Out-Null; & $sc.Source delete $ServiceName 2>$null | Out-Null }
  $nssm = Get-Command nssm.exe -ErrorAction SilentlyContinue
  if ($nssm) { & $nssm.Source stop $ServiceName 2>$null | Out-Null; & $nssm.Source remove $ServiceName confirm 2>$null | Out-Null }
  if ($Purge) {
    Remove-Item $InstallRoot, $DataRoot -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host 'Service and local data purged; external PostgreSQL data was not touched.'
  } else { Write-Host "Service removed; retained $InstallRoot and $DataRoot." }
}

Require-Admin
if ($Action -eq 'install') { Install-Service } else { Uninstall-Service }
