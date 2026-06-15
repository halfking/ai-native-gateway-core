# Session → Memora 提炼管线

> MVP 2026-06-15：规则提炼 + 手动触发；LLM 提炼与全自动沉淀为后续阶段。

## 目标

从 llm-gateway 会话上下文（`request_logs` 预览）中提取**有用事实**，写入 Memora L1 会话记忆，并附带 `project_id` 元数据供后续 L2+ 沉淀。

## 数据流

```
session-context 详情页
    │ POST /api/system/session-context/{taskId}/extract-to-memora
    ▼
handleSessionExtractToMemora
    ├─ SELECT request_logs WHERE gw_task_id = taskId  (对话线索)
    ├─ GET 已有 Memora facts (Search, user_id=k:{api_key_id}:{task_id})
    ├─ memora.ExtractFromPreviews() 规则过滤 + 去重
    ├─ memora.Client.AddMessage() → /product/add
    └─ INSERT session_memora_extraction_log
    ▼
Memora 异步索引 → L1 会话事实 → (可选) distill_knowledge 沉淀 job
```

## 触发方式

| 方式 | MVP | 说明 |
|------|-----|------|
| 手动按钮「提炼入 Memora」 | ✅ | 详情页 `SessionContextDetailView` |
| 列表批量 | ⏳ | 后续：勾选多 task_id |
| 后台 worker | ⏳ | 会话 idle N 小时后自动提炼 |

## 过滤规则（有用 vs 格式噪音）

**跳过（噪音）**：
- 空/过短（< 24 字符）
- 纯 markdown 壳：` ``` ` 围栏、HTML 实体、仅标题
- 系统 prompt 重复：`You are`, `Follow ALL`, `<user_query>`, `AGENTS.md` 块
- Tool call 元数据：JSON `tool_call` / `function` / `"type":"tool"`
- 运维格式行：`exit_code`, `pid:`, `cwd:`, `---` 分隔符块
- 与已有 Memora fact 高度重叠（子串匹配，忽略空白）

**保留（有用）**：
- 用户实质性提问/指令（prompt_preview，direction=user）
- 助手结论性回复（response_preview，≥ 40 字符，非纯代码块）
- 部署结论、配置变更、错误根因等含关键词片段

## API 契约

### POST `/api/system/session-context/{taskId}/extract-to-memora`

**权限**：super_admin（与会话上下文其他接口一致）

**请求体**（可选 JSON）：
```json
{
  "dry_run": false,
  "include_responses": true
}
```

**响应 200**：
```json
{
  "task_id": "abc-123",
  "user_id": "k:42:abc-123",
  "project_id": "kaixuan-1-deploy",
  "written": 3,
  "skipped_noise": 12,
  "skipped_duplicate": 2,
  "memora_message_ids": [],
  "extracted_at": "2026-06-15T13:00:00Z",
  "samples": ["部署 llm-gateway-go 184 成功，healthz 200"]
}
```

**错误**：400（无 task_id）、404（task 不存在）、503（Memora 未配置）、502（Memora 写入失败）

### GET `/api/system/session-context/{taskId}/extraction-status`

返回最近一次 `session_memora_extraction_log` 记录（`extracted_at`, `written`, `status`）。

## 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `LLM_GATEWAY_MEMORA_BASE_URL` | — | Memora `/product/add` 基址（已有） |
| `LLM_GATEWAY_MEMORA_API_KEY` | — | Bearer token（已有） |
| `MEMORA_PROJECT_ID` | `kaixuan-1-deploy` | 写入 info.project_id |

## 持久化

表 `session_memora_extraction_log`（gateway PG，启动时 ensure）：

| 列 | 类型 | 说明 |
|----|------|------|
| task_id | TEXT PK | gw_task_id |
| extracted_at | TIMESTAMPTZ | 最近提炼时间 |
| written | INT | 写入条数 |
| skipped_noise | INT | 跳过噪音数 |
| skipped_duplicate | INT | 跳过重复数 |
| status | TEXT | ok / partial / error |
| detail | JSONB | 样本片段、错误信息 |

## 未做项（后续）

- LLM 提炼（复用 gateway compaction 模型）
- `POST .../sediment` 调用 Memora `distill_knowledge` MCP
- 列表页批量提炼
- 自动 idle worker

## SSOT 迁移（Phase 4）

Gateway 当前在进程内调用 `memora.ExtractFromPreviews` + `Client.AddMessage`（直连 MemOS `/product/add`）。

**目标**：写入统一走 Memora Dashboard `POST /api/sessions/ingest`（见 `services/kxmemory/docs/session-ingest-from-agents.md`），gateway 仅负责：
1. 从 `request_logs` 组装 `preview_turns`
2. 可选本地 `dry_run` 对比
3. 调用 ingest API，`source=gateway`

Memora 侧过滤规则已与 `memora/extract.go` 对齐（`session_ingest.py`）。
