# 2026-07-15 — Storage backend boot 接线（Phase 3D）

## 概要

把 `domains/attachments/` 的 pluggable storage matrix 真正接到 `cmd/gateway/main.go`
的 boot 流程。`f17c97c85` (Cloudreve) + `ac8519d62` (OSS/S3 canonical) 之前只是 inactive
library code —— 没有 boot 接线就永远不会被 runtime 选中。

新增 `LLM_GATEWAY_STORAGE_TYPE` 环境变量做 opt-in 控制：

- 未设置 / `filesystem` / `local` / `fs`（任意大小写）→ 保持原 LocalStorageBackend 行为
- `oss` → 起 `OSSStorageBackend`（需要 `-tags storage_oss` 编译）
- `s3` / `minio` → 起 `S3StorageBackend`（需要 `-tags storage_s3` 编译）
- `cloudreve` → 起 `CloudreveStorageBackend`（需要 `-tags cloudreve_storage` 编译）
- 其他 typo 值 → Warn + 退化到 LocalStorageBackend（不阻塞启动）

**核心安全保证**: 默认行为不变。245/154 当前线上版本的 `.env` 里都没有
`LLM_GATEWAY_STORAGE_TYPE`，升级后行为应当完全一致。

## 变更清单

| 文件 | 行数 | 说明 |
|---|---|---|
| `cmd/gateway/attachment_storage_init.go` | 新增 ~165 | helper: `initAttachmentStorage` + `isLocalStorageType` + `isAcceptableBackendType` + `mapBackendTag` |
| `cmd/gateway/attachment_storage_init_test.go` | 新增 ~290 | 12 个单测覆盖每个分支 |
| `cmd/gateway/main.go:1190-1224` | 修改 ~25 行 | 把原 `attachments.NewStorage(attachmentDir)` 硬编码替换为 `initAttachmentStorage(defaultAttachmentDir)` |
| `CHANGELOG.md` | +30 | 顶部 [Unreleased] 子节点 |

## 关键设计决策

### 1. 失败安全到 LocalStorageBackend，不 fatal

任何 opt-in path 错误（typo type / 缺字段 / 不带对应 build tag / 远程不通）
都走 `slog.Warn` + 退到 LocalStorageBackend。**不阻塞 gateway 启动**。

理由：attachments extraction 本身是非关键路径（multimodal feature 关掉，chat 仍然能用），
启动阻塞会让 SRE 在 oncall 时被打脸。让 storage 失败可见（log/HealthCheck），但不要让
整个 gateway 倒下。

### 2. 默认 / 兼容值不区分大小写

`LLM_GATEWAY_STORAGE_TYPE=Filesystem` / `LOCAL` / `fs` 都视为 local，
而不是 typo 退回。理由：CI / 配置管理工具常常把 env key normalize，留宽容错对 ops
更友好。文档里把 canonical 值标为小写 `filesystem`。

### 3. helper 抽到独立文件而非 main.go

`cmd/gateway/` 已有 `session_state_init.go` / `pprof_server.go` / `version.go` 等
`_*_init.go` 命名约定。`attachment_storage_init.go` 跟这条 pattern 一致，
main.go 不至于继续膨胀。

### 4. helper 是可单测的纯函数

`initAttachmentStorage(defaultBaseDir)` 接受 baseDir 作为参数（不查全局），从 `os.Getenv`
读 env 但不写。**没有 goroutine / 没有 syscall / 没有 listener**。这让它在
`go test` 容器里直接跑就行，不需要 daemon 启动来验证。

测试矩阵（§测试矩阵）证明每条分支都在 CI 阶段被覆盖；线上不需要做「boot smoke test」。

### 5. Warning 信息含 actionable hint

```
WARN  attachment storage: backend construct failed, falling back to filesystem
  type=cloudreve
  error="Cloudreve storage backend not available - build with -tags cloudreve_storage"
  hint="if you set cloudreve but the binary was built without -tags storage_cloudreve_storage, rebuild with the tag"
```

ops 看到这种 log 不需要读代码也知道该怎么修。

### 6. 不动 `cmd/gateway/main.go` 的其它逻辑

只替换 `attachmentStorage` 构造那一段。原代码做的事：
- `attachments.NewStorage(baseDir)` → 走 LocalStorageBackend
- `LLM_GATEWAY_ATTACHMENT_MAX_SIZE` → `storage.MaxSize`

新代码做的事完全等价（filesystem 分支），plus 选择性 opt-in 到其它 backend。
其它所有 attachmentStorage 的引用（admin handler / retention worker 等）都不动。

