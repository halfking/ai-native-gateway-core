# Prompt Budget Settings

## Change

`prompt_too_large` 的默认估算上限从 256k 调整为 1M tokens，并从固定环境变量升级为平台级系统配置：

- Key: `gateway.max_prompt_tokens`
- Default: `1048576`
- Range: `0..10485760`
- `0` 表示关闭限制
- `HotReload: true`

管理员可通过以下接口更新：

```http
PUT /api/admin/settings/gateway.max_prompt_tokens
Content-Type: application/json

{"value": 1048576}
```

请求入口使用 5 秒 TTL 读取缓存，更新后无需重启即可生效。优先级保持为 `settings_kv > LLM_GATEWAY_MAX_PROMPT_TOKENS > default`。没有初始化 settings registry 的测试或无 DB 部署仍使用环境变量回退。

## Scope

本次只调整 prompt 限制的配置入口和默认值，不修改 `request_logs_bodies`、`request_logs_bodies_hot` 或其他数据存储路径。数据存储优化另行评估和实施。

## Rollback

通过管理员接口将 `gateway.max_prompt_tokens` 设置为原目标值，或设置为 `0` 关闭限制。无数据库 schema 变更，无需 migration。

## Audit Fixes

提交后的审计补充了按 key 串行的 TTL 刷新、settings 写入后的显式缓存失效、registry 初始化时的缓存清理、DB-enabled 环境变量关闭别名解析，以及整数配置的小数拒绝校验。
