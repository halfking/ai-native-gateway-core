# 2026-07-23 Distribution & Version-Management API (B+C) — Design

> 范围：分发自动化 + 版本管理 API。覆盖 `docs/分发与激活/` 第 09、13 章的 P0 缺口，
> 与 `ai-native-maintain/internal/distribution/` 现状合并推进。
> 仓库拆分：`ai-native-maintain`（主）+ `llm-gateway-go`（installer 侧只改 1 处）。
> 频道：单 stable（schema 预留 channel 列）。
> build_seq：手动（本 spec 不设计自动递增；与 A 子系统重叠，用户已确认延期）。

---

## §1 架构总览

```
┌──────────────────────────────────────────────────────────────────────┐
│                客户侧 (llm-gateway-go)                                │
│  installer upgrade check ──► GET /maintain-api/distribution/version-check
│  installer install     ──► GET /maintain-api/distribution/catalog     │
└─────────────────────────────┬────────────────────────────────────────┘
                              │ HTTPS
                              ▼
┌──────────────────────────────────────────────────────────────────────┐
│  ai-native-maintain   (主仓库，本 spec 主要改动)                        │
│                                                                       │
│   HTTP layer (internal/httpapi/)                                     │
│     /maintain-api/distribution/{catalog,version-check,versions}      │
│     /maintain-api/admin/distribution/...                             │
│                       │                                               │
│                       ▼                                               │
│   DistributionService   [新 — 编排层]                                 │
│           │                                                           │
│     ┌─────┴─────┐                                                     │
│     ▼           ▼                                                     │
│  CatalogService  StorageProvider iface [新]                          │
│  (DB + ticket)    Upload/Head/Delete/SignedURL/List/EnsurePath       │
│                         │                                             │
│                         ▼                                             │
│              Cloudreve v4 adapter [新]                                │
│              (session/token + file/* + upload/{sessionId})           │
└─────────────────────────────────┬─────────────────────────────────────┘
                                  │ HTTPS + JWT (ci-upload 账号)
                                  ▼
                  ┌────────────────────────────────────────┐
                  │  files.kxpms.cn (Cloudreve v4.15.0)    │
                  │  cloudreve://my/release/llm-gateway-go/│
                  │    └── {version}/                      │
                  │        ├── *.tar.gz / *.zip            │
                  │        ├── SHA256SUMS                  │
                  │        └── release-notes.md            │
                  └────────────────────────────────────────┘
```

**关键边界**：
- `StorageProvider` 是接口；今天只有 `CloudreveAdapter` 实现。未来加 S3 不动 service/catalog/DB。
- `DistributionService` 是唯一编排层；HTTP handler 只翻译参数；事务边界在 service。
- `TicketSigner`（已存在）签下载 URL 时，本 spec 暂**不**强制校验（依赖 Cloudreve 端文件 capability `ReadAnonymous`；未来若需可撤销签名再补 reverse-proxy，详见 §3.2）。
- `ci-upload` 专用账号：非 Admin，仅 `ScopeFilesRead + ScopeFilesWrite`，路径限于 `cloudreve://my/release/llm-gateway-go/`。

---

## §2 HTTP API 契约

**基础路径**：
- 公共：`/maintain-api/distribution/*`
- Admin：`/maintain-api/admin/distribution/*`（需 JWT，`scope` 包含 `distribution:write`）

### 2.1 公共 API

| Method | Path | 说明 |
|---|---|---|
| `GET` | `/catalog?channel=stable` | 完整目录（沿用现有 `CatalogService.BuildCatalog`） |
| `GET` | `/version-check?channel=stable&current=v1.13.0&arch=amd64&platform=linux` | 升级检测 |
| `GET` | `/versions` | 轻量列表（仅 released，无 artifacts 详情） |
| `GET` | `/versions/{version}/manifest` | 单版本 manifest（含 SHA256SUMS 直链） |

**`/version-check` 行为**：
- `current` 缺失 → 返回 `latest`，`update_available=false`，`min_required` 默认空。
- `current` 提供 → `autoupdate.IsNewer(current, latest)` 判断；返回 `update_available=true`；同时返回 `min_required`（DB 字段，可空）。
- `target_artifacts[*].download_url`：Cloudreve 直链，TTL 由 Cloudreve 端 capability 控制。
- 错误 envelope：`{code, msg, data, correlation_id}`（沿用 Cloudreve 风格）。

