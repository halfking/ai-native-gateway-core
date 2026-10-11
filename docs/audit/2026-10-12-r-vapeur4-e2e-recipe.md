# R-vapeur4 E2E 验证脚本（部署后跑）

> 适用：commit `ba1987a4d`（proactive chat→responses 升级）已部署到 245 / 154 之后。
> **目标**：用 `grok-4.6` / `hxt-local` / `vapeur` 经公网 `llm.kxpms.cn` 跑一发
> chat-shape 请求，从 `request_logs_hot` 抓 `request_id` + `upstream_endpoint`，
> 断言上游是 `/v1/responses` 而**不是** `/v1/chat/completions`，作为 R-vapeur4
> 修复生效的硬证据。

## 前置

- 245 / 154 已部署 `ba1987a4d` 或更新的 build_seq
- 公网到 `llm.kxpms.cn` 通畅（mesh 8782 / 8443 / 公网 443 SNI → 9443）
- `request_logs_hot` schema 在生产可读
- 有一把能调 `grok-4.6` 的 API key（admin token 拿的 session token 也行，
  只要模型权限覆盖 `grok-4.6`）

## 步骤一：发起 chat-shape 请求

```bash
# 拿 admin token（例）
ADMIN_TOKEN=$(curl -sS -X POST http://127.0.0.1:8782/api/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<admin-pass>"}' | jq -r .access_token)

# 拿到一个能调 grok-4.6 的 api_key 的明文（凭据解密 + 返回）
# 简化：用 admin token 通过 /api/admin/keys 临时签发一把 session-only key
KEY_RESP=$(curl -sS -X POST http://127.0.0.1:8782/api/admin/keys \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"r-vapeur4-e2e","models":["grok-4.6"],"expires_at":"2026-10-13T00:00:00Z"}')
KEY=$(echo "$KEY_RESP" | jq -r .key)
```

或者直接用现成的 `api_key_id=758`（vapeur hxt-local 上的可调账号）：

```bash
# 若已知道 plaintext sk-… 直接用
KEY='sk-<…vapeur-hxt-local…>'
```

发起请求：

```bash
RESP=$(curl -sS -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"grok-4.6",
    "messages":[{"role":"user","content":"Say hi in one sentence."}],
    "max_tokens":32,
    "stream":false
  }')
echo "$RESP"
```

预期：返回正常 chat 形态响应（带 `chat.completion` 信封）。
如果返回 5xx / 4xx，把响应 body 整段贴出来——但本验证的关键是日志里的 upstream_endpoint
而不是响应本身。

记下响应耗时（粗略），便于和日志 `latency_ms` 对照。

## 步骤二：从 request_logs_hot 抓 request_id + upstream_endpoint

请求会落在生产 PG。生产 PG 在 `172.16.2.210:5432`（DB-level）。生产侧查询：

```bash
PGPASSWORD='<prod-pg-pwd>' psql \
  -h 172.16.2.210 -p 5432 -U <prod-user> -d llm_gateway \
  -c "SELECT ts, request_id, outbound_model, provider_id, credential_id,
             upstream_protocol, upstream_endpoint, status_code, latency_ms,
             success, error_kind, protocol_conversion
      FROM request_logs_hot
      WHERE outbound_model='grok-4.6'
        AND provider_id=36
        AND credential_id=126
        AND ts > NOW() - INTERVAL '10 minutes'
      ORDER BY ts DESC LIMIT 5;"
```

预期（硬证据）：
- `upstream_endpoint` = `https://api.vapeur.ai/v1/responses` （**不是** `/v1/chat/completions`）
- `upstream_protocol` = `openai-responses`
- `protocol_conversion` = `t`
- `status_code` = 200
- `success` = `t`
- `latency_ms` 通常 < 5s（vapor `/v1/responses` 路径比 `/v1/chat/completions` 稳）

## 步骤三：兜底——gateway 日志对照

拿上一步的 `request_id`，到生产网关（245 或 154）的 `canary-8781.jsonl` 查：

```bash
ssh 245 'grep "<request_id>" /var/log/llm-gateway-go/canary-8781.jsonl | \
  jq -c "select(.msg == \"upstream_http_attempt\" or .msg == \"proactive chat→responses upgrade on verified capability\")"'
```

预期看到两行：

