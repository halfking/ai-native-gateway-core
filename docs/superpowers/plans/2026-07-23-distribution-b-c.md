# 分发 B+C 实施计划

> Spec: `docs/superpowers/specs/2026-07-23-distribution-b-c-design.md`
> Scope: **B 文件分发（Cloudreve v4 适配）+ C 版本管理 API**（A/D/E 后续 spec）
> 仓库分布：主力改动 `ai-native-maintain`；`llm-gateway-go` 仅 installer 一处对接
> 数据库：maintain 仓 `maintain` schema，已在 252 pg17 上运行（保持兼容；只做加列式迁移）

---

## 阶段 0 / 准备（一次性）

### 任务 0.1 — 把 plan 同步到仓库根 README 索引

**文件**：
- `ai-native-maintain/README.md`（在「Documentation」段追加）：
  ```markdown
  - [Distribution](docs/distribution/storage-provider.md) — Cloudreve v4 + version mgmt
  ```
- `llm-gateway-go/README.md`（追加指向 spec 的 link）

**验收**：`grep -l distribution/README.md ai-native-maintain/README.md llm-gateway-go/README.md` 两个 README 均含字符串

---

## 阶段 1 / 基础（Storage 抽象 + Cloudreve 适配器）

### 任务 1.1 — 扩展 `maintain.release_artifacts` 列（加 storage_uri 等）

**新建**：`ai-native-maintain/sql/migrations/015_distribution_storage.sql`
```sql
ALTER TABLE maintain.release_artifacts
    ADD COLUMN storage_uri          TEXT,
    ADD COLUMN last_verified_at     TIMESTAMPTZ,
    ADD COLUMN last_verified_ok     BOOLEAN,
    ADD COLUMN upload_state         TEXT NOT NULL DEFAULT 'local'
        CHECK (upload_state IN ('local','uploading','cloudreve','failed','orphan'));
-- local      : 仍存于 cfg.ArtifactRoot（旧默认）
-- uploading  : 后台正在上传到 Cloudreve
-- cloudreve  : 已成功上传到 Cloudreve（download 流转发或重定向）
-- failed     : 上传 Cloudreve 失败，文件已保留在 local
-- orphan     : DB 行存在但本地/Cloudreve 都查不到（Head 不通）

UPDATE maintain.release_artifacts
   SET storage_uri = 'file://' || COALESCE(download_path, '')
 WHERE storage_uri IS NULL;
```

**文件**：
- `ai-native-maintain/sql/migrations/015_distribution_storage.sql`（新增）
- `ai-native-maintain/internal/store/postgres.go`（在 `schemaDDL` 之外，**新增 `applyMigration015`**，迁移驱动由现有 `applyMigrations` 调用）

**验收**：`psql -h 252 -U maintain -d maintain -f 015_distribution_storage.sql` 成功；DESCRIBE 表 4 列已加

### 任务 1.2 — 扩展 `maintain.releases` 加 `status`

**追加到**：`ai-native-maintain/sql/migrations/015_distribution_storage.sql`（同一文件追加）：
```sql
ALTER TABLE maintain.releases
    ADD COLUMN status   TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','released','archived')),
    ADD COLUMN archived_at TIMESTAMPTZ;

-- 历史数据：published_at IS NOT NULL 视为 released
UPDATE maintain.releases
   SET status = 'released'
 WHERE published_at IS NOT NULL;

CREATE INDEX maintain_releases_status_channel_idx
    ON maintain.releases (status, channel);
```

**验收**：DESCRIBE `maintain.releases` 多 `status` 列；旧 published 行 status='released'

### 任务 1.3 — 扩展 `Artifact` / `ReleaseMeta` Go 结构体

**文件**：`ai-native-maintain/internal/distribution/types.go`
**现状**：已有 `Artifact`（12 字段）、`ReleaseMeta`（14 字段）
**改动**：
```go
// 在 Artifact 末尾加：
    StorageURI      *string    `json:"storage_uri,omitempty"`
    UploadState     string     `json:"upload_state"`           // 默认 "local"
    LastVerifiedAt  *time.Time `json:"last_verified_at,omitempty"`
    LastVerifiedOK  *bool      `json:"last_verified_ok,omitempty"`
// 在 ReleaseMeta 末尾加：
    Status          string     `json:"status"`                 // draft|released|archived
    ArchivedAt      *time.Time `json:"archived_at,omitempty"`
    NotesURL        string     `json:"notes_url,omitempty"`
```