### 2.2 Admin API

| Method | Path | 说明 |
|---|---|---|
| `POST` | `/versions` | 创建 `draft` 版本 |
| `GET` | `/versions` | 列出所有版本（含 draft），支持 `?status=` 过滤 |
| `GET` | `/versions/{version}` | 单版本详情 |
| `PUT` | `/versions/{version}` | 更新 `notes / release_date / build_seq`（仅 draft 状态可改 `version` 字段） |
| `DELETE` | `/versions/{version}` | 软删除 → `archived`；released 不可删 |
| `POST` | `/versions/{version}/publish` | `draft → released`；幂等（已 released 返回 200 + 当前状态） |
| `POST` | `/versions/{version}/unpublish` | `released → draft`；不删 Cloudreve 文件 |
| `POST` | `/versions/{version}/artifacts` | 上传文件元数据（multipart + 流转发到 Cloudreve） |
| `GET` | `/versions/{version}/artifacts` | 列出 artifacts（含 Cloudreve `live` 状态） |
| `DELETE` | `/versions/{version}/artifacts/{platform}/{arch}` | 删 Cloudreve 文件 + DB 行 |

**约束**：
- Admin 路由强制 JWT + `distribution:write` scope。
- 仅 `customer` edition 上传；`master` 流程独立（本 spec 不覆盖）。
- `publish` 前必须有 ≥1 个 artifact，否则 422。
- 唯一约束 `(version_id, platform, arch, edition)`：冲突 → 409。
- 单个 artifact 大小上限 10GB（Cloudreve v4 chunked upload 限制），>10GB → 413。

---

## §3 Storage Provider 接口 + Cloudreve 适配器

### 3.1 Go 接口

```go
// 路径：ai-native-maintain/internal/distribution/storage/provider.go
package storage

type Provider interface {
    EnsurePath(ctx context.Context, rootURI string) (string, error)
    Upload(ctx context.Context, opts UploadOptions) (*Artifact, error)
    Head(ctx context.Context, uri string) (*Artifact, error)
    Delete(ctx context.Context, uri string) error
    SignedDownloadURL(ctx context.Context, uri string, ttl time.Duration) (string, time.Time, error)
    List(ctx context.Context, dirURI string) ([]Listing, error)
}

type UploadOptions struct {
    TargetURI      string
    Source         io.Reader
    Size           int64
    ExpectedSHA256 string
    Overwrite      bool
}

type Artifact struct {
    URI       string
    Size      int64
    SHA256    string
    ETag      string
    UpdatedAt time.Time
}

type Listing struct {
    Name string
    URI  string
    Size int64
    IsDir bool
}

var (
    ErrNotFound = errors.New("storage: not found")
    ErrConflict = errors.New("storage: already exists")
    ErrTooLarge = errors.New("storage: payload too large")
)
```

**接口原则**：
- 不暴露 Cloudreve 概念（`cloudreve://` scheme 对 service 透明；service 传 `release/llm-gateway-go/{ver}/foo.tgz`，adapter 拼 URI scheme）。
- `Upload` 接 `io.Reader`：admin handler 直接流转发，不落盘 maintain 本地。
- `Head` 必须支持「检查文件是否真的在 Cloudreve」——catalog `live` 状态的真相来源。
- 不暴露 Cloudreve JWT：adapter 内部维护 token 缓存 + 自动 refresh。

### 3.2 Cloudreve v4 映射

| Storage 操作 | Cloudreve v4 HTTP |
|---|---|
| 登录 | `POST /api/v4/session/token`（缓存 access_token，401 时 refresh_token 续期） |
| `EnsurePath(dirURI)` | `POST /api/v4/file/create` `{uri, type: "folder"}`（409 = 已存在，吞掉） |
| `Upload` 准备 | `POST /api/v4/upload/{sessionId}` |
| `Upload` 流 | `PUT /api/v4/upload/{sessionId}`（chunked binary） |
| `Upload` 完成 | `POST /api/v4/upload/{sessionId}/complete` |
| `Head(uri)` | `GET /api/v4/file?uri={uri}` 取 `metadata.size + sha256`；404 → `ErrNotFound` |
| `Delete(uri)` | `DELETE /api/v4/file` body `{"uris":[uri]}` |
| `SignedDownloadURL` | Cloudreve v4 无原生临时 token；策略：直链 + 文件 capability `ReadAnonymous`。依赖用户在 Cloudreve 后台为 `release/llm-gateway-go/{ver}/` 子树勾选「任何登录用户可下载」。adapter 不签 URL（持有密钥风险）。 |
| `List(dirURI)` | `GET /api/v4/file?uri={dirURI}` 取 `files[]` |

