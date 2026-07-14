# Phase 2B Cloudreve Contract Blocker

日期：2026-07-14

## 结论

Phase 2B 目前阻塞。Cloudreve v4 源码证明了上传会话链，但没有证明存在一个可由 llm-gateway-go 直接依赖、稳定且已授权的通用 `POST /upload` API。当前不实现假 uploader，也不把 Cloudreve 的浏览器/用户 API 当成内部服务契约。

## 已确认的 Cloudreve v4 链路

1. 创建上传会话：`CreateUploadSessionService` 接收 `uri`、`size`、`mime_type`、`metadata`、`policy_id` 等字段，随后调用 `manager.CreateUploadSession`。
2. 凭据生成：`CreateUploadSession` 先准备 upload session，再由 storage driver 生成 credential；返回内容包含 session ID、过期时间、存储策略、回调 secret 和 URI。
3. 数据上传：本机/从机上传按 session ID 和 chunk index 进入分片路由；服务校验 Content-Length、chunk offset 和 session 所属用户。
4. 完成：最后一个分片调用 `CompleteUpload`，由存储 driver 完成上传。
5. 内容/缩略图：Cloudreve 使用 file-content 路由，并通过 `download=true`、`thumb=true` 查询参数区分下载和缩略图语义；这不是简单的上传响应 URL 契约。

## 证据

- `/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/service/explorer/upload.go:21-94`：创建会话请求字段及创建流程。
- `/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/service/explorer/upload.go:96-208`：session ID、chunk index、Content-Length、最后分片完成流程。
- `/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/pkg/filemanager/manager/upload.go:25-43,50-146`：session、driver credential、expires、callback secret 和 KV session 生命周期。
- `/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/pkg/filemanager/manager/upload.go:188-220,289-300`：driver 上传和 CompleteUpload。
- `/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/pkg/cluster/routes/routes.go:142-165,201-206`：上传分片、文件内容和缩略图 URL 构造。
- `/Users/xutaohuang/workspace/official-deploy/services/cloudreve/cloudreve/routers/router.go:447-542,699-709`：路由注册，显示 Cloudreve 内部/用户路由分开存在。

## 尚未确认的契约

- 网关到 files.kxpms.cn 的认证方式：JWT、服务 token、签名 header 或其他方式。
- 网关是否允许代用户创建 Cloudreve upload session，以及 `uri` 的稳定命名/租户隔离规则。
- multipart 或 chunk 上传的准确 Content-Type、header、响应 JSON schema 和错误码。
- credential 的 URL TTL、刷新规则和网关重试时是否可以安全复用。
- 完成响应中的 file ID、content URL、thumbnail URL、download URL 是否稳定，以及权限/签名 TTL。
- 文件、音频、视频、PDF 和通用文件的 MIME/缩略图/强制下载行为。

## 解阻塞条件

需要 files.kxpms.cn 所有者提供版本化 internal API contract，或在 gateway 仓库内实现并测试一个 gateway-owned internal API。最低契约应明确认证、multipart schema、响应 schema、幂等键、TTL、URL 权限、缩略图/下载行为和失败重试语义。契约测试应使用 `httptest.Server` 或等价的受控服务，不能只依赖 Cloudreve 内部实现细节。
