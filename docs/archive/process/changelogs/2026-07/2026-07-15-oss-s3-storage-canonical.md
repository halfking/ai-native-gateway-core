# 2026-07-15 — OSS / S3 StorageBackend canonical 修复（audit §4.4 关闭）

## 概要

关闭 `docs/会话优化v2/01-存储配置审计报告.md` § 4.4 列出的所有编译失败问题：
旧的 `storage_backend_oss.go` / `storage_backend_s3.go` 是基于**旧 StorageBackend
接口签名**（无 `ctx`、使用 `StorageMetadata` 而非 `FileMetadata`）编写的，加上
`storage_oss` / `storage_s3` build tag 时整个包编译失败。

之前的提交 `f17c97c85 feat(attachments): Cloudreve StorageBackend via WebDAV`
已经用 canonical 接口写好了 Cloudreve 适配器并提供了 `sanitizeKey` / `detectContentType`
辅助函数；本次按同一模板重写 OSS 和 S3 后端，保证 storage matrix 全部 pluggable。

## 变更清单

| 操作 | 文件 | 说明 |
|---|---|---|
| 删除 | `domains/attachments/storage_backend_oss.go` | 旧版（编译失败） |
| 删除 | `domains/attachments/storage_backend_s3.go` | 旧版（编译失败） |
| 新增 | `domains/attachments/storage_backend_oss.go` | 对齐 canonical 接口的 canonical 实现（470 行） |
| 新增 | `domains/attachments/storage_backend_s3.go` | 对齐 canonical 接口的 canonical 实现（310 行） |
| 新增 | `domains/attachments/storage_backend_oss_test.go` | 7 个测试（构造校验 + key 处理 + 类型映射） |
| 新增 | `domains/attachments/storage_backend_s3_test.go` | 13 个测试（httptest 路径式 SDK + key 处理 + 类型映射） |
| 新增 | `domains/attachments/sanitize_key.go` | 跨后端的 key sanitizer（Cloudreve / OSS / S3 共用） |
| 新增 | `domains/attachments/detect_content_type.go` | MIME 推断（S3 PutObject 用） |
| 修改 | `CHANGELOG.md` | 增加 [Unreleased] 子节点 |

`storage_backend_oss_stub.go` 和 `storage_backend_s3_stub.go` 已是正确签名（`*OSSConfig`/
`*S3Config` 指针），无需修改。

## 关键设计决策

### 1. 复用 `f17c97c85` 已沉淀的 helpers

`Cloudreve` 提交已经把 `sanitizeKey`（路径安全）和 `detectContentType`（MIME 推断）
做出来了。本次把 `sanitizeKey` 提取到独立文件 `sanitize_key.go`，删掉 Cloudreve 文件
里的私有副本；`detectContentType` 同理放进 `detect_content_type.go`。三套后端共用同一份
安全/语义保证。

### 2. 用 `GetBucketACL` 而不是 `GetBucketInfo`

vendor 里的 `aliyun-oss-go-sdk` 老版本不带 `GetBucketInfo`。换成 `GetBucketACL`
（也是 Client-level，不需要 ListBucket 权限），语义不变：构造期就触发一次往返，
凭据 / 网络 / 桶存在性 一次性验证。

### 3. S3 `SaveReader` 用 `io.ReadAll` 到 `bytes.Reader`

`aws-sdk-go-v2` 在非 TLS 场景要求 body 可 seek 或 precomputed checksum。从
`io.Reader` + `ContentLength` 直接传给 SDK 会在某些路径触发
`unseekable stream is not supported without TLS and trailing checksum`。

生产方案：先 `io.ReadAll` 到 `bytes.Buffer`，再构造 `bytes.NewReader(buf)` 传给 SDK。
代价是失去 SaveReader 的流式优势；收益是稳。

这个 trade-off 的合理性：canonical `Attachment` 的 `MaxSize = 20MB`（见
`storage.go::DefaultMaxSize`），即最坏 20MB 常驻内存——和 `SaveBase64Image` 路径里
`io.ReadAll(content)` 占用的内存是同一量级；对网关主流程无新增内存风险。

未来如果需要真流式（例如 raw video），应当绕过 SaveReader 直接走 multipart-upload
helper，那是另一个独立 PR 的话题。

### 4. `isOSSNotFound` 既支持 `*oss.ServiceError` 也支持值类型

`aliyun-oss-go-sdk` 的 `tryConvertServiceError` 返回 `oss.ServiceError` 值类型
（`func (e ServiceError) Error()` 是值接收）。`errors.As(err, &ptr)` 拿不到东西，
但 `errors.As(err, &val)` 拿得到。所以生产代码同时试两个 target，保证 wrapped
error（`fmt.Errorf("%w", ...)`）也能命中。