**简化方案的可逆性**：若未来要可撤销签名 → 加一层 reverse-proxy（`llm.kxpms.cn/download/*`）用 maintain 端持有的 `GeneralAuth` secret 二次签名；adapter 接口不动。

### 3.3 错误处理与重试

| 错误 | HTTP code | Retry |
|---|---|---|
| `ErrNotFound` | 404 | 否 |
| `ErrConflict` | 409 | 否（用户应选 `Overwrite=true`） |
| Cloudreve 401 | 502 `cloudreve_unauthorized` | 内部 1 次（refresh_token） |
| Cloudreve 5xx | 502 `cloudreve_upstream` | 内部 3 次，指数退避 1s/2s/4s |
| Cloudreve 超时 (>30s) | 504 | 同上 |
| 网络 EOF | 502 | 内部 3 次 |

---

## §4 DB Schema

```sql
-- 迁移：maintain-2026-07-23-distribution-v1

CREATE TABLE release_versions (
    id              BIGSERIAL PRIMARY KEY,
    version         TEXT NOT NULL,
    build_seq       INT  NOT NULL,
    channel         TEXT NOT NULL DEFAULT 'stable',
    status          TEXT NOT NULL DEFAULT 'draft',
    git_sha         TEXT,
    git_tag         TEXT,
    release_date    DATE,
    min_required    TEXT,
    notes           TEXT,
    notes_url       TEXT,
    published_at    TIMESTAMPTZ,
    published_by    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT release_versions_version_uniq UNIQUE (version, channel)
);
CREATE INDEX release_versions_status_channel_idx
    ON release_versions (status, channel);
CREATE INDEX release_versions_published_idx
    ON release_versions (published_at DESC NULLS LAST)
    WHERE status = 'released';

CREATE TABLE release_artifacts (
    id              BIGSERIAL PRIMARY KEY,
    version_id      BIGINT NOT NULL REFERENCES release_versions(id) ON DELETE CASCADE,
    platform        TEXT NOT NULL,
    arch            TEXT NOT NULL,
    edition         TEXT NOT NULL DEFAULT 'customer',
    artifact_name   TEXT NOT NULL,
    storage_uri     TEXT NOT NULL,
    size_bytes      BIGINT NOT NULL,
    sha256          TEXT NOT NULL,
    etag            TEXT,
    uploaded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_verified_at TIMESTAMPTZ,
    last_verified_ok BOOLEAN,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT release_artifacts_uniq UNIQUE (version_id, platform, arch, edition)
);
CREATE INDEX release_artifacts_version_idx ON release_artifacts (version_id);

CREATE TABLE download_events (
    id              BIGSERIAL PRIMARY KEY,
    request_id      TEXT NOT NULL,
    version         TEXT NOT NULL,
    platform        TEXT NOT NULL,
    arch            TEXT NOT NULL,
    edition         TEXT NOT NULL DEFAULT 'customer',
    channel         TEXT NOT NULL DEFAULT 'stable',
    holder_id       BIGINT,
    donation_id     BIGINT,
    source          TEXT NOT NULL DEFAULT 'installer',
    result          TEXT NOT NULL DEFAULT 'started',
    duration_ms     INT,
    user_agent      TEXT,
    client_ip       INET,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX download_events_version_idx ON download_events (version);
CREATE INDEX download_events_request_idx ON download_events (request_id);
CREATE INDEX download_events_created_idx ON download_events (created_at DESC);

CREATE TABLE publish_audit (
    id              BIGSERIAL PRIMARY KEY,
    version_id      BIGINT NOT NULL REFERENCES release_versions(id),
    actor           TEXT NOT NULL,
    action          TEXT NOT NULL,
    from_status     TEXT,
    to_status       TEXT,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX publish_audit_version_idx ON publish_audit (version_id, created_at DESC);
```

