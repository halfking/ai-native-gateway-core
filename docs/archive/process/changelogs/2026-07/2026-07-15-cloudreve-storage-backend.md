# 2026-07-15 — Cloudreve StorageBackend 适配器（Phase 3：可插拔存储矩阵扩展）

## 概要

给 `domains/attachments` 加第四个存储后端类型 `cloudreve`，通过 Cloudreve 的 WebDAV
端点（`/dav/...`）实现标准的 `StorageBackend` 接口。这是继 `filesystem` 之后第二个
真正对齐 canonical 接口的实现；之前 OSS/S3 的 build-tag 文件由于签名漂移没法编译。

不改 gateway 的 boot 流程；通过 `attachments.NewStorageBackendFromConfig(cfg)` 在
调用方需要时按需切换。CLI / env 入口已对齐。

## 变更清单

| 文件 | 类型 | 行数 | 角色 |
|---|---|---|---|
| `domains/attachments/storage_backend_cloudreve.go` | 新增 | ~430 | CloudreveStorageBackend，对齐 canonical `StorageBackend` |
| `domains/attachments/storage_backend_cloudreve_stub.go` | 新增 | ~20 | 不带 `cloudreve_storage` tag 时的友好 stub |
| `domains/attachments/propfind_parser.go` | 新增 | ~85 | RFC 4918 multistatus XML 解析（PROPFIND 响应） |
| `domains/attachments/storage_backend_cloudreve_test.go` | 新增 | ~480 | 13 个单测：httptest 模拟 Cloudreve WebDAV server |
| `domains/attachments/storage_config_cloudreve_test.go` | 新增 | ~95 | ValidateStorageConfig + env loader 集成测试 |
| `domains/attachments/storage_config.go` | 修改 | +30 | `StorageConfig` 新增 `Cloudreve*` 字段；switch "cloudreve" 分支；env loader；Validate |

## 关键设计决策

### 1. 选 WebDAV 而不是 REST API

Cloudreve v4 同时提供 REST (`/api/v4/...`) 和 WebDAV (`/dav/...`)。选择 WebDAV：
- **一套协议覆盖全部 9 个接口方法**：
  - `Save` → `PUT`
  - `SaveReader` → `PUT` with `Content-Length`（流式不缓到内存）
  - `GetReader` / `Get` → `GET`
  - `Exists` → `HEAD`
  - `Delete` → `DELETE`
  - `List` → `PROPFIND Depth: 1`
  - `GetMetadata` → `HEAD`（直接读 `Content-Length` / `Last-Modified`）
  - `HealthCheck` → `PROPFIND Depth: 0`
- **PUT 幂等** + **HEAD 廉价**：契合 `SaveBase64Image` 的先 `Exists` 去重再 `Save` 流程
- **不支持 chunked transfer**（这是 Cloudreve/sabredav 限制），但我们的 `SaveReader`
  上游总是有 `size`（`io.LimitReader(reader, size)`），所以不影响
- **避免拉取整套 REST client**：少 ~5 个 SDK 依赖

### 2. Build tag 默认关闭

跟 OSS/S3 保持一致：`//go:build cloudreve_storage`。默认 `go build` 不引入 Cloudreve
代码路径，避免给常规部署带无用依赖。启用方式：

```bash
go build -tags cloudreve_storage ./cmd/gateway
LLM_GATEWAY_STORAGE_TYPE=cloudreve \
LLM_GATEWAY_CLOUDREVE_BASE_URL=https://cloudreve.kxpms.cn \
LLM_GATEWAY_CLOUDREVE_USERNAME=gateway \
LLM_GATEWAY_CLOUDREVE_PASSWORD='...' \
LLM_GATEWAY_CLOUDREVE_REMOTE_PATH=/llm-gateway-attachments \
./gateway
```

### 3. 路径安全

- `sanitizeKey()` 拒 `..` / `.` / 空前缀，镜像 `LocalStorageBackend.getFilePath`
- WebDAV URL 用 `url.PathEscape` 段级编码，支持空格 / Unicode 文件名
- `MKCOL` 在 PUT 前自动建父目录链；`405 Method Not Allowed` = 已存在 = 幂等

### 4. 与 Phase 2C（migration 401 `request_attachments`）集成点

`StoragePath` 列在 `request_attachments` 表中本来就是 VARCHAR —— Cloudreve 适配器
返回的 `key`（如 `2026/07/a1/b2/a1b2c3d4.png`）直接落库即可，不需要 schema 改动。
下游读取仍走 `backend.Get(relPath)` 反向查 WebDAV `GET`。