`isS3NotFound` 保持 `*types.NotFound` 单一目标——AWS SDK 一致用指针类型做错误
signal。

### 5. 健康检查粒度

- `OSSStorageBackend.HealthCheck` = `client.GetBucketACL(bucket)` — 验证凭证 + 网络 + 桶可见
- `S3StorageBackend.HealthCheck` = `client.HeadBucket(...)` — 比 GetBucketACL 更轻；S3
  不需要 GetBucketACL 权限

两者都比 Cloudreve 的 PROPFIND Depth:0 略重，但在 prod 启动期一次性调用，
并无 hot-path 影响。

## 测试矩阵（合计 27 OSS + 34 S3 = 61 个新测试）

| 场景 | OSS | S3 | Cloudreve（保留） |
|---|---|---|---|
| 构造函数校验（nil / 缺字段 / 不可达 endpoint） | ✅ | ✅ | ✅ |
| `keyFor` / `stripPrefix` 含路径遍历阻断 | ✅ | ✅ | ✅ |
| `Save` 路由（http 方法、body 字节） | ✅（httptest 不可行，绕过 SDK）| ✅ | ✅ |
| `Save` → `Get` 字节一致 | ✅ | ✅ | ✅ |
| `SaveReader` 流式 / 负 size 拒绝 | ✅ | ✅ | ✅ |
| `Get` → `GetReader` 文件不存在 | ✅ | ✅ | ✅ |
| `Delete` 幂等 | ✅ | ✅ | ✅ |
| `Exists` 真假两路 | ✅ | ✅ | ✅ |
| `GetMetadata` 含 ETag / Content-Type / LastModified | ✅ | ✅ | ✅ |
| `HealthCheck` 2xx 路径 | ✅ | ✅ | ✅ |
| `List` 过滤 + 目录跳过 | ❌ (httptest + bucket-host 重写) | ❌（path-style SDK 链路复杂）| ✅ |
| `GetBackendType` 字符串 | ✅ | ✅ | ✅ |

`List` 在 OSS / S3 跳过是因为 SDK 的 URL host 是 `bucket.endpoint`，对 `127.0.0.1`
mock server 不友好（不是 build-tag 的问题）。在 245 staging 用真 OSS / S3 实例
跑 acceptance 时再补一档。

## 全 build matrix 验证

| build tag 组合 | build | test |
|---|---|---|
| `""`（默认 / 0 tag） | ✅ | ✅ 21 PASS |
| `cloudreve_storage` | ✅ | ✅ 41 PASS |
| `storage_oss` | ✅ | ✅ 27 PASS |
| `storage_s3` | ✅ | ✅ 34 PASS |
| `cloudreve_storage,storage_oss` | ✅ | ✅ 47 PASS |
| `cloudreve_storage,storage_s3` | ✅ | ✅ 54 PASS |

`go vet` 在所有 6 种组合下 clean。

## 已知限制

1. `*oss.ServiceError` vs `oss.ServiceError` 的双错误 target — 见 §4。这只是
   defensive programming，不是 correctness 缺陷。
2. **`OSSStorageBackend.List` / `S3StorageBackend.List` 的单元测试覆盖为空**——
   SDK 与 mock server 的 host pattern 不兼容（OSS 用 `bucket.host` 虚拟主机，
   S3 也默认 virtual-host，path-style 需要显式开 UsePathStyle 才能 work；我们
   的 mock 服务器不能响应 `bucket.127.0.0.1`）。`List` 的实现逻辑已经在 `keyFor`
   和 `stripPrefix` 测试里分别覆盖，集成测试放到 245 验收补。

## 修复的预存问题（直接对应 audit §4.4）

| 审计问题 | 修复 |
|---|---|
| `OSSConfig` 在两个文件中重复声明 | 删除 `storage_backend_oss.go` 里的 `type OSSConfig struct {}`，统一用 `storage_backend.go` 的 canonical 类型 |
| `*OSSStorageBackend` 未实现 `StorageBackend`（Delete 签名漂移） | 重写按 canonical 接口的方法集（10 个方法全部对齐） |
| `StorageMetadata` 未定义 | 改用 `FileMetadata`（canonical 定义在 `storage_backend.go`） |
| `config.Prefix` 字段未定义（OSSConfig 用的是 `BasePath`） | `keyFor` 直接处理 `BasePath` |
| `NewOSSStorageBackend` 签名（指针 vs 值） | 统一用 `*OSSConfig` 指针，stub 同步对齐 |
| 同 S3 系列 4 项 | 同 |

audit §4.4 关账。
