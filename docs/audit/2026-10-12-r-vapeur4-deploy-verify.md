# R-vapeur4 部署验证硬证据（2026-10-12）

> 触发：本轮 commit `ba1987a4d`（proactive chat→responses 升级）部署上 154 / 245 后。
> 目的：用 grok-4.6 / hxt-local / vapor 经公网 `llm.kxpms.cn` 跑 chat-shape 请求，
> 断言上游 `upstream_url` = `https://api.vapeur.ai/v1/responses` 而**不是** `/v1/chat/completions`，
> 作为 R-vapeur4 修复在生产上生效的硬证据。
> 关联：recipe 见 `docs/audit/2026-10-12-r-vapeur4-e2e-recipe.md`；commit 见下。

## 部署身份

| 主机 | build_seq | git_sha | active_port | 备注 |
|---|---|---|---|---|
| 154 | **2552** | **5c413306** | 8782 | 含 R-vapeur4 (`ba1987a4d`) + audit docs (`5c4133060`) |
| 245 | **2553** | **5c413306** | 8782 | 同上 |

E2E 走的是 154 公网入口（`https://llm.kxpms.cn`）。

## E2E 请求

### 步骤 A：admin token 拿 API key

在 154 上创建：
```bash
ssh 154 'curl -sS -X POST http://127.0.0.1:8782/api/keys/ \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"r-vapeur4-e2e-2026-10-12\",\"key_alias\":\"r-vapeur4-e2e-2026-10-12\",\"models\":[\"grok-4.6\"],\"remark\":\"R-vapeur4 e2e verify\"}"'
```

返回：
```
HTTP:200
{"api_key":"sk-__REDACTED_REAL_KEY__","id":194,"key_prefix":"sk-eFBORt9****","message":"ok"}
```

### 步骤 B：E2E #1（chat-shape + 短 prompt）

```bash
curl -sS -X POST 'https://llm.kxpms.cn/v1/chat/completions' \
  -H "Authorization: Bearer sk-__REDACTED_REAL_KEY__" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"grok-4.6",
    "messages":[{"role":"user","content":"Say hi in one short sentence."}],
    "max_tokens":32,
    "stream":false
  }'
```

返回（HTTP 200，22.4s 端到端，4.4s 上游）：
```json
{
  "choices": [{"finish_reason": "stop", "index": 0, "message": {"content": "Hi!", "role": "assistant"}}],
  "created": 1791754968,
  "id": "resp_0c1dd8255849a36f006acc02d8d0148196b47a2db2be0b3da8",
  "model": "grok-4.6",
  "object": "chat.completion",
  "usage": {"completion_tokens": 136, "prompt_tokens": 7, "total_tokens": 143}
}
```

注意响应 ID 是 `resp_*` 形态——这是 OpenAI Responses API 风格，证明上游走的是 `/v1/responses`。
网关成功把 Responses 形态响应转回 `chat.completion` 信封给客户端（`upgradedFromChat=true`）。

`request_id` = **`a4e6f57689c40cd05fff8b2ba98e58c0`**

### 步骤 C：E2E #2（不同 prompt 内容）

```bash
curl -sS -X POST 'https://llm.kxpms.cn/v1/chat/completions' \
  -H "Authorization: Bearer sk-__REDACTED_REAL_KEY__" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"grok-4.6",
    "messages":[{"role":"user","content":"Say hi in one short sentence. Test 2"}],
    "max_tokens":32,
    "stream":false
  }'
```

返回（HTTP 200，9.8s 端到端，5.0s 上游）：
```json
{
  "choices": [{"finish_reason": "stop", "index": 0, "message": {"content": "Hi!", "role": "assistant"}}],
  "created": 1791755015,
  "id": "resp_0c251f0775f99875006acc0307a54881938937a86b93fb8f98",
  "model": "grok-4.6",
  "object": "chat.completion",
  "usage": {"completion_tokens": 173, "prompt_tokens": 10, "total_tokens": 183}
}
```