## 测试矩阵

| 场景 | 期望 | 实际 |
|---|---|---|
| `NewCloudreveStorageBackend` 三种 config 校验（缺 URL / 缺凭证 / 错 scheme） | 失败 | ✅ |
| 默认 remotePath / Timeout | `/llm-gateway-attachments` / 30s | ✅ |
| `Save → Get/GetReader` 循环 | 字节一致 | ✅ |
| `SaveReader` 流式上传 | 字节一致 | ✅ |
| `SaveReader(size=-1)` | 返回错误 | ✅ |
| `Exists` 真假两路 | 200→true / 404→false | ✅ |
| `Delete` 幂等 | 二次删不报错 | ✅ |
| `Get` 不存在 | error 含 "not found" | ✅ |
| `GetMetadata` 含 ETag / Content-Type / Last-Modified | 全字段填充 | ✅ |
| `GetMetadata` 不存在 | error 含 "not found" | ✅ |
| `GetBackendType` | `"cloudreve"` | ✅ |
| `HealthCheck` 2xx 路径 | 无错 | ✅ |
| `List` 含 Depth:1 过滤 / 子目录 / 目录条目跳过 | ≥2 file / 无尾斜杠 | ✅ |
| `sanitizeKey` 拒 `../`/`.`/`/` | 全部返回 error | ✅ |
| `parsePropfindResponse` 多形态（RFC / bad / empty） | 三档分别通过 | ✅ |
| `ValidateStorageConfig` 四档（缺 URL/用户/密码 / 全填好） | 三档报错一档通过 | ✅ |
| env-loader 完整路径 | 与显式 config 一致 | ✅ |

总计 21 个测试，全绿。

## 已知限制

1. Cloudreve 没提供 chunked PUT —— `SaveReader` 必须知道上游大小。`io.LimitReader(reader, size)`
   上限保证内存常驻 ~64KB，跟原始 1MB 限制对齐。
2. `List` Depth:1 只返回直接子节点；如果调用方传 deep prefix（如 `2026/07` 而非
   `2026/07/aa/`），会按 prefix 字符串后过滤，对 Cloudreve 行为是「先 MKCOL-like listing
   → 然后字符串 startsWith 二次过滤」，不是真递归 PROPFIND。后续如需要递归列表再补
   `Depth: infinity` 配置。
3. 没实现签名 URL / PublicLink 之类 Cloudreve 独有功能——canonical `StorageBackend`
   没有这些方法；如果后续要做可以让 Cloudreve 适配器再嵌入签名能力。

## 部署相关

### 不联动 boot 流程（明示决策）

`cmd/gateway/main.go` 仍然只走 `attachments.NewStorage(baseDir)` 启动，**不**自动
读取 `LLM_GATEWAY_STORAGE_TYPE` 切换后端。理由：

- `LoadStorageConfigFromEnv()` 已经存在并已 cloudreve 友好；
  调用方一行代码 `backend, _ := attachments.NewStorageBackendFromConfig(cfg)`
  + `attachments.NewStorageWithBackend(backend)` 即可接入
- 启动期隐式切换存储是 P0 级别配置爆炸点（数据丢失风险），不应在没评审的情况下
  引入 —— 这点同样适用于 OSS / S3 的现有 stub

要在 prod 启用，需要另起一个独立的 PR（≥ 1 个 business owner review）做
`main.go` 的 boot 接线，并加 e2e 验证。

### 与官方部署架构的关系

`deploy/` 下的 `build-image.sh` 仍按 `go build ./cmd/gateway`（无 tag）编译。
如要让 build image 支持 Cloudreve，需要：

1. `build-image.sh` 加 `TAGS="cloudreve_storage"`（或外部传参）
2. 注入 Cloudreve 凭据到镜像运行环境（k8s Secret / env file）
3. 245 → 154 晋级测试在迁移路径里覆盖

以上放到 245 部署 PR 中。

## 修复的预存问题（不在本任务范围内，但记录）

旧 `storage_backend_oss.go` / `storage_backend_s3.go` 是基于**旧 StorageBackend 接口**
编写的（无 `ctx`、`StorageMetadata` 而非 `FileMetadata`），打开 `-tags storage_oss`
/ `-tags storage_s3` 直接编译失败。这两个文件长期只走 stub 分支，OSS/S3 后端在
production 从未真正启用过。本任务的 Cloudreve 适配器**不复用**上述老代码，完全按
canonical 接口重新实现，作为后续 OSS / S3 适配器重构的参考模板。