**设计选择**：
- 软删除：`status='archived'`，不真删。
- `storage_uri` 单独列，**不**直接用 `cloudreve://` scheme 硬编码；service 视其为 opaque string；Cloudreve adapter 内部拼装。未来切 S3，DB 不动。
- `last_verified_at` + `last_verified_ok`：admin `GET /artifacts` 调 `storage.Head()` 后写回这 2 列。
- `publish_audit` 记录「谁在什么时候发了哪个版本」。
- `release_notes` 只放 URL；内容由 `release-notes.md` 上传到 Cloudreve。
- 不在本表里放 `donation_id` / `holder_id` FK（donation/holder 表在 maintain 仓其它地方，独立演进）。

---

## §5 测试策略 + E2E 方案

### 5.1 测试金字塔

| 层 | 工具 | 覆盖 |
|---|---|---|
| Unit | `go test` + `testify` | `storage/cloudreve_adapter_test.go`（mocked HTTP）、`distribution_service_test.go`（fake Repository + fake Provider）、`handlers_test.go`（httptest） |
| Integration | `dockertest postgres` + `httptest.Server`（fake Cloudreve） | DB 迁移 + repository + service + handler 串通 |
| E2E | `go test -tags=e2e` | 全链路：create → upload → publish → catalog → version-check → 模拟 installer 拉下载 |

### 5.2 单元测试关键 case

- `Provider` 接口：100% 方法覆盖 + mock 返回
- `CatalogService.BuildCatalog`：空版本（fallback）、单版本无 artifact（fallback matrix）、单版本有 artifact、12 版本上限、错误传递
- `DistributionService.UploadArtifact`：流转发完整性、SHA256 中途校验失败、Conflict 已存在、Head 不通、context cancel
- `DistributionService.Publish`：无 artifact 时 422、已 released 幂等返回 200、archived 不可发布
- `VersionCheck`：current 缺失、current 等于 latest、current 落后、min_required 拒绝升级
- `TicketSigner`：签/验、过期、改字段拒绝

### 5.3 Integration 关键 case（fake Cloudreve）

- `POST /admin/versions` → DB 写入 draft → `GET /admin/versions` 可见
- `POST /admin/versions/{v}/artifacts`（multipart，~50MB 随机流）→ adapter 调 fake Cloudreve → DB 行写入 + `storage_uri` 正确
- `POST /admin/versions/{v}/publish` → status 变 released
- `GET /catalog` → 含 published 版本 + 默认 matrix
- `GET /version-check?current=v1.0.0` → `update_available=true` + target 正确

### 5.4 E2E 方案（fake Cloudreve + 可选 live 252）

**环境前置**（一次性手工，本 spec 提供 SOPS 模板）：
1. 用户在 Cloudreve 后台创建 `ci-upload@kxpms.cn` 账号 + 配 Publisher 组 + 颁发 token
2. SOPS 加密 `ai-native-maintain/.env.distribution.enc`：`CLOUDREVE_BASE_URL=https://files.kxpms.cn`、`CLOUDREVE_CI_TOKEN=...`
3. `make e2e-setup` 起本地 postgres + 跑迁移 + 起 maintain dev 服务（`http://localhost:9090`）

**E2E 套件** `ai-native-maintain/e2e/distribution_test.go`：

```go
//go:build e2e
func TestE2E_DistributionFullCycle(t *testing.T) { ... }
```

伪代码骨架：
1. create draft version `v9.9.9-e2e`
2. upload 3 artifacts（linux/amd64、darwin/arm64、windows/amd64）~2MB 随机流
3. live-verify: HEAD on Cloudreve 确认文件存在 + SHA256 一致
4. publish
5. catalog 含此版本
6. version-check 从 v9.9.8 → update_available=true + target=v9.9.9-e2e
7. 模拟 installer GET download URL，200 + 完整 body
8. cleanup：delete 3 artifacts + unpublish + soft-delete version

**E2E 验收**：
- [ ] 全 8 步走通，无 5xx
- [ ] Cloudreve 端 `cloudreve://my/release/llm-gateway-go/v9.9.9-e2e/` 实际创建并含 3 文件
- [ ] DB `release_artifacts` 表 3 行
- [ ] 客户端下载后 sha256 与 DB 一致
- [ ] cleanup 后 Cloudreve 目录为空
- [ ] 不破坏其它已发布版本（catalog 仍含历史）

### 5.5 测试不覆盖什么

- **不测 live Cloudreve 性能**：E2E 走 fake Cloudreve 做并发；live 252 仅冒烟。
- **不测大文件**（>100MB）：E2E 用 2MB；100MB+ 由 A 子系统自测。
- **不测 malicious user**：admin auth 信任 SOPS 加密 token；不做 scope 权限绕过测试。