**验收**：`go build ./...` 通过

### 任务 1.4 — 新建 `storage.Provider` 接口包

**新建**：`ai-native-maintain/internal/distribution/storage/provider.go`
```go
package storage

import (
    "context"
    "errors"
    "io"
    "time"
)

type Artifact struct {
    URI       string    // "file:///.../v1.14.0.tar.gz" 或 "cloudreve://my/release/llm-gateway-go/v1.14.0/...tar.gz"
    Size      int64
    SHA256    string
    ETag      string
    UpdatedAt time.Time
}

type UploadOptions struct {
    Source          io.Reader
    Size            int64
    ExpectedSHA256  string
    Overwrite       bool
}

type Provider interface {
    Name() string                                                          // "local-fs" / "cloudreve"
    EnsurePath(ctx context.Context, dirURI string) (string, error)
    Upload(ctx context.Context, targetURI string, opts UploadOptions) (*Artifact, error)
    Head(ctx context.Context, uri string) (*Artifact, error)
    Delete(ctx context.Context, uri string) error
    Open(ctx context.Context, uri string) (io.ReadCloser, string, error) // (body, content-type, err)
    SignedURL(ctx context.Context, uri string, ttl time.Duration) (string, time.Time, error)
    List(ctx context.Context, dirURI string) ([]Listing, error)
}

type Listing struct {
    URI       string
    Name      string
    Size      int64
    SHA256    string
    UpdatedAt time.Time
    IsDir     bool
}

var (
    ErrNotFound    = errors.New("storage: not found")
    ErrConflict    = errors.New("storage: already exists")
    ErrTooLarge    = errors.New("storage: payload too large")
    ErrUnsupported = errors.New("storage: unsupported operation")
)
```

**新建**：`ai-native-maintain/internal/distribution/storage/provider_test.go`
- 接口契约测试：fake provider 必须实现全部方法签名（编译即验）

**验收**：`go test ./internal/distribution/storage/...` 通过

### 任务 1.5 — 新建 `LocalFSAdapter`（封装现有 `cfg.ArtifactRoot` 行为）

**新建**：`ai-native-maintain/internal/distribution/storage/local_fs.go`
```go
package storage

// LocalFSAdapter 保留旧 /artifacts/{ver}/{file} 行为，向后兼容 154/245/252 上现有部署
type LocalFSAdapter struct {
    Root string
}

func NewLocalFS(root string) *LocalFSAdapter { return &LocalFSAdapter{Root: root} }

func (l *LocalFSAdapter) Name() string { return "local-fs" }

func (l *LocalFSAdapter) EnsurePath(ctx context.Context, dirURI string) (string, error) {
    p := strings.TrimPrefix(dirURI, "file://")
    if err := os.MkdirAll(p, 0o755); err != nil {
        return "", err
    }
    return "file://" + p, nil
}

func (l *LocalFSAdapter) Upload(ctx context.Context, targetURI string, opts UploadOptions) (*Artifact, error) {
    // 1) target = file://{root}/{rel}
    // 2) 校验目标父目录存在
    // 3) 写临时 .part + tee 到 SHA256 hasher
    // 4) 校验 ExpectedSHA256（如给）
    // 5) os.Rename 到正式路径
    // 6) 返回 Artifact{URI=file://..., Size, SHA256, ETag=hex(SHA256), UpdatedAt=now}
}

func (l *LocalFSAdapter) Head(ctx, uri) (*Artifact, error) { /* os.Stat + 重算 SHA256（缓存可在后续加） */ }
func (l *LocalFSAdapter) Delete(ctx, uri) error             { /* os.Remove */ }
func (l *LocalFSAdapter) Open(ctx, uri) (io.ReadCloser, string, error) {
    // file://... → os.Open + http.DetectContentType
}
func (l *LocalFSAdapter) SignedURL(ctx, uri, ttl) (string, time.Time, error) {
    // 直接返回本地直链 + 过期时间 = now+ttl（依赖 HTTP handler 校验 ticket；这是与现 file_handler 兼容的关键）
}
func (l *LocalFSAdapter) List(ctx, dirURI) ([]Listing, error) {
    // filepath.Walk 顶层 → 转 Listing
}
```