## 测试矩阵

`cmd/gateway/attachment_storage_init_test.go`：

| 测试 | 验证点 |
|---|---|
| `TestInitAttachmentStorage_DefaultIsFilesystem` | 无 env → filesystem + 正确 baseDir |
| `TestInitAttachmentStorage_LocalAliases/{filesystem,local,fs,FILESYSTEM,Local}` | 5 个 alias 都识别为 filesystem |
| `TestInitAttachmentStorage_UnknownTypeFallsBack` | "s3nuba" (typo) → filesystem + Warn |
| `TestInitAttachmentStorage_BackendHealthCheckDoesNotCrash` | 远程不通（127.0.0.1:1）→ boot 不阻塞 |
| `TestInitAttachmentStorage_OSSValidationFails` | OSS 缺 endpoint → filesystem + Warn |
| `TestInitAttachmentStorage_S3ValidationFails` | S3 缺 access key → filesystem + Warn |
| `TestInitAttachmentStorage_MaxSizeAppliedFilesystem` | `LLM_GATEWAY_ATTACHMENT_MAX_SIZE=5242880` 生效 |
| `TestInitAttachmentStorage_BadMaxSizeIgnored` | 非数字 size 被忽略（保留默认 20MB） |
| `TestInitAttachmentStorage_DirCreationForFilesystem` | nested dir 自动创建 |
| `TestIsLocalStorageType` | alias 表正确（含大小写变体）|
| `TestIsAcceptableBackendType` | recognized / non-recognized 表 |
| `TestMapBackendTag` | env→build-tag mapping |

总计 12 测试 / 5 子测试 = **17 PASS**。所有 tag 组合跑通：

```
default:                     12 PASS
cloudreve_storage:           12 PASS
storage_oss:                 12 PASS
storage_s3:                  12 PASS
cloudreve_storage,storage_oss: 12 PASS
cloudreve_storage,storage_s3: 12 PASS
```

外加全 package 跑也绿：

```
=== tag: default ===
ok  	github.com/kaixuan/llm-gateway-go/cmd/gateway
ok  	github.com/kaixuan/llm-gateway-go/domains/attachments
=== tag: cloudreve_storage ===
ok    ...
=== tag: storage_oss ===
ok    ...
=== tag: storage_s3 ===
ok    ...
```

## 部署决策（重要：本次 commit 不上 prod）

### 升级到 245 / 154 是否安全？

**当前回答**：**是**，行为不变升级。但建议仍然按正常流程走：

1. 245 upgrade → 246 → 245+1 → ... (正常 staging)
2. soak 24h 看「attachment storage backend selected」日志确认 type=filesystem
3. 154 promotion

理由：升级是「零行为变更」但我们要让 boot WARN 类日志出现在生产 stream，
这样 ops 能确认线上跑的 type 确实是 filesystem（而不是误触发了 typo 退化）。

### 真实 OSS/S3/Cloudreve 启用（仍然不在本次范围）

要真正在 245 / 154 启用新后端：

1. `build-image.sh` 加 `TAGS="cloudreve_storage"` 之类（按目标 backend）
2. `.env` 加 `LLM_GATEWAY_STORAGE_TYPE=cloudreve` + 凭据
3. SSH 注入 凭据到 `EnvironmentFile=/opt/llm-gateway-go/.env`
4. **业务 owner review** —— 涉及 LLM 网关的存储 backend 切换是 P1 变更
5. 245 staging soak（含真实 multipart / presign / 大文件 smoke）
6. 154 promotion

单独 PR（Phase 3E）。

## 已知限制

1. **Boot-stage health-check 是 best-effort** —— 如果 cloudreve instance 不通，
   log WARN 然后继续启动。attachment extraction 会在第一次写时再 fail。
   想要 fail-fast 可以加 LLM_GATEWAY_ATTACHMENT_REQUIRE_HEALTH=true（不在本次）。

2. **不存在显式 `filesystem`/`local` 之外的非远程 backend 类型**。
   未来若要支持「NAS via NFS / SMB」之类，要新加 backend type + 验证表。

3. **`isAcceptableBackendType` 是黑/白名单**。要给 ops 加新 backend type，
   必须改 `attachmentBackendTypeAcceptable` map + 测试 + 文档。

4. **未在 245 跑真实 boot smoke test**。因为这次 commit 涉及 boot path，
   但又显式 fail-safe 不阻塞启动，所以单测覆盖已经够信。如果 245 升级后
   上线 24h 内 log stream 出现 `attachment storage: backend construct failed` 等
   WARN，需要立刻回滚到上一个 deploy。