---

## §6 范围外 + 文档交付 + 推进次序

### 6.1 不在本 spec（其它 spec 接管）

| 议题 | 后续 spec |
|---|---|
| `packaging/build-{master,customer}.sh` 6 平台产物 + Docker 镜像 | **A** 打包自动化 |
| `installer install.sh` 用户主装 + `installer install --docker` | **D** 用户安装脚本 |
| `installer upgrade` 守护 + slot/blue-green + auto-rollback | **E** 用户升级脚本 |
| 自动 build_seq 递增 | A（用户已确认延期） |
| LTS / beta / alpha 频道扩展 | 启用时单独 spec（schema 已可承载） |
| 官网 `llm.kxpms.cn/maintain/download` 页面 | 另 spec |
| 客户捐助 + ticket 留空间 | 已归档，不复活 |

### 6.2 文档交付

**新增**（`ai-native-maintain/docs/distribution/`）：
- `storage-provider.md` — Provider 接口约定 + 错误码 + 重试策略
- `admin-api.md` — Admin endpoint 速查
- `public-api.md` — Public endpoint 速查
- `cloudreve-adapter.md` — Cloudreve v4 映射表 + 账号/凭据约定 + 故障排查
- `runbook.md` — publish / unpublish / 删除 artifact 操作手册
- `e2e.md` — E2E 环境准备 + 验收清单

**更新**：
- `ai-native-maintain/CHANGELOG.md` — `Unreleased` 段
- `ai-native-maintain/README.md` — 加 `distribution` 模块说明
- `llm-gateway-go/docs/分发与激活/09-分发与下载.md` — 收敛到「API 由 ai-native-maintain 提供」
- `llm-gateway-go/docs/分发与激活/13-双版本构建与分发策略.md` — 移除 TODO 指向本 spec

### 6.3 推进次序

1. DB migration v1（4 张表）
2. `storage.Provider` 接口 + fake 实现 + 单测
3. Cloudreve adapter + 单测 + 集成 fake Cloudreve
4. Repository（pgx）+ 单测（dockertest postgres）
5. `DistributionService` 编排层 + 单测
6. HTTP handlers + 单测 + 集成 fake Cloudreve
7. 挂上 `cmd/maintain` 入口 + JWT scope 校验
8. E2E 全链路
9. 文档 + CHANGELOG
10. 用户手工 smoke（创建 ci-upload 账号）

### 6.4 风险与开放问题

| 风险 | 应对 |
|---|---|
| Cloudreve v4.15 → 后续 v4.x API 微调让 adapter 失效 | adapter 集中在 `cloudreve_adapter.go`；版本常量 `cloudreveAPIVersion = "v4"` 在 `consts.go`；e2e 跑 fake 不直接探活 |
| 用户没及时创建 ci-upload 账号 | 显式 TODO[NEEDS-CREDENTIALS]；E2E 跑 fake 不依赖真账号；文档写明「账号不存在则 E2E 跳过 live 模式」 |
| 大文件（>2GB）流上传中途断网 | adapter PUT 分块 + 重试；断流后 Cloudreve 端保留 `incomplete` upload session，下次 resume（待实测） |
| publish 误发错版无 UI 回退 | `POST /versions/{v}/unpublish` 配 audit 留痕；UI 后续 spec |
| tickets 与 Cloudreve share 重复 | 本 spec 不引入 Cloudreve share；ticket 仅作未来 trace 字段预留，不强制校验 |

---

## §7 开放 TODO（实现阶段补）

- [ ] **TODO[NEEDS-CREDENTIALS]**：用户在 Cloudreve 后台创建 `ci-upload@kxpms.cn` 账号 + Publisher 组 + token；token 进 SOPS `.env.distribution.enc`。
- [ ] **TODO[NEEDS-UI]**：Admin UI（`maintain-web`）增加版本管理页面（draft / publish / artifacts 上传）。
- [ ] **TODO[FOLLOW-UP]**：若 future spec 要求可撤销签名下载 URL，加 reverse-proxy 签发层。
- [ ] **TODO[FOLLOW-UP]**：自动 build_seq 递增由 A 子系统接管。

---

## 状态

- Draft：待用户审阅。
- 审阅通过后 → 调用 writing-plans skill 输出 implementation plan。