**单测**：`ai-native-maintain/internal/distribution/storage/local_fs_test.go`
- 上传 + 读回字节相等
- 重命名后 .part 已清理
- Head 不存在 → ErrNotFound
- Delete 不存在 → nil（幂等）

**验收**：`go test ./internal/distribution/storage/ -run TestLocalFS` 通过；端到端 round-trip OK

### 任务 1.6 — 新建 `CloudreveAdapter`（v4 REST + JWT 缓存）

**新建**：`ai-native-maintain/internal/distribution/storage/cloudreve.go`
```go
package storage

type CloudreveConfig struct {
    BaseURL    string // https://files.kxpms.cn
    Username   string // ci-upload@kxpms.cn
    Password   string // SOPS 解出
    RootURI    string // cloudreve://my/release/llm-gateway-go
    HTTPClient *http.Client
    Logger     *slog.Logger
}

type CloudreveAdapter struct {
    cfg CloudreveConfig
    mu  sync.Mutex
    accessToken  string
    refreshToken string
    expiresAt    time.Time
}

func NewCloudreve(cfg CloudreveConfig) *CloudreveAdapter { /* ... */ }
func (c *CloudreveAdapter) Name() string { return "cloudreve" }

// 内部方法：
func (c *CloudreveAdapter) ensureAuth(ctx context.Context) error {
    // 缓存 token 未过期 → 直接返
    // 否则 POST /api/v4/session/token {email,password} → 缓存 access+refresh + expires
    // 若返回 401 → 强制 refresh（access 已被服务端失效），refresh 失败 → 重新登录
}
```

**实现要点（对照 live 探测结果）**：
- 登录：`POST /api/v4/session/token` JSON `{email, password}` → `{code:0, data:{token: {access_token, refresh_token, expires_at}}}`
- 鉴权头：`Authorization: Bearer <access_token>`（从 live curl 验证）
- `EnsurePath(uri)`：`POST /api/v4/file/create` JSON `{uri, type:"folder"}`；409 → 幂等返成功
- `Upload`：
  1. `POST /api/v4/upload` `{uri, size, mime}` → 拿 `session_id, upload_url`
  2. `PUT <upload_url>` 流转发（chunked transfer-encoding；由 http.Client 自动）
  3. `POST /api/v4/upload/{session_id}/complete` JSON `{uri, sha256, parts:[]}` → 拿 `id, parent`
  4. 失败：`DELETE /api/v4/upload/{session_id}` 清理 + 返回
- `Head(uri)`：`GET /api/v4/file?uri={uri}` 取 `parent.metadata.size + sha1`（**注意 Cloudreve 用 sha1 不是 sha256**；转 sha256 需上传时也携带 sha256 → 优先信 upload 时算的 sha256；Head 只用于「存在性 + size」二次校验）
- `Open(uri)`：先尝试 `Open` 走 Cloudreve 流（`GET /api/v4/file/content/{id}`），再 `Bearer + Range` 支持分段；若 401 → refresh + retry
- `SignedURL`：依赖 Cloudreve 文件自身 capability 暴露 Public download（用户在 Cloudreve 后台勾选文件「允许下载」），URL 形式 `https://files.kxpms.cn/d/<file_id>/<encoded-name>`。不签。返回时 `expires_at = now+ttl` 仅用于日志。
- `Delete(uri)`：`DELETE /api/v4/file` JSON `{uris:[uri]}`
- `List(dirURI)`：`GET /api/v4/file?uri={dirURI}` 取 `files[]`

**单测**：`ai-native-maintain/internal/distribution/storage/cloudreve_test.go`
- 用 `httptest.NewServer` 起一个伪 Cloudreve v4 端（按真实路由形态手写）
- 覆盖：login/refresh/EnsurePath幂等/Upload-成功/Upload-401-refresh/Upload-500-retry/Head-404/Head-200/Delete/List
- 不依赖 live 252

**验收**：`go test ./internal/distribution/storage/ -run TestCloudreve` 100% 通过；live 烟测（任务 3.4 一并做）

### 任务 1.7 — 注册 Provider 与配置接入