1. `proactive chat→responses upgrade on verified capability`，三字段：
   - `credential_id=126`
   - `raw_model=grok-4.6`
   - `provider_id=36`
2. `upstream_http_attempt`，关键字段：
   - `upstream_url` = `https://api.vapeur.ai/v1/responses`
   - `upstream_status` = 200

只要这两条都出现且上游 URL 是 `/v1/responses`，就证明 R-vapeur4 修复在生产上生效。

## 步骤五：失败诊断（如果 `/v1/responses` 没出现）

按以下顺序排查：

1. **网关 build 版本是否含 `ba1987a4d`**
   ```bash
   ssh 245 'strings /opt/llm-gateway-go/gateway | grep -E "R-vapeur4|proactiveChat" | head'
   ```
   - 命中：binary 含修复 → 继续 2
   - 不命中：当前 binary 不是 ba1987a4d，deploy 没把 commit 带上；需要重 deploy

2. **`credential_model_capabilities` 是否真有 grok-4.6 / cred 126 的 capability 行**
   ```bash
   ssh 245 'cd /opt/llm-gateway-go && \
     curl -sS http://127.0.0.1:8782/api/admin/credentials/126/capabilities \
     -H "Authorization: Bearer $ADMIN_TOKEN"'
   ```
   或直查 DB：
   ```sql
   SELECT capability, supported, last_tested_at, evidence_json
   FROM credential_model_capabilities
   WHERE credential_model_binding_id = 3483121;
   ```
   - 命中且 `supported=t`：capability 仍有效 → 继续 3
   - 命中且 `supported=f`：探针写过负向结论（stale 探针结果）；R-vapeur4 会跳过 proactive

3. **`providers.protocol` 是否还是 `openai-responses`**
   ```sql
   SELECT code, protocol FROM providers WHERE id=36;
   ```
   - `openai-responses`：继续 4
   - 别的（`openai-completions` 或别名）：catalogue 侧被改过；R-vapeur4 主动路径走不到

4. **`cand.SupportsNativeResponsesKnown` / `SupportsNativeResponses` 是否都为 `true`**
   - 这是 Candidate struct 的两个 bool 字段。如果 capability 表 `supported=t` 但
     候选者不知道（known=false），proactive 会跳过。生产代码读 capability 表是
     候选加载路径（provider/client.go:2022-2054）；如果路径上有 bug，写回 SQL 但
     struct 字段为 false。需要走 `provider/client.go:2022` 看是否取到。

5. **如果上游仍打 `/v1/chat/completions` 但 1/2/3/4 都正常**
   - 大概率是 executor 拿到的 `cand.SupportsNativeResponses` 字段值问题，
     或 `params.ResponsesBodyBytes` 在某个上游 hook 里被填了非空。
   - 在 `domains/streaming/executors/executor_chat.go:392-397` 加临时 `slog.Info`
     重新测一次确认。

## 步骤六：把硬证据归档

把以下贴到 `docs/audit/2026-10-12-r-vapeur4-deploy-verify.md`（不存在则建）：

```
## 245 deploy verify（2026-10-12 / build_seq=NNNN）

| 指标 | 期望 | 实测 |
|---|---|---|
| build_seq | > 2548 | NNNN |
| upstream_endpoint | /v1/responses | <…> |
| upstream_status | 200 | <…> |
| latency_ms | < 5000 | <…> |
| success | t | <…> |
| request_id | (uuid) | <…> |

(proactive chat→responses upgrade on verified capability 日志存在)
(upstream_http_attempt URL=https://api.vapeur.ai/v1/responses)
```

## 备注

- 经 `llm.kxpms.cn` 走公网；mesh 内则走 `https://llm.itestu.cn`
- 测试前后可以看下 `request_logs_hot` 该 cred 该 model 的失败率：
  ```sql
  SELECT date_trunc('hour', ts) h,
         COUNT(*) FILTER (WHERE success) ok,
         COUNT(*) FILTER (WHERE NOT success) fail
  FROM request_logs_hot
  WHERE outbound_model='grok-4.6' AND credential_id=126
    AND ts > NOW() - INTERVAL '24 hours'
  GROUP BY h ORDER BY h DESC LIMIT 24;
  ```
  预期 deploy 后 fail 计数不再上涨（修复后 chat 腿被绕过）。