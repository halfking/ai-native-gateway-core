# 请求/会话统一详情 — UI 效果图

对应方案：统一请求/会话详情（含扩展读通路）。

| 文件 | 说明 |
|------|------|
| [request-detail-mockup-single.png](./request-detail-mockup-single.png) | 单请求模式 · 概览 / 数据源 / Tab |
| [request-detail-mockup-session.png](./request-detail-mockup-session.png) | 会话轮次模式 · 左时间线 + 右分面 |
| [request-detail-mockup-flow-security.png](./request-detail-mockup-flow-security.png) | 流程每环节耗时 + 压缩/脱敏三栏 |

## 读通路

1. 本机内存 meta  
2. 本机文件 `{LLM_GATEWAY_REQUEST_DETAIL_DIR}/{request_id}.json`  
3. `request_logs` / `request_logs_bodies`  
4. `session_turns` / `session_bodies`  

API：`GET /api/admin/request-detail/{request_id}`