**修改**：`ai-native-maintain/internal/config/config.go`
```go
// 在 cfg 已有 ArtifactRoot 字段后追加：
type Config struct {
    // ...
    Storage StorageConfig
}
type StorageConfig struct {
    Provider   string            // "local-fs" | "cloudreve"  默认 "local-fs"
    LocalRoot  string            // 兼容老配置 = ArtifactRoot
    Cloudreve  CloudreveSettings // 仅 Provider=cloudreve 用
}
type CloudreveSettings struct {
    BaseURL    string `yaml:"base_url"`
    Username   string `yaml:"username"`
    Password   string `yaml:"password"`
    RootURI    string `yaml:"root_uri"`
}
```

**修改**：`ai-native-maintain/cmd/maintain/main.go`（或 equivalent 入口）
- 启动时构造 provider：`cfg.Storage.Provider == "cloudreve" ? storage.NewCloudreve(...) : storage.NewLocalFS(cfg.ArtifactRoot)`
- 注入到 `httpapi.NewServer` 构造器

**单测**：`ai-native-maintain/internal/config/config_test.go`
- 默认 = local-fs
- 显式 cloudreve 时配置正确解析（yaml fixture）

**验收**：`go build ./...` 通过；`go test ./internal/config` 通过

### 任务 1.8 — 把现有 `Artifact` 类型与 `storage.Artifact` 桥接

**修改**：`ai-native-maintain/internal/httpapi/upload.go`（已有，约 280 行）
- 把 `UpsertArtifact` 写 DB 时新增 `StorageURI`、`UploadState` 两个列写入
- 现有签名 `(releaseID, platform, arch, edition, label, artifactName, sha256, sizeBytes, downloadPath)` 末尾加 `storageURI, uploadState` 两参数
- 对应更新所有 caller：`PublishAPI.UpsertArtifact`、`upload.go` handler

**修改**：`ai-native-maintain/internal/store/postgres.go`
- `UpsertArtifact` SQL：`INSERT ... VALUES (..., $9, $10, 'local')` + 第十一列 `storage_uri`, 十二列 `upload_state`
- `memory_repository.go` 同名方法同步加字段

**验收**：`go build ./...` 通过；现有 catalog 单测照常通过

---

## 阶段 2 / HTTP 端点（缺失的 CRUD + 跨存储重写）

### 任务 2.1 — `Repository` 接口扩展

**修改**：`ai-native-maintain/internal/store/repository.go`
```go
// 新增：
DeleteArtifact(ctx context.Context, version string, platform, arch string) error
SoftDeleteRelease(ctx context.Context, version string, archivedBy string) error
UpdateRelease(ctx context.Context, version string, patch ReleasePatch) error
VersionCheck(ctx context.Context, channel, current string, platform, arch string) (*VersionCheckResult, error)
ListArtifactsWithLive(ctx context.Context, version string) ([]LiveArtifact, error)

// ReleasePatch 字段：
type ReleasePatch struct {
    Description *string
    Changelog   *string
    ImageTag    *string
    MinVersion  *string
    Mandatory   *bool
}

// VersionCheckResult:
type VersionCheckResult struct {
    CurrentVersion    string
    LatestVersion     string
    UpdateAvailable   bool
    Mandatory         bool
    MinRequired       string
    TargetRelease     *ReleaseMeta
    TargetArtifacts   []Artifact
    Channel           string
    NotesURL          string
}

// LiveArtifact = Artifact + (StorageURI 来自 DB，Live 来自 Head 调用)
```

**修改**：`ai-native-maintain/internal/store/postgres.go` — 全部实现（带 tx）
**修改**：`ai-native-maintain/internal/store/memory_repository.go` — 同步实现（用于 e2e）

**验收**：`go test ./internal/store/...` 100% 通过

### 任务 2.2 — 重构 `upload.go` 使用 storage.Provider

**修改**：`ai-native-maintain/internal/httpapi/upload.go`
- handler 接受 `(provider storage.Provider, repo store.Repository)`
- 流转发：
  1. tee to SHA256 hasher
  2. `provider.Upload(ctx, targetURI, storage.UploadOptions{Source: body, Size: req.ContentLength, ExpectedSHA256: ""})` → 拿 `storage.Artifact`
  3. 计算本地 SHA256 比对 provider 返回 SHA256（Cloudreve 上传时携带）
  4. repo.UpsertArtifact(..., storageURI=provider.Artifact.URI, uploadState=strings.ToLower(provider.Name()))
  5. err 时 fallback：若 provider 是 cloudreve 且失败，回写 local-fs（保留旧行为），upload_state='failed'

**验收**：
- `go test ./internal/httpapi/...` 通过
- 新增 `upload_test.go` case：local-fs 流成功；fake cloudreve 流成功；fake cloudreve 失败回退 local-fs 且 upload_state='failed'

### 任务 2.3 — 重构 `file_handler.go` 使用 storage.Provider

**修改**：`ai-native-maintain/internal/httpapi/file_handler.go`
- `ServeArtifact(w, r)` 不再 `cfg.ArtifactRoot` 拼接，转调：
  ```go
  a, err := repo.GetArtifactByVersionPlatform(r.Context(), ver, platform, arch)
  if a.UploadState == "cloudreve" {
      body, ct, err := provider.Open(ctx, a.StorageURI)
      http.ServeContent(w, r, a.ArtifactName, a.UpdatedAt, body)
      return
  }
  // local：原行为
  ```
- `downloads/catalog` 中 `Artifacts[].DownloadURL` 改为 `provider.SignedURL(...)` 返回（local 模式给本地直链；cloudreve 模式给 Cloudreve 公链）

**验收**：本地模式路由走通；cloudreve 模式假服务器走通

### 任务 2.4 — 新建 `distribution_admin.go`（缺失的 admin endpoint）

**新建**：`ai-native-maintain/internal/httpapi/distribution_admin.go`
```go
// PUT /admin/releases/{version}
func (s *Server) AdminUpdateRelease(w, r)
// body: ReleasePatch JSON；只允许 draft 状态更新

// DELETE /admin/releases/{version}
func (s *Server) AdminDeleteRelease(w, r)
// 仅 draft 或 archived 可删；soft-delete（status='archived', archived_at=now）；若已 uploaded artifacts → 删 Cloudreve（不强制，best-effort）

// DELETE /admin/releases/{version}/artifacts/{platform}/{arch}
func (s *Server) AdminDeleteReleaseArtifact(w, r)
// 删 storage（Provider.Delete）+ 删 DB 行；tx 顺序：先 DB 软标记 → 后 storage 删

// GET /admin/releases/{version}/artifacts?live=true
func (s *Server) AdminListReleaseArtifacts(w, r)
// 默认带 live check（provider.Head）；设置 ?live=false 跳过
```

**修改**：`ai-native-maintain/internal/httpapi/server.go`
- 在 `mux` 注册：
  ```go
  mux.HandleFunc("PUT /maintain-api/admin/releases/{version}",                  s.AdminUpdateRelease)
  mux.HandleFunc("DELETE /maintain-api/admin/releases/{version}",                s.AdminDeleteRelease)
  mux.HandleFunc("DELETE /maintain-api/admin/releases/{version}/artifacts/{platform}/{arch}", s.AdminDeleteReleaseArtifact)
  mux.HandleFunc("GET /maintain-api/admin/releases/{version}/artifacts",         s.AdminListReleaseArtifacts)
  ```

**验收**：`go test ./internal/httpapi/ -run TestAdminDistribution` 通过；fake Cloudreve + memory repo 跑通

### 任务 2.5 — 新建 `/upgrade/check` 端点（spec §2.1）

**新建**：`ai-native-maintain/internal/httpapi/distribution_public.go`
```go
// GET /maintain-api/upgrade/check?channel=stable&current=v1.13.0&arch=amd64&platform=linux
func (s *Server) PublicUpgradeCheck(w, r)
// 委派 repo.VersionCheck；空 current 时返回 latest 但 UpdateAvailable=false
// 返回 JSON: VersionCheckResult
```

**修改**：`ai-native-maintain/internal/httpapi/server.go` 注册

**验收**：`curl http://localhost:9090/maintain-api/upgrade/check?channel=stable&current=v1.13.0` 返 200 + JSON

### 任务 2.6 — `LicenseAdminAPI` 中集成 Cloudreve 凭据校验（确保 admin token 可用）

**修改**：`ai-native-maintain/internal/httpapi/license_admin.go`
- 启动时若 `cfg.Storage.Provider == "cloudreve"`，在 `NewServer` 后台异步跑一次 `provider.EnsurePath(cfg.Storage.Cloudreve.RootURI)`；失败 → 日志 ERROR 但不阻止启动（admin 后续操作会显式失败）

**验收**：`/admin/releases/{v}/artifacts` 在 cloudreve 配置下可成功上传到 `cloudreve://my/release/llm-gateway-go/test/foo.tgz`