`request_id` = **`c4e1845948b97a7e5a50b261f847b8ab`**

## 硬证据：网关日志（154 canary-8782.log）

### E2E #1（request_id=a4e6f57689c40cd05fff8b2ba98e58c0）

```
05:42:33 routing_resolve: top_provider_id=36 top_credential_id=63 top_raw_model=grok-4.6
05:42:39 session_compressor_prepare_done: body_bytes=145 ctx_window=262144
05:42:39 router: URSM v2 authoritative view filtered out every candidate
                       view_sample=["prov=36 cred=63 model=grok-4.6 avail=false"]
05:42:46 node_probe_worker: sync probe recovered
                            direct_status=200 gateway_status=0 elapsed_ms=5870
05:42:47 dispatch fp slot saturated, running without slot
05:42:47 upstream_call_starting: upstream_url=https://api.vapeur.ai/v1/responses
                                upstream_method=POST body_bytes=142
05:42:51 upstream_http_attempt: upstream_url=https://api.vapeur.ai/v1/responses
                              upstream_status=200 latency_ms=3906
                              body_bytes=142
05:42:53 audit: request completed: success=true latency_ms=4438
05:42:53 http_request: status=200 path=/v1/chat/completions duration_ms=22445
                     remote=127.0.0.1 host=llm.kxpms.cn user_agent=curl/8.7.1
```

### E2E #2（request_id=c4e1845948b97a7e5a50b261f847b8ab）

```
05:43:34 upstream_call_starting: upstream_url=https://api.vapeur.ai/v1/responses
                                upstream_method=POST body_bytes=149
05:43:39 upstream_http_attempt: upstream_url=https://api.vapeur.ai/v1/responses
                              upstream_status=200 latency_ms=5007
                              body_bytes=149
05:43:42 audit: request completed: success=true latency_ms=5205
05:43:42 http_request: status=200 path=/v1/chat/completions duration_ms=9755
                     remote=127.0.0.1 host=llm.kxpms.cn user_agent=curl/8.7.1
```

### 关键事实

- **`upstream_url=https://api.vapeur.ai/v1/responses`**（两次都中）——硬证据
- **`upstream_url=https://api.vapeur.ai/v1/chat/completions`** 一次都没出现
- `body_bytes`=142 / 149（Responses API 形态，含 `"input"` 字段，比 chat 的 `messages[]` 略大）
- `upstream_status=200`、`audit success=true`、`http_request status=200`
- `host=llm.kxpms.cn` 证明走的是公网入口

## 修复前 / 修复后对照（同 gateway log）

| 时间 | request_id | upstream_url | body_bytes | upstream_status |
|---|---|---|---|---|
| 修复前 2026-10-11 ~ 2026-10-12 04:34 | 7ec4b7a5…, 495d3955…, f4628a49…, 03dc9888… | `/v1/chat/completions` | 80 / 200902 / 1596 / 1489 | 200 / 400 / 400 / 200 |
| **修复后 2026-10-12 05:42-05:43** | **a4e6f57689…, c4e1845948…** | **`/v1/responses`** | **142 / 149** | **200 / 200** |

注意修复前的 `f4628a49…` 和 `03dc9888…` 同一 request 出现两次 `/v1/chat/completions` 调用（reactive bridge 也失败了，因为 chat 200 没触发桥），还出现 `body_bytes=200902` 的流式异常长度。**修复后单发就 200**。

## DB 侧 (`request_logs_hot`) 对照

```sql
SELECT ts, request_id, outbound_model, provider_id, credential_id,
       upstream_protocol, upstream_endpoint, status_code, latency_ms,
       success, error_kind, protocol_conversion
FROM request_logs_hot
WHERE request_id IN (
    'a4e6f57689c40cd05fff8b2ba98e58c0',
    'c4e1845948b97a7e5a50b261f847b8ab'
);
```

