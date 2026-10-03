<#
.SYNOPSIS
  client-deploy.ps1 — Windows 一键自动化部署编排器（PowerShell 原生）。

  串联完整部署生命周期（Linux/macOS 见 client-deploy.sh，目录约定一致）：
    阶段 1  检测并安装 Docker Desktop（winget）
    阶段 2  部署 PostgreSQL（pg17-circus，bind-mount data 目录）
    阶段 3  经 maintain ticket 下载网关镜像 tar + sha256 → 部署网关
    阶段 4  健康检查
    阶段 5  注册 schtasks：DB 备份 + 日志清理 + 版本轮转

.PARAMETER Command
  deploy（默认=全流程）| docker | db | gateway | verify | setup-tasks |
  rotate | rollback | list | record
.PARAMETER Version
  目标版本（省略=最新）
.PARAMETER DryRun
  只打印计划，不实际下载/compose
.EXAMPLE
  .\client-deploy.ps1 deploy
  .\client-deploy.ps1 deploy -Version 1.2.0
  .\client-deploy.ps1 deploy -DryRun
  .\client-deploy.ps1 rollback
#>
[CmdletBinding()]
param(
  [Parameter(Position = 0)][string]$Command = 'deploy',
  [string]$Version = $env:VERSION,
  # PS5.1 不支持 ?: 三元/空合并，用 $(if (...) {...} else {...}) 兼容。
  [string]$MaintainBase = $(if ($env:MAINTAIN_BASE) { $env:MAINTAIN_BASE } else { 'https://llmgateway.internal.example.com/maintain-api' }),
  [string]$Channel = $(if ($env:CHANNEL) { $env:CHANNEL } else { 'stable' }),
  [string]$InstallRoot = $(if ($env:INSTALL_ROOT) { $env:INSTALL_ROOT } else { 'C:\llm-gateway' }),
  [int]$GatewayPort = $(if ($env:GATEWAY_PORT) { [int]$env:GATEWAY_PORT } else { 8080 }),
  [string]$PostgresUser = $(if ($env:POSTGRES_USER) { $env:POSTGRES_USER } else { 'llm_user' }),
  [string]$PostgresDb = $(if ($env:POSTGRES_DB) { $env:POSTGRES_DB } else { 'llm_gateway' }),
  [string]$PostgresPassword = $env:POSTGRES_PASSWORD,
  [int]$HealthRetries = $(if ($env:HEALTH_RETRIES) { [int]$env:HEALTH_RETRIES } else { 30 }),
  [int]$KeepVersions = $(if ($env:KEEP_VERSIONS) { [int]$env:KEEP_VERSIONS } else { 3 }),
  [int]$KeepReleases = $(if ($env:KEEP_RELEASES) { [int]$env:KEEP_RELEASES } else { 3 }),
  # 网关运行所需密钥（首次生成并持久化 .env，后续复用，避免升级轮换导致 token 失效）。
  [string]$GatewayEnvMode = $(if ($env:GATEWAY_ENV_MODE) { $env:GATEWAY_ENV_MODE } else { 'production' }),
  [string]$GatewayApiKey = $env:GATEWAY_API_KEY,
  [string]$GatewayAdminApiKey = $env:GATEWAY_ADMIN_API_KEY,
  [string]$GatewayJwtSecret = $env:GATEWAY_JWT_SECRET,
  [string]$GatewaySecretKey = $env:GATEWAY_SECRET_KEY,
  [string]$GatewayCredEncKey = $env:GATEWAY_CRED_ENC_KEY,
  [string]$GatewayCorsOrigins = $env:GATEWAY_CORS_ORIGINS,
  [string]$OfflineBundle = $env:OFFLINE_BUNDLE,
  [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # 加速 Invoke-WebRequest

# ─── 路径约定（与 .sh 版完全对齐，drive 不同）─────────────────────────────
if (-not $InstallRoot.Contains(':')) { $InstallRoot = "C:\llm-gateway" }
$DataDir        = Join-Path $InstallRoot 'data'
$VersionsDir    = Join-Path $InstallRoot 'versions'
$ReleasesDir    = Join-Path $InstallRoot 'releases'
$EnvFile        = Join-Path $InstallRoot '.env'
$EnvBakDir      = Join-Path $InstallRoot '.env.bak'
$ComposeFile    = Join-Path $InstallRoot 'docker-compose.yml'
$VersionFile    = Join-Path $InstallRoot 'VERSION'
$Changelog      = Join-Path $InstallRoot 'CHANGELOG.log'
$LibDir         = Join-Path $InstallRoot 'scripts'
$PgContainer    = 'llm-gateway-pg'
$GatewayContainer = 'llm-gateway'

$CurrentVersion = if (Test-Path $VersionFile) { (Get-Content $VersionFile -Raw).Trim() } else { '' }
$Arch = if ($env:PROCESSOR_ARCHITECTURE -match 'ARM') { 'arm64' } else { 'amd64' }
$DockerSubDir = "linux-$Arch"
$BundleDir = ''   # 离线包根目录（Init-OfflineBundle 填充）

function Log($m)  { Write-Host "[client-deploy] $m" }
function Warn($m) { Write-Host "[client-deploy] WARN: $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[client-deploy] ERROR: $m" -ForegroundColor Red; exit 1 }
function Ts()     { (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ') }

function Append-Changelog($action, $ver, $status, $extra = '') {
  New-Item -ItemType Directory -Force -Path $InstallRoot | Out-Null
  $line = "[$(Ts)] $action ver=$ver prev=$CurrentVersion status=$status $extra"
  try { Add-Content -Path $Changelog -Value $line -ErrorAction Stop } catch { }
}

function Invoke-Priv($exe, $args) {
  # Windows 上通常以管理员运行；这里仅封装，便于将来加 UAC 提升。
  & $exe @args
}

# ─── 下载/校验（经 maintain ticket，与 .sh 同协议）─────────────────────────
function Get-CatalogSha($version, $platform, $arch) {
  try {
    $json = Invoke-RestMethod "$MaintainBase/distribution/versions?channel=$Channel" -SkipCertificateCheck
  } catch {
    try { $json = Invoke-RestMethod "$MaintainBase/downloads/catalog" -SkipCertificateCheck } catch { return '' }
  }
  $wantV = $version.TrimStart('v')
  foreach ($v in ($json.items + $json.versions)) {
    if (-not $v) { continue }
    if ($v.version.TrimStart('v') -ne $wantV) { continue }
    foreach ($a in ($v.items + $v.artifacts)) {
      if ($a.platform -eq $platform -and $a.arch -eq $arch) { return $a.sha256 }
    }
  }
  return ''
}

function Get-LatestVersion() {
  try {
    $j = Invoke-RestMethod "$MaintainBase/distribution/version-check?channel=$Channel&current=v0.0.0&platform=docker&arch=$Arch" -SkipCertificateCheck
    return $j.latest_version
  } catch { return '' }
}

# ─── 离线包（air-gapped）支持 ────────────────────────────────────────────────
function Manifest-Field($key) {
  if (-not $BundleDir -or -not (Test-Path (Join-Path $BundleDir 'MANIFEST.json'))) { return '' }
  try {
    $d = Get-Content (Join-Path $BundleDir 'MANIFEST.json') -Raw | ConvertFrom-Json
    return $d.$key
  } catch { return '' }
}

function Init-OfflineBundle {
  if (-not $OfflineBundle) { return }
  if (Test-Path $OfflineBundle -PathType Container) {
    $script:BundleDir = $OfflineBundle
  } elseif (Test-Path $OfflineBundle -PathType Leaf) {
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("bundle-" + [System.Guid]::NewGuid().ToString('N'))
    try { Expand-Archive -Path $OfflineBundle -DestinationPath $tmp -Force }
    catch { Die "OFFLINE_BUNDLE 解压失败: $OfflineBundle" }
    $m = Get-ChildItem $tmp -Recurse -Filter 'MANIFEST.json' -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $m) {
      Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
      Die "OFFLINE_BUNDLE 未含 MANIFEST.json: $OfflineBundle"
    }
    $script:BundleDir = $m.DirectoryName
    # 注：不清理 $tmp（BundleDir 指向子目录），进程退出时 OS 清理。
  } else {
    Die "OFFLINE_BUNDLE 不存在: $OfflineBundle"
  }
  if (-not (Test-Path (Join-Path $BundleDir 'MANIFEST.json'))) { Die "离线包缺少 MANIFEST.json: $BundleDir" }
  Log "离线模式: bundle=$BundleDir version=$(Manifest-Field 'version') arch=$(Manifest-Field 'arch')"
}

function Load-BundleImage($what, $tarField, $shaField, $imgField) {
  $tarName = Manifest-Field $tarField
  $sha = Manifest-Field $shaField
  $img = Manifest-Field $imgField
  if (-not $tarName) { Warn "离线包 MANIFEST 缺 $tarField"; return $null }
  # 约定：MANIFEST tar 字段包含 images/ 前缀。先完整路径，再回退 images/<basename>。
  $tar = Join-Path $BundleDir $tarName
  if (-not (Test-Path $tar)) {
    $base = Split-Path $tarName -Leaf
    $tar = Join-Path (Join-Path $BundleDir 'images') $base
  }
  if (-not (Test-Path $tar)) {
    Warn "离线包缺镜像文件 ${tarName}（尝试了 $BundleDir\$tarName 与 $BundleDir\images\$(Split-Path $tarName -Leaf)）"
    return $null
  }
  if ($sha) { if (-not (Verify-Sha256 $sha $tar)) { return $null } }
  else { Warn "离线包 $what 无 sha256 — 跳过校验" }
  Log "$what : docker load (offline) $tar"
  if (-not (docker load -i $tar)) { Warn "离线包 $what docker load 失败"; return $null }
  return $img
}

function Get-Ticket($version, $platform, $arch) {
  $body = @{ version = $version; platform = $platform; arch = $arch } | ConvertTo-Json -Compress
  $resp = Invoke-RestMethod -Method Post -Uri "$MaintainBase/downloads/ticket" -ContentType 'application/json' -Body $body -SkipCertificateCheck
  return @{ url = $resp.url; name = $resp.file_name }
}

function Verify-Sha256($expected, $file) {
  if (-not $expected) { Log "sha256 skip (no expected) $(Split-Path $file -Leaf)"; return $true }
  $actual = (Get-FileHash -Algorithm SHA256 $file).Hash.ToLower()
  if ($actual -ne $expected.ToLower()) { Warn "checksum MISMATCH $file"; return $false }
  Log "sha256 OK $(Split-Path $file -Leaf)"; return $true
}

function Fetch-AndLoad($what, $arch, $ver) {
  $t = Get-Ticket $ver 'docker' $arch
  if (-not $t.url -or -not $t.name) { Warn "$what : no published docker/$arch artifact for $ver"; return $false }
  $sha = Get-CatalogSha $ver 'docker' $arch
  if (-not $sha) { Warn "$name : 无已发布 sha256 — 拒绝加载"; return $false }
  $destDir = if ($what -eq 'db') { Join-Path $VersionsDir "$ver\docker\multi" } else { Join-Path $VersionsDir "$ver\docker\$DockerSubDir" }
  New-Item -ItemType Directory -Force -Path $destDir | Out-Null
  $tar = Join-Path $destDir $t.name
  Log "$what : downloading $($t.name)"
  try { Invoke-WebRequest -Uri $t.url -OutFile $tar -SkipCertificateCheck }
  catch { Warn "$what : download failed"; return $false }
  if (-not (Verify-Sha256 $sha $tar)) { Remove-Item $tar -Force; return $false }
  Log "$what : docker load"
  if ($LASTEXITCODE -ne 0 -or -not (docker load -i $tar)) { Warn "$what : docker load failed"; return $false }
  "$sha  $($t.name)" | Set-Content (Join-Path $destDir 'SHA256SUMS')
  return $true
}

function Docker-ImagePresent($img) { (docker image inspect $img 2>$null) -ne $null; $LASTEXITCODE -eq 0 }

# ─── 阶段 1：Docker ────────────────────────────────────────────────────────
function Test-DockerReady { (Get-Command docker -ErrorAction SilentlyContinue) -and (docker info 2>$null) }

function Stage-Docker {
  Log "阶段 1: 检测并安装 Docker (windows/$Arch)"
  if (Test-DockerReady) { Log "docker 已就绪，跳过安装"; return }
  if ($DryRun) { Log "DryRun — 将通过 winget 安装 Docker Desktop"; return }
  $winget = Get-Command winget -ErrorAction SilentlyContinue
  if ($winget) {
    Log "winget install Docker.DockerDesktop"
    winget install --id Docker.DockerDesktop --accept-source-agreements --accept-package-agreements
  } else {
    Die "未检测到 winget。请手动安装 Docker Desktop: https://www.docker.com/products/docker-desktop"
  }
  # Docker Desktop 需启动后 daemon 才就绪。
  Warn "Docker Desktop 已安装，请启动它（首次需同意许可）。等待 daemon 就绪…"
  for ($i = 0; $i -lt 60; $i++) {
    if (Test-DockerReady) { Log "docker daemon ready"; return }
    Start-Sleep -Seconds 3
  }
  Die "docker 安装后仍未就绪 — 请确认 Docker Desktop 已启动后重跑"
}

# ─── 阶段 2：PostgreSQL ─────────────────────────────────────────────────────
function Resolve-PgImage {
  $target = if ($Version) { $Version } else { $script:TargetVersion }
  $pg17 = "pg17-circus:$($target.TrimStart('v'))"
  if ($env:PG_IMAGE_OVERRIDE) { $script:PgImage = $env:PG_IMAGE_OVERRIDE; Log "db: 使用 PG_IMAGE_OVERRIDE"; return }
  $script:PgImage = $pg17
  if (Docker-ImagePresent $pg17) { Log "db: $pg17 本地已存在"; return }
  if ($BundleDir) {
    if ($DryRun) { Log "DryRun — 将从离线包加载 $pg17"; return }
    $loaded = Load-BundleImage 'db' 'pg_tar' 'pg_sha256' 'pg_image'
    if ($loaded) { if ($loaded) { $script:PgImage = $loaded }; Log "db: 已从离线包加载 $($script:PgImage)"; return }
    Warn "db: 离线包加载 $pg17 失败"
    # 尊重 ALLOW_DB_IMAGE_FALLBACK：离线 load 失败也应尝试回退。
    if ($env:ALLOW_DB_IMAGE_FALLBACK -ne '0') {
      $script:PgImage = 'postgres:17-alpine'
      Warn "db: 回退 postgres:17-alpine（无 circus/citus 侧车，仅基础 PG）"
      return
    }
    Die "db: 离线包加载失败且 ALLOW_DB_IMAGE_FALLBACK=0（检查 bundle MANIFEST/images/ 完整性）"
  }
  if (-not $DryRun -and (Fetch-AndLoad 'db' 'multi' $target)) { Log "db: 已加载 $pg17"; return }
  if ($env:ALLOW_DB_IMAGE_FALLBACK -ne '0') {
    $script:PgImage = 'postgres:17-alpine'
    Warn "db: $pg17 不可得 — 回退 postgres:17-alpine"
  } else {
    Die "db: $pg17 不可得且 ALLOW_DB_IMAGE_FALLBACK=0"
  }
}

function Wait-PgReady {
  for ($i = 0; $i -lt $HealthRetries; $i++) {
    if (docker exec $PgContainer pg_isready -U $PostgresUser 2>$null) { Log "db: pg_isready OK"; return }
    Start-Sleep -Seconds 1
  }
  Die "db: pg_isready 超时"
}

function Seed-Database {
  Log "db: 预建库 $PostgresDb（schema 由网关启动迁移）"
  if ($DryRun) { Log "DryRun — skip CREATE DATABASE"; return }
  $exists = docker exec -e PGPASSWORD=$PostgresPassword $PgContainer psql -U $PostgresUser -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='$PostgresDb'" 2>$null
  if ($exists -notmatch '1') {
    docker exec -e PGPASSWORD=$PostgresPassword $PgContainer psql -U $PostgresUser -d postgres -c "CREATE DATABASE `"$PostgresDb`";"
  }
  docker exec -e PGPASSWORD=$PostgresPassword $PgContainer psql -U $PostgresUser -d $PostgresDb -c "GRANT ALL PRIVILEGES ON DATABASE `"$PostgresDb`" TO `"$PostgresUser`";" 2>$null | Out-Null
}

function Backup-PgBeforeRebuild {
  $bakDir = Join-Path $DataDir 'backups'
  New-Item -ItemType Directory -Force -Path $bakDir | Out-Null
  $hasData = docker exec -e PGPASSWORD=$PostgresPassword $PgContainer psql -U $PostgresUser -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='$PostgresDb'" 2>$null
  if ($hasData -notmatch '1') { Log "db: 库 $PostgresDb 不存在，重建前无需备份"; return }
  $out = Join-Path $bakDir "pre-upgrade-$PostgresDb-$(Get-Date -Format yyyyMMdd-HHmmss).dump"
  Log "db: 重建前备份 → $out"
  if (docker exec -e PGPASSWORD=$PostgresPassword $PgContainer pg_dump -U $PostgresUser -d $PostgresDb -Fc -f /tmp/_preupgrade.dump 2>$null) {
    docker cp "${PgContainer}:/tmp/_preupgrade.dump" $out 2>$null
    docker exec $PgContainer rm -f /tmp/_preupgrade.dump 2>$null
    Log "db: 重建前备份完成"
  } else {
    Warn "db: 重建前 pg_dump 失败 — 继续（bind-mount 仍保留物理数据）"
  }
}

function Stage-Db {
  Log "阶段 2: 部署 PostgreSQL"
  Resolve-PgImage
  New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
  if ($DryRun) { Log "DryRun — 将以 $($script:PgImage) 启动 $PgContainer，bind-mount $DataDir"; return }
  # 幂等：已有同镜像容器在跑则跳过重建。
  $existingImg = ''
  if (docker inspect $PgContainer 2>$null) {
    $existingImg = (docker inspect --format '{{.Config.Image}}' $PgContainer 2>$null)
  }
  $running = (docker inspect -f '{{.State.Running}}' $PgContainer 2>$null)
  if ($existingImg -eq $script:PgImage -and $running -match 'true') {
    Log "db: $PgContainer 已运行同镜像（$($script:PgImage)），跳过重建"
    Wait-PgReady
    Seed-Database
    return
  }
  if ($existingImg) { Log "db: 镜像变更或容器已停，重建"; Backup-PgBeforeRebuild }
  docker rm -f $PgContainer 2>$null | Out-Null
  docker run -d --name $PgContainer --restart unless-stopped `
    -e POSTGRES_PASSWORD=$PostgresPassword `
    -e POSTGRES_USER=$PostgresUser `
    -e POSTGRES_DB=$PostgresDb `
    -v "${DataDir}:/var/lib/postgresql/data" `
    $script:PgImage | Out-Null
  if ($LASTEXITCODE -ne 0) { Die "db: 启动 PG 容器失败" }
  Wait-PgReady
  Seed-Database
}

# ─── 阶段 3：网关 ───────────────────────────────────────────────────────────
function Resolve-GatewayImage {
  $target = if ($Version) { $Version } else { $script:TargetVersion }
  $script:GatewayImageDefault = "llm-gateway-go:$($target.TrimStart('v'))-$Arch"
  if ($env:GATEWAY_IMAGE_OVERRIDE) { $script:GatewayImage = $env:GATEWAY_IMAGE_OVERRIDE; Log "gateway: 使用 override"; return }
  $script:GatewayImage = $script:GatewayImageDefault
  if (Docker-ImagePresent $script:GatewayImageDefault) { Log "gateway: $($script:GatewayImageDefault) 本地已存在"; return }
  if ($BundleDir) {
    if ($DryRun) { Log "DryRun — 将从离线包加载 $($script:GatewayImageDefault)"; return }
    $loaded = Load-BundleImage 'gateway' 'gateway_tar' 'gateway_sha256' 'gateway_image'
    if ($loaded) { if ($loaded) { $script:GatewayImage = $loaded }; Log "gateway: 已从离线包加载 $($script:GatewayImage)"; return }
    Die "gateway: 离线包加载 $($script:GatewayImageDefault) 失败（检查 bundle MANIFEST gateway_tar/gateway_sha256 字段、images/ 目录完整性）"
  }
  if ($DryRun) { Log "DryRun — 将下载 $($script:GatewayImageDefault)"; return }
  if (-not (Fetch-AndLoad 'gateway' $Arch $target)) { Die "gateway: 无法获取 $($script:GatewayImageDefault)" }
}

function Rotate-Versions {
  if (-not (Test-Path $VersionsDir)) { return }
  $dirs = Get-ChildItem $VersionsDir -Directory | Sort-Object Name
  if ($dirs.Count -le $KeepVersions) { return }
  $remove = $dirs.Count - $KeepVersions
  foreach ($d in $dirs[0..($remove - 1)]) { Remove-Item $d.FullName -Recurse -Force; Log "rotate: 删除 $($d.Name)" }
  Log "rotate: versions 保留 $KeepVersions 份"
}

function Backup-Env {
  if (Test-Path $EnvFile) {
    New-Item -ItemType Directory -Force -Path $EnvBakDir | Out-Null
    $bak = Join-Path $EnvBakDir ".env.$((Get-Date).ToString('yyyyMMddHHmmss'))"
    Copy-Item $EnvFile $bak
    Log "env: 已备份 → $bak"
  }
}

function Write-EnvFile {
  # 密码/密钥已由 Ensure-Password/Ensure-Secrets 持久化；这里只合并镜像/端口/URL 配置。
  if ($DryRun) { Log "DryRun — 将更新 $EnvFile"; return }
  # gateway 子命令可能未走 Stage-Db（PgImage 未设）；从 .env 或 override 补齐。
  if (-not $script:PgImage) { $script:PgImage = Env-Get 'DB_IMAGE' }
  if (-not $script:PgImage) {
    $t = if ($Version) { $Version } else { $script:TargetVersion }
    $script:PgImage = if ($env:PG_IMAGE_OVERRIDE) { $env:PG_IMAGE_OVERRIDE } else { "pg17-circus:$($t.TrimStart('v'))" }
  }
  Env-KvMerge 'INSTALL_ROOT' $InstallRoot
  Env-KvMerge 'GATEWAY_IMAGE' $script:GatewayImage
  Env-KvMerge 'GATEWAY_PORT' $GatewayPort
  Env-KvMerge 'DB_IMAGE' $script:PgImage
  Env-KvMerge 'POSTGRES_USER' $PostgresUser
  Env-KvMerge 'POSTGRES_DB' $PostgresDb
  Env-KvMerge 'DATABASE_URL' "postgres://$PostgresUser`:$PostgresPassword@${PgContainer}:5432/$PostgresDb`?sslmode=disable"
  Env-KvMerge 'MAINTAIN_BASE' $MaintainBase
  Log "env: 已更新 $EnvFile"
}

function Write-ComposeFile {
  if ($DryRun) { Log "DryRun — 将写入 $ComposeFile"; return }
  $yaml = @"
services:
  ${GatewayContainer}:
    image: `${GATEWAY_IMAGE}
    container_name: $GatewayContainer
    ports:
      - "`${GATEWAY_PORT}:8781"
    environment:
      - LLM_GATEWAY_ENV=`${GATEWAY_ENV_MODE}
      - LLM_GATEWAY_LISTEN=:8781
      - LLM_GATEWAY_DATABASE_URL=`${DATABASE_URL}
      - LLM_GATEWAY_API_KEY=`${GATEWAY_API_KEY}
      - LLM_GATEWAY_ADMIN_API_KEY=`${GATEWAY_ADMIN_API_KEY}
      - LLM_GATEWAY_JWT_SECRET=`${GATEWAY_JWT_SECRET}
      - LLM_GATEWAY_SECRET_KEY=`${GATEWAY_SECRET_KEY}
      - LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=`${GATEWAY_CRED_ENC_KEY}
      - LLM_GATEWAY_CORS_ORIGINS=`${GATEWAY_CORS_ORIGINS}
      - MAINTAIN_BASE=`${MAINTAIN_BASE}
    depends_on:
      - $PgContainer
    restart: unless-stopped
  ${PgContainer}:
    image: `${DB_IMAGE}
    container_name: $PgContainer
    environment:
      - POSTGRES_USER=`${POSTGRES_USER}
      - POSTGRES_PASSWORD=`${POSTGRES_PASSWORD}
      - POSTGRES_DB=`${POSTGRES_DB}
    volumes:
      - ${DataDir}:/var/lib/postgresql/data
    restart: unless-stopped
"@
  Set-Content -Path $ComposeFile -Value $yaml -Encoding UTF8
  Log "compose: 写入 $ComposeFile"
}

function Snapshot-Release([string]$SnapVer) {
  if (-not (Test-Path $VersionsDir)) { return }
  $target = if ($SnapVer) { $SnapVer } elseif ($Version) { $Version } else { $script:TargetVersion }
  $snap = Join-Path $ReleasesDir "$($target.TrimStart('v'))-$((Get-Date).ToString('yyyyMMddHHmmss'))"
  New-Item -ItemType Directory -Force -Path $ReleasesDir | Out-Null
  $srcVer = Join-Path $VersionsDir $target
  if (Test-Path $srcVer) {
    Copy-Item $srcVer $snap -Recurse -Force
    if (Test-Path $ComposeFile) { Copy-Item $ComposeFile (Join-Path $snap 'docker-compose.yml.snapshot') }
    if (Test-Path $EnvFile) { Copy-Item $EnvFile (Join-Path $snap '.env.snapshot') }
    Log "snapshot: $snap"
  }
  $snaps = Get-ChildItem $ReleasesDir -Directory | Sort-Object Name
  if ($snaps.Count -gt $KeepReleases) {
    $remove = $snaps.Count - $KeepReleases
    foreach ($s in $snaps[0..($remove - 1)]) { Remove-Item $s.FullName -Recurse -Force }
    Log "snapshot: releases 保留 $KeepReleases 份"
  }
}

function Stage-Gateway {
  Log "阶段 3: 下载并部署网关"
  Resolve-GatewayImage
  Rotate-Versions
  Backup-Env
  Write-EnvFile
  Write-ComposeFile
  Snapshot-Release
  if ($DryRun) { Log "DryRun — 跳过 compose up"; return }
  Push-Location $InstallRoot
  try {
    docker compose -f $ComposeFile up -d
    if ($LASTEXITCODE -ne 0) { Die "compose up 失败 — 见 docker compose logs" }
  } finally { Pop-Location }
  Set-Content -Path $VersionFile -Value $(if ($Version) { $Version } else { $script:TargetVersion })
  Log "gateway: 已切换到 $(if ($Version) { $Version } else { $script:TargetVersion })"
}

# ─── 阶段 4：验证（三段：healthz + DB 连通性 + API 冒烟）─────────────────────
function Dump-Logs {
  Warn "打印最近日志（compose logs --tail=80）"
  Push-Location $InstallRoot
  try { docker compose -f $ComposeFile logs --tail=80 } catch {} finally { Pop-Location }
}

function Stage-Verify {
  # 1. 网关 healthz（重试）
  Log "阶段 4.1: 健康检查 http://127.0.0.1:$GatewayPort/healthz"
  $healthOk = $false
  for ($i = 0; $i -lt $HealthRetries; $i++) {
    try { Invoke-WebRequest "http://127.0.0.1:$GatewayPort/healthz" -UseBasicParsing -SkipCertificateCheck | Out-Null; $healthOk = $true; break }
    catch { Start-Sleep -Seconds 1 }
  }
  if (-not $healthOk) { Warn "healthz FAILED"; Dump-Logs; return $false }
  Log "healthz OK"
  # 2. DB 连通性 + 网关迁移：users 表应存在
  Log "阶段 4.2: 验证 DB 连通性与网关迁移（$PostgresDb.users）"
  $pgPw = if ($PostgresPassword) { $PostgresPassword } else { Env-Get 'POSTGRES_PASSWORD' }
  $usersOk = docker exec -e PGPASSWORD=$pgPw $PgContainer psql -U $PostgresUser -d $PostgresDb -tAc "SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='users'" 2>$null
  if ($usersOk -notmatch '1') { Warn "DB users 表不存在 — 网关可能未完成迁移"; Dump-Logs; return $false }
  Log "DB 连通 OK（users 表存在）"
  # 3. API 冒烟
  Log "阶段 4.3: API 冒烟"
  try {
    $code = (Invoke-WebRequest "http://127.0.0.1:$GatewayPort/healthz" -UseBasicParsing -SkipCertificateCheck).StatusCode
    if ($code -ne 200) { Warn "API 冒烟异常（healthz code=$code）"; Dump-Logs; return $false }
  } catch { Warn "API 冒烟异常"; Dump-Logs; return $false }
  Log "阶段 4: 全部验证通过"
  return $true
}

# ─── 阶段 5：定时任务（schtasks）─────────────────────────────────────────────
function Install-Helpers {
  # 仅确保目录存在；备份/清理脚本在 Setup-Tasks 中内联生成（自包含，不依赖源码 lib/）。
  New-Item -ItemType Directory -Force -Path $LibDir | Out-Null
  Log "helpers: lib 目录 $LibDir"
}

function Setup-Tasks {
  Log "阶段 5: 注册 schtasks 定时任务"
  Install-Helpers
  if ($DryRun) { Log "DryRun — 将注册每日备份 + 每周清理 schtasks"; return }
  $backupScript = @"
`$pw = (Select-String -Path '$EnvFile' -Pattern '^POSTGRES_PASSWORD=' | Select-Object -First 1).Line.Split('=',2)[1]
docker exec -e PGPASSWORD=`$pw $PgContainer pg_dump -U $PostgresUser -d $PostgresDb -Fc -f /tmp/_b.dump
docker cp ${PgContainer}:/tmp/_b.dump '$DataDir\backups\$PostgresDb-`$(Get-Date -Format yyyyMMdd-HHmmss).dump'
docker exec $PgContainer rm -f /tmp/_b.dump
"@
  $cleanScript = @"
`$pw = (Select-String -Path '$EnvFile' -Pattern '^POSTGRES_PASSWORD=' | Select-Object -First 1).Line.Split('=',2)[1]
`$cutoff = (Get-Date).AddDays(-30).ToString('yyyy-MM-ddTHH:mm:ssZ')
docker exec -e PGPASSWORD=`$pw $PgContainer psql -U $PostgresUser -d $PostgresDb -c "DELETE FROM request_logs WHERE created_at < '`$cutoff'"
"@
  $bkPs1 = Join-Path $LibDir 'backup-db.ps1'
  $clPs1 = Join-Path $LibDir 'clean-logs.ps1'
  Set-Content $bkPs1 $backupScript -Encoding UTF8
  Set-Content $clPs1 $cleanScript -Encoding UTF8

  # 每日 03:00 备份，每周日 04:00 清理。/RU SYSTEM 静默后台运行。
  schtasks /Create /TN "llm-gateway-backup" /TR "powershell -NoProfile -ExecutionPolicy Bypass -File `"$bkPs1`"" /SC DAILY /ST 03:00 /F /RU SYSTEM 2>$null | Out-Null
  schtasks /Create /TN "llm-gateway-clean"  /TR "powershell -NoProfile -ExecutionPolicy Bypass -File `"$clPs1`"" /SC WEEKLY /D SUN /ST 04:00 /F /RU SYSTEM 2>$null | Out-Null
  Log "schtasks: 已注册 llm-gateway-backup（每日 03:00）与 llm-gateway-clean（周日 04:00）"
}

# ─── 子命令 ─────────────────────────────────────────────────────────────────
function Resolve-TargetVersion {
  if ($Version) { $script:TargetVersion = $Version }
  elseif ($BundleDir) {
    $script:TargetVersion = Manifest-Field 'version'
    if (-not $script:TargetVersion) { Die "离线包 MANIFEST 缺 version 字段" }
  }
  else {
    $script:TargetVersion = Get-LatestVersion
    if (-not $script:TargetVersion) { Die "无法获取最新版本（设 -Version 显式指定）" }
  }
  $tag = if ($BundleDir) { ' [offline]' } else { '' }
  Log "目标版本: $($script:TargetVersion)（当前: $(if($CurrentVersion){$CurrentVersion}else{'none'})）$tag"
}

function Env-KvMerge($key, $value) {
  # 把 KEY=VALUE 写入/更新 .env（存在则替换该行，不存在则追加）。单一来源、原子。
  New-Item -ItemType Directory -Force -Path $InstallRoot | Out-Null
  $line = "$key=$value"
  if (Test-Path $EnvFile) {
    $rest = Get-Content $EnvFile | Where-Object { $_ -notmatch "^$key=" }
    ($line + "`n" + ($rest -join "`n")) | Set-Content $EnvFile -Encoding UTF8
  } else {
    $line | Set-Content $EnvFile -Encoding UTF8
  }
}

function Env-Get($key) {
  if (-not (Test-Path $EnvFile)) { return '' }
  $line = Get-Content $EnvFile -ErrorAction SilentlyContinue | Where-Object { $_ -match "^$key=" } | Select-Object -First 1
  if ($line) { return ($line -split '=', 2)[1] }
  return ''
}

function Get-Token($len = 32) {
  # base64 随机 token（用于网关密钥）。
  $bytes = New-Object byte[] $len
  ([System.Security.Cryptography.RandomNumberGenerator]::Create()).GetBytes($bytes)
  return [Convert]::ToBase64String($bytes)
}

function Ensure-Password {
  # 密码必须在 Stage-Db（初始化 PG）与 Write-EnvFile 之前确定并落盘，
  # 否则 Stage-Db 用空密码初始化、Write-EnvFile 又生成新密码 → 鉴权不一致。
  if (-not $PostgresPassword) { $script:PostgresPassword = Env-Get 'POSTGRES_PASSWORD' }
  if (-not $PostgresPassword) {
    $b = New-Object byte[] 18
    ([System.Security.Cryptography.RandomNumberGenerator]::Create()).GetBytes($b)
    $script:PostgresPassword = ([Convert]::ToBase64String($b) -replace '[/+=]', '')
    $script:PostgresPassword = $script:PostgresPassword.Substring(0, [Math]::Min(24, $script:PostgresPassword.Length))
    Log "db: 已生成新 POSTGRES_PASSWORD"
  }
  Env-KvMerge 'POSTGRES_PASSWORD' $PostgresPassword
}

function Ensure-Secrets {
  # 网关密钥：首次随机生成 + 持久化 .env，后续复用（升级不轮换）。
  $generated = $false
  if (-not $GatewayApiKey) { $script:GatewayApiKey = Env-Get 'GATEWAY_API_KEY' }
  if (-not $GatewayApiKey) { $script:GatewayApiKey = "gw_$(Get-Token 24)"; $generated = $true }
  if (-not $GatewayAdminApiKey) { $script:GatewayAdminApiKey = Env-Get 'GATEWAY_ADMIN_API_KEY' }
  if (-not $GatewayAdminApiKey) { $script:GatewayAdminApiKey = "ops_$(Get-Token 32)"; $generated = $true }
  if (-not $GatewayJwtSecret) { $script:GatewayJwtSecret = Env-Get 'GATEWAY_JWT_SECRET' }
  if (-not $GatewayJwtSecret) { $script:GatewayJwtSecret = Get-Token 48; $generated = $true }
  if (-not $GatewaySecretKey) { $script:GatewaySecretKey = Env-Get 'GATEWAY_SECRET_KEY' }
  if (-not $GatewaySecretKey) { $script:GatewaySecretKey = Get-Token 32; $generated = $true }
  if (-not $GatewayCredEncKey) { $script:GatewayCredEncKey = Env-Get 'GATEWAY_CRED_ENC_KEY' }
  if (-not $GatewayCredEncKey) { $script:GatewayCredEncKey = Get-Token 32; $generated = $true }
  if (-not $GatewayCorsOrigins) { $script:GatewayCorsOrigins = "http://127.0.0.1:$GatewayPort,http://localhost:$GatewayPort" }
  if ($generated) { Log "gateway: 已生成新密钥集" } else { Log "gateway: 复用 .env 已有密钥（升级不轮换）" }
  Env-KvMerge 'GATEWAY_ENV_MODE' $GatewayEnvMode
  Env-KvMerge 'GATEWAY_API_KEY' $GatewayApiKey
  Env-KvMerge 'GATEWAY_ADMIN_API_KEY' $GatewayAdminApiKey
  Env-KvMerge 'GATEWAY_JWT_SECRET' $GatewayJwtSecret
  Env-KvMerge 'GATEWAY_SECRET_KEY' $GatewaySecretKey
  Env-KvMerge 'GATEWAY_CRED_ENC_KEY' $GatewayCredEncKey
  Env-KvMerge 'GATEWAY_CORS_ORIGINS' $GatewayCorsOrigins
}

function Invoke-Deploy {
  New-Item -ItemType Directory -Force -Path $InstallRoot | Out-Null
  Resolve-TargetVersion
  Ensure-Password
  Ensure-Secrets
  # 升级路径：预先生成回退快照，验证失败时自动回退。
  $isUpgrade = $false
  if ($CurrentVersion -and ($CurrentVersion.TrimStart('v') -ne $script:TargetVersion.TrimStart('v'))) {
    $isUpgrade = $true
    Log "升级路径: $CurrentVersion → $($script:TargetVersion)，预先生成回退快照"
    Snapshot-Release $CurrentVersion
  }
  Stage-Docker
  Stage-Db
  Stage-Gateway
  if (Stage-Verify) {
    Setup-Tasks
    Append-Changelog 'deploy' $script:TargetVersion 'ok'
    Log "✅ 部署完成: $($script:TargetVersion) @ http://127.0.0.1:$GatewayPort"
  } else {
    Append-Changelog 'deploy' $script:TargetVersion 'failed' 'verify'
    if ($isUpgrade) {
      Warn "验证失败 — 自动回退到升级前快照"
      if (Invoke-Rollback $CurrentVersion) {
        Append-Changelog 'upgrade' $script:TargetVersion 'failed' "auto_rolled_back_to_$CurrentVersion"
        Die "已自动回退到 $CurrentVersion（本次升级到 $($script:TargetVersion) 失败）"
      } else {
        Append-Changelog 'upgrade' $script:TargetVersion 'failed' 'auto_rollback_also_failed'
        Die "验证失败且回退未成功 — 请手动检查 docker compose logs 与 releases/ 快照"
      }
    } else {
      Die "首次部署验证失败 — 无可回退版本，请检查 docker compose logs"
    }
  }
}

function Invoke-Rollback {
  if (-not (Test-Path $ReleasesDir) -or -not (Get-ChildItem $ReleasesDir -ErrorAction SilentlyContinue)) { Die "无可用回退快照" }
  $want = if ($args) { $args[0] } else { $null }
  $snap = if ($want) {
    Get-ChildItem $ReleasesDir -Directory | Where-Object { $_.Name -like "$($want.TrimStart('v'))-*" } | Sort-Object Name | Select-Object -Last 1
  } else {
    Get-ChildItem $ReleasesDir -Directory | Sort-Object Name | Select-Object -Last 1
  }
  if (-not $snap) { Die "未找到匹配的回退快照" }
  Log "rollback: 从 $($snap.FullName) 恢复"
  if ($DryRun) { Log "DryRun — 将恢复 .env.snapshot + compose 并 up"; return }
  $envSnap = Join-Path $snap.FullName '.env.snapshot'
  $cmpSnap = Join-Path $snap.FullName 'docker-compose.yml.snapshot'
  if (Test-Path $envSnap) {
    if (Test-Path $EnvFile) { New-Item -ItemType Directory -Force -Path $EnvBakDir | Out-Null; Copy-Item $EnvFile (Join-Path $EnvBakDir ".env.$((Get-Date).ToString('yyyyMMddHHmmss')).prerollback") }
    Copy-Item $envSnap $EnvFile -Force
  }
  if (Test-Path $cmpSnap) { Copy-Item $cmpSnap $ComposeFile -Force }
  Push-Location $InstallRoot
  try { docker compose -f $ComposeFile up -d; if ($LASTEXITCODE -ne 0) { Die "rollback compose up 失败" } } finally { Pop-Location }
  if (Stage-Verify) { Append-Changelog 'rollback' $snap.Name 'ok'; Log "✅ 回退成功: $($snap.Name)"; return $true }
  else { Append-Changelog 'rollback' $snap.Name 'failed' 'health_check'; Die "回退后健康检查仍失败" }
}

function Invoke-List {
  Log "当前版本: $(if($CurrentVersion){$CurrentVersion}else{'none'})"
  if (Test-Path $VersionsDir) { Log "已下载版本:"; Get-ChildItem $VersionsDir -Directory | ForEach-Object { "  $($_.Name)" } }
  if (Test-Path $ReleasesDir) { Log "回退快照:";   Get-ChildItem $ReleasesDir -Directory | ForEach-Object { "  $($_.Name)" } }
  if (Test-Path $Changelog)   { Log "最近变更:";   Get-Content $Changelog -Tail 10 | ForEach-Object { "  $_" } }
}

# ─── 入口 ───────────────────────────────────────────────────────────────────
Init-OfflineBundle
switch ($Command.ToLower()) {
  'deploy' { Invoke-Deploy }
  'db'     { Resolve-TargetVersion; Ensure-Password; Stage-Db }
  'gateway'{ Resolve-TargetVersion; Ensure-Password; Ensure-Secrets; Stage-Gateway; if (-not (Stage-Verify)) { Die 'health 失败' } }
  'verify' { if (-not (Stage-Verify)) { exit 1 } }
  'docker' { Stage-Docker }
  'setup-tasks' { Setup-Tasks }
  'rotate' { Rotate-Versions; Log 'rotate 完成' }
  'rollback' { Invoke-Rollback @args }
  'list'   { Invoke-List }
  default  { Die "unknown command: $Command (deploy|docker|db|gateway|verify|setup-tasks|rotate|rollback|list)" }
}