---

## 阶段 3 / E2E + 跨仓对接 + 文档

### 任务 3.1 — fake Cloudreve v4 服务端（e2e 共享）

**新建**：`ai-native-maintain/internal/distribution/storage/cloudreve_fake.go`（build tag `e2e`）
```go
//go:build e2e

// 模拟 Cloudreve v4 路由：
// POST /api/v4/site/ping -> {"code":0,"data":"4.15.0-fake"}
// POST /api/v4/session/token -> 返 access_token="fake-token"
// GET  /api/v4/file?uri=... -> 返预置 JSON
// POST /api/v4/file/create -> 409 已存在 -> 200 OK
// POST /api/v4/upload -> 返 session_id + upload_url（指向同 server）
// PUT  /upload/{session_id} -> 收字节（写到临时文件）
// POST /api/v4/upload/{session_id}/complete -> 返 id, parent
// DELETE /api/v4/file -> 200 OK
```

**新建**：`ai-native-maintain/internal/distribution/storage/cloudreve_fake_test.go`
- 启动 fake、跑 CloudreveAdapter 全方法 → 验证 round-trip

**验收**：`go test -tags=e2e ./internal/distribution/storage/...` 通过

### 任务 3.2 — E2E 套件：完整 catalog 流程

**新建**：`ai-native-maintain/e2e/distribution_test.go`（build tag `e2e`）
```go
//go:build e2e

func TestE2E_Distribution_FullCycle(t *testing.T) {
    // 1. 起 fake Cloudreve
    // 2. 起 dockertest postgres + 跑 migrations 001..015
    // 3. 起 maintain server (httptest.NewServer)
    // 4. POST /admin/releases v=v9.9.9-e2e build_seq=99999
    // 5. POST /admin/releases/v9.9.9-e2e/artifacts  (linux/amd64, 2MB 随机流)
    // 6. GET /admin/releases/v9.9.9-e2e/artifacts?live=true -> upload_state=cloudreve
    // 7. POST /admin/releases/v9.9.9-e2e/publish
    // 8. GET /maintain-api/downloads/catalog -> 含 v9.9.9-e2e + artifact DownloadURL
    // 9. GET /maintain-api/upgrade/check?current=v9.0.0 -> update_available=true target=v9.9.9-e2e
    // 10. GET 那个 DownloadURL -> 200 + bytes 完全相同
    // 11. DELETE /admin/releases/v9.9.9-e2e/artifacts/linux/amd64 -> fake Cloudreve 收到 DELETE
    // 12. DELETE /admin/releases/v9.9.9-e2e -> status=archived
}
```

**验收**：`go test -tags=e2e ./e2e/...` 全 12 步通过

### 任务 3.3 — CLI `cloudreve-uploader`（live ops 工具）

**新建**：`ai-native-maintain/cmd/cloudreve-uploader/main.go`
```go
// 子命令：
//   cloudreve-uploader upload --config=.env.252.enc --src=./v1.14.0-linux-amd64.tar.gz --version=v1.14.0 --platform=linux --arch=amd64
//     -> POST multipart 到本地 admin endpoint（避免 CLI 持有 Cloudreve 凭据）
//   cloudreve-uploader reconcile --config=.env.252.enc --version=v1.14.0
//     -> GET /admin/releases/{v}/artifacts?live=true 重新核对；fail → 重传
//   cloudreve-uploader verify --config=.env.252.enc --uri=cloudreve://...
//     -> 仅 Head 校验
//
// 凭据读取：解密 .env.252.enc（用 SOPS / age / cloud KMS — 当前仓库用 SOPS；具体算法见运维 runbook）
```

**验收**：`go build ./cmd/cloudreve-uploader` 成功；二进制 size < 20MB

### 任务 3.4 — 252 live 烟测脚本（手动操作）

**新建**：`ai-native-maintain/scripts/smoke-252.sh`
```bash
#!/usr/bin/env bash
set -euo pipefail

# 解密凭据 → 调用 maintain 252 admin → 上传 → reconcile → 验收
# 输出到 stdout：catalog JSON / version-check JSON
```

**说明**：此脚本是给用户/运维操作手册使用，**不**进 CI。

**验收**：用户在 252 上手跑一次成功（手动）

### 任务 3.5 — llm-gateway-go installer 对接新 endpoint

**修改**：`llm-gateway-go/installer/internal/upgrader/catalog_source.go`（约 200 行）
- `FetchLatest(ctx, baseURL, channel, current)` 改为 `GET {baseURL}/maintain-api/upgrade/check?channel=...&current=...&arch=...&platform=...`
- 解析 `VersionCheckResult` JSON

**修改**：`llm-gateway-go/installer/cmd/llm-gw-installer/upgrade.go`
- 调用 `catalog_source.FetchLatest`；若 UpdateAvailable=true → 进入既有 download/apply 流程

**新增单测**：`llm-gateway-go/installer/internal/upgrader/catalog_source_test.go`
- httptest fixture 返回新 JSON 形态 → 解析正确

**验收**：`cd llm-gateway-go/installer && go test ./internal/upgrader/...` 通过

### 任务 3.6 — 文档四件套

**新建**：
- `ai-native-maintain/docs/distribution/storage-provider.md` — Provider 接口契约 + 错误码表 + 错误重试策略表
- `ai-native-maintain/docs/distribution/cloudreve-adapter.md` — Cloudreve v4 端点映射表（Storage 操作 → Cloudreve HTTP）+ 凭据约定 + 故障排查（401 / 403 / 5xx）
- `ai-native-maintain/docs/distribution/runbook.md` — publish / unpublish / archive / 重传 / 上传失败后如何重建 artifact 的操作清单
- `ai-native-maintain/docs/distribution/e2e.md` — E2E 环境前置（用户创建 ci-upload 账号的步骤）+ 验收清单（8 步）

**更新**：
- `ai-native-maintain/CHANGELOG.md` — `Unreleased` 段加 `### Added` 条目
- `ai-native-maintain/README.md` — 加 distribution 链接
- `llm-gateway-go/docs/分发与激活/09-分发与下载.md` — 加「API 提供方在 ai-native-maintain」段落，链接到 spec
- `llm-gateway-go/docs/分发与激活/13-双版本构建与分发策略.md` — 加「Cloudreve 自动上传由 cloudreve-uploader CLI 承担」段落

**验收**：
- `git grep -l 'TODO\[NEEDS-CREDENTIALS\]' ai-native-maintain` 仅剩 `docs/distribution/runbook.md`（用户创建账号步骤）

### 任务 3.7 — 同步到 252 pg17 数据库

**手动步骤**（写进 runbook）：
```bash
# 1. 本地先导一份当前数据
pg_dump -h 252 -U maintain -d maintain -t 'maintain.*' > backup-pre-015.sql

# 2. 在 252 上跑迁移（事务）
psql -h 252 -U maintain -d maintain -v ON_ERROR_STOP=1 <<'EOF'
BEGIN;
\i ai-native-maintain/sql/migrations/015_distribution_storage.sql
COMMIT;
EOF

# 3. 校验：DESCRIBE maintain.releases 多 status / archived_at；DESCRIBE maintain.release_artifacts 多 4 列

# 4. 本地开发库同步（开发同学各自跑同一条 SQL）
```

**说明**：252 的 maintain 服务重启由 252 上 systemd unit 管理；迁移跑完后再 restart。

**验收**：
- 252 `psql ... -c "SELECT status, COUNT(*) FROM maintain.releases GROUP BY status"` 返回合理分布
- `ai-native-maintain/sql/migrations/015_distribution_storage.sql` 可重复执行不报错（用 `IF NOT EXISTS`）

---

## 总进度表