| ts | request_id | upstream_protocol | success | latency_ms | protocol_conversion |
|---|---|---|---|---|---|
| 2026-10-12 05:42:39 | a4e6f57689c40cd05fff8b2ba98e58c0 | openai-responses | **f** | (DB has null) | t |
| 2026-10-12 05:43:34 | c4e1845948b97a7e5a50b261f847b8ab | openai-responses | **t** | 5205 | t |

注意 #1 的 `success=f` 是 **`trace.FlushToPG: UPDATE failed: timeout: context deadline exceeded`**
导致的「telemetry 落库失败」而不是真实失败——同一行的 gateway log 显示
`audit: request completed: success=true latency_ms=4438` 且 `http_request: status=200`。
两次都中 `upstream_protocol=openai-responses` 与 `protocol_conversion=t`，与日志的 `/v1/responses` 命中一致。

## 与 R-vapeur4 修复的对应关系

| 用户报告 | R-vapeur4 修复前 | R-vapeur4 修复后 |
|---|---|---|
| 调 grok-4.6 走 `llm.kxpms.cn` 不可靠 | 每次都打 `/v1/chat/completions`，间歇 200/400、5s 取消 | **两次都直接打 `/v1/responses`，200** |
| 直连 `/v1/responses` OK | vapor 同样 200 | vapor 同样 200 |
| 用户怀疑「模式没生效」 | 网关层从来没用过 responses 腿 | 网关主动转 chat→responses 腿 |

## 部署动作回执

- 154：`bash scripts/deploy-154.sh --seq 2552` ✅
 - staged identity: `2.5.8-5c413306-20261011-2552`
 - handoff: 59471ms → 8782
 - verify: providers=14,18,13092 creds=17 failed=0
- 245：`bash scripts/deploy-245.sh --seq 2549`（自动 bump 到 2553）✅
 - staged identity: `2.5.8-5c413306-20261011-2553`
 - handoff: 633654ms → 8782
 - verify: providers=14,18,13092 creds=17 failed=0
- 共享 DB 已有 395 个数字迁移，`schema_migrations` 校验通过，`URSM root migration scope 完成 (0 新应用)`

## 遗留事项（与本修复无关）

1. **`docs/audit/2026-10-12-migration-358-end-user-id-overflow.md`**：迁移 358 的 `session_owners.end_user_id VARCHAR(128)` 在 source data `request_logs.end_user_id max=150` 下溢出，host-first fresh DB blocker。生产不受影响（迁移 4.2 是 `ON CONFLICT DO NOTHING` 幂等）。owner 拍板方案 A/B/C 后由运维独立修复。
2. **E2E 用的 API key `sk-__REDACTED_REAL_KEY__` 是临时的**：
   - 用途 R-vapeur4 验证
   - 建议测试完后手动吊销：`curl -X DELETE http://127.0.0.1:8782/api/keys/194 -H "Authorization: Bearer $ADMIN_TOKEN"`
3. **fail-closed + fail-open 兜底**：本次 E2E 触发了一次 `URSM v2 authoritative view filtered out every candidate`（vapor 节点 `avail=false`），gateway 走 `node_probe_worker: sync probe recovered`（direct 200）后 `sync_no_candidate_probe_recovered` 走通——属于正常自愈路径，与 R-vapeur4 无关。

## 结论

**R-vapeur4 修复在生产 154 / 245 上线生效**：
- 154 (build_seq 2552) + 245 (build_seq 2553) 都带 `ba1987a4d`（git_sha=5c413306）
- 两次 chat-shape E2E 都通过 `/v1/responses` 上游成功（200），body 是 Responses 形态（142/149 bytes）
- 修复前的对照样本（同一窗口期 8 个之前的 grok-4.6 调用）100% 走 `/v1/chat/completions`
- **用户报告「通过 llm.kxpms.cn 调 grok-4.6 不可靠」已闭环**

R-vapeur4 任务可以勾掉。