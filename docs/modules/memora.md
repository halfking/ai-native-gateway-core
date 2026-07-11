# Memora 记忆服务

Memora 将可复用的会话事实、偏好和提炼结果写入长期记忆，并在上下文压缩恢复时检索与当前任务相关的 L1 事实。所有检索都使用由租户、API Key 和任务组成的 scoped user ID，避免跨租户和跨任务混入记忆。

## 关联能力

- 会话缓存：提供跨请求会话状态。
- 会话压缩：在上下文恢复时使用 Memora L1 检索。
- 会话全景分析：为手动或后续自动提炼提供总结、主题和意图数据。
- kxmemory / Memora：写入 `/product/add`，可选使用 Dashboard 的 `/api/smart_search`。

## 启用

```bash
LLM_GATEWAY_MEMORA_BASE_URL=https://memora.example.internal
LLM_GATEWAY_MEMORA_API_KEY=...
# 可选：启用 kxmemory Dashboard 的 SmartSearch。
LLM_GATEWAY_MEMORA_SMART_SEARCH_BASE_URL=https://kxmemory.example.internal
LLM_GATEWAY_MEMORA_SMART_SEARCH_API_KEY=...
```

生产环境必须使用 HTTPS，避免将 Memora API key 通过明文 HTTP 传输。

## 运维接口

- `GET /api/system/memora-status`：连接状态和 Sink 统计。
- `POST /api/system/memora-ping`：连通性测试。
- `POST /api/system/memora-sink`：`{"action":"pause"}` 或 `{"action":"resume"}`。
- `GET /api/system/memora-query/{task_id}?q=...`：在任务所属租户范围内查询事实。
- `POST /api/system/session-context/{task_id}/extract-to-memora`：手动提炼会话事实。

暂停写入会拒绝新的写入并增加 Sink 的 `dropped` 统计；恢复后新写入继续进入异步队列。优雅关闭时 Gateway 最多等待 5 秒以排空队列。

## 当前边界

当前版本保留现有 Memora `/product/add` 写入契约和 Dashboard SmartSearch 读取契约。kxmemory 的会话摘要/意图写入接口尚未在本仓库声明稳定协议，因此不调用未验证的端点；会话总结与意图继续由会话全景分析模块持久化，Memora 通过手动提炼和异步写入保存可复用事实。