| 任务 | 估时 | 依赖 | 主要 commit 区域 |
|---|---|---|---|
| 0.1 README 索引 | 5min | — | ai-native-maintain/README.md, llm-gateway-go/README.md |
| 1.1+1.2 migration 015 | 30min | 0.1 | ai-native-maintain/sql/migrations/015 |
| 1.3 types.go 扩展 | 15min | 1.1, 1.2 | ai-native-maintain/internal/distribution/types.go |
| 1.4 Provider 接口 | 30min | — | ai-native-maintain/internal/distribution/storage/ |
| 1.5 LocalFSAdapter | 1h | 1.4 | ai-native-maintain/internal/distribution/storage/local_fs.go |
| 1.6 CloudreveAdapter | 4h | 1.4 | ai-native-maintain/internal/distribution/storage/cloudreve.go |
| 1.7 配置接入 | 30min | 1.5, 1.6 | ai-native-maintain/internal/config |
| 1.8 Artifact 桥接 | 1h | 1.1, 1.3, 1.7 | ai-native-maintain/internal/httpapi/upload.go |
| 2.1 Repository 扩展 | 2h | 1.3 | ai-native-maintain/internal/store |
| 2.2 upload.go 重构 | 2h | 1.5, 1.6, 2.1 | ai-native-maintain/internal/httpapi/upload.go |
| 2.3 file_handler.go 重构 | 1.5h | 1.5, 1.6, 2.1 | ai-native-maintain/internal/httpapi/file_handler.go |
| 2.4 distribution_admin.go | 2h | 1.5, 1.6, 2.1 | ai-native-maintain/internal/httpapi/distribution_admin.go |
| 2.5 upgrade/check | 30min | 2.1 | ai-native-maintain/internal/httpapi/distribution_public.go |
| 2.6 LicenseAdmin 异步 EnsurePath | 15min | 1.6, 2.4 | ai-native-maintain/internal/httpapi/license_admin.go |
| 3.1 fake Cloudreve | 2h | 1.6 | ai-native-maintain/internal/distribution/storage/cloudreve_fake.go |
| 3.2 E2E 套件 | 3h | 3.1, 2.4, 2.5 | ai-native-maintain/e2e/distribution_test.go |
| 3.3 cloudreve-uploader CLI | 1.5h | 2.2, 3.1 | ai-native-maintain/cmd/cloudreve-uploader |
| 3.4 smoke-252.sh | 30min | 3.3 | ai-native-maintain/scripts/smoke-252.sh |
| 3.5 installer 对接 | 1h | 2.5 | llm-gateway-go/installer/internal/upgrader/catalog_source.go |
| 3.6 文档 4 件套 | 1.5h | 全部 | ai-native-maintain/docs/distribution/*, llm-gateway-go/docs/分发与激活/* |
| 3.7 252 数据库同步 | 30min | 1.1, 1.2 | ai-native-maintain/sql/migrations/015 + runbook |

**总计：约 23 小时**

---

## 阶段门禁（每阶段结束自检）

**阶段 1 自检**：
- [ ] `go test ./internal/distribution/storage/...` 100% 通过
- [ ] 252 pg17 上 `DESCRIBE maintain.release_artifacts` 多 4 列
- [ ] 252 pg17 上 `DESCRIBE maintain.releases` 多 status / archived_at
- [ ] `cfg.Storage.Provider=cloudreve` 配置可正确加载且 `provider.Name() == "cloudreve"`

**阶段 2 自检**：
- [ ] `go test ./internal/httpapi/...` 100% 通过
- [ ] `curl -X PUT .../admin/releases/v1.14.0` 返 200
- [ ] `curl -X DELETE .../admin/releases/v1.14.0/artifacts/linux/amd64` 返 204
- [ ] `curl .../upgrade/check?current=v1.13.0` 返 VersionCheckResult JSON

**阶段 3 自检**：
- [ ] `go test -tags=e2e ./...` 100% 通过
- [ ] `cloudreve-uploader upload` 二进制可执行
- [ ] smoke-252.sh 跑通
- [ ] installer 单测通过
- [ ] 文档 4 件套发布到 docs/distribution/
- [ ] 252 数据库已同步（迁移已跑；服务已 restart）

---

## 风险与回退

| 风险 | 回退策略 |
|---|---|
| Cloudreve v4 实际端点与文档不一致 | 任务 1.6 单测用 fake 服务器；live 烟测 3.4 单跑一次；不通过则改 adapter，本地模式照常工作 |
| 现有 154/245 部署依赖 local-fs 路径 | 默认 Provider="local-fs" 不变；不主动切换；保留 `cfg.Storage.LocalRoot = cfg.ArtifactRoot` 兼容 |
| 迁移 015 跑失败 | `sql/migrations/015` 全用 `IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`；失败可单独 drop 列回退 |
| 上传大文件（>500MB）卡 Cloudreve PUT 超时 | cloudreve adapter PUT 设 `MaxIdleConnsPerHost=4` + `IdleConnTimeout=60s`；client Timeout=30min；失败自动 retry 3 次 |
| E2E 用 dockertest 起 postgres 太慢 | e2e 用 sqlite-memory 模式跑快速子集；完整 e2e 单独脚本 |