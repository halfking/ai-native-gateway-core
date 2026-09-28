# Handoff — reasoning_effort 收窄链路批判复审（2026-09-29）

> 上一轮见 `docs/audit/2026-09-28-reasoning-effort-clamp-audit.md`。
> 本文件只覆盖本轮（09-29 00:55–02:xx）的事实、改动、未决项；上一轮
> 自身真实度修正已并入审计文档 §5/§6/§7，本文件不再重复。

## 1. 一句话状态

本轮没找到修复本身的 bug，但发现两个**功能性缺陷**：① `metrics/empty_response_metrics.go`
的 `RecordEmptyResponseAttempt` 自 R45 落地以来**零调用点**（空响应观测面静默失效）；
② 方舟方言上的 `reasoning_effort:"disabled"` 不是"关闭"，是"档位调到最便宜"——
方舟的真正关闭开关是 `thinking:{type:disabled}`，前者会被方舟照常收思考成本。
本次提交 `ee75da15a` 修了①并守门②，没改 29d326f10 的修复逻辑；GLM/方舟的
"档位下移"边界已在 §5 列出，作为已知语义限制。

## 2. 关键事实（均绑定 git_sha，换一轮即需重取）

| 项 | 值 | 取证方式 |
|---|---|---|
| HEAD（提交时） | `ee75da15a` | `git rev-parse` |
| HEAD（提交前） | `3a30afd94` | `git log --oneline -1` |
| 对照组镜像（8782） | `kx-llm-gateway-local:2.5.6.2308`（构建 23:04，早于修复 23:54） | `docker inspect` |
| 实验组镜像（旁挂 18784） | `kx-llm-gateway-local:reasonfix-20260929-v2`（vcs.revision `0d3515ffa`） | `docker inspect` |
| 旁挂实例 listen | `:8784` (host 18784), `LLM_GATEWAY_BG_MODE=data-plane` | `docker inspect` |
| 8782 在 01:13:57Z 被另一流程升级 | 镜像 → `2.5.6.2314`，二进制 md5 `3016d06…` | `docker inspect`（非本轮操作） |
| 真机 A/B 时窗 | 17:07–17:11Z（n=12，NEW+OLD 各 6 条），早于 8782 升级 | 本轮日志 |
| `/metrics` 鉴权 | Bearer token 401；`/admin/metrics` 200 但本轮 admin key 未取到 metrics 行 | curl |
| `request_logs`/`request_logs_bodies` | `max(ts) = 17:02:41+08`，**早于**本轮 A/B 起点 | SQL |

## 3. 本轮改动（commit `ee75da15a`）

| 文件 | 性质 | 说明 |
|---|---|---|
| `domains/streaming/handler.go` | **真修复（最小面）** | `streamErrorKindForDetailCode` 在 `empty_stream_no_content`/`early_empty_detection` 归一处串接 `metrics.RecordEmptyResponseAttempt(detailCode)`。接线位置选归一层而不是源头（stream.go / responses_bridge.go 多入口），修改面最小，metrics 层做归一化（done_no_content/early_empty/other）。 |
| `internal/paramguard/reason_effort_dialect_test.go` | **新增守门** | `TestArk_EffortIsTierNotKillSwitch`：钉死方舟 `doubao-pro-thinking` 能力表 `{minimal,low,medium,high}` 无 none 零档；断言 `reasoning_effort:"disabled"` → `minimal` 且字段必存在、必有 clamp 报告。防止未来有人把方舟走"零档=删字段"路径。 |
| `docs/audit/2026-09-28-reasoning-effort-clamp-audit.md` | 文档 | §5 重写为"结构性不可达"（catalogToDialect 缺口在 R45 SSOT 已登记）；§7 新增真机 A/B 三态表 + 逐档 max_tokens 实测（64/3, 128/1, ≥256/0）。 |

## 4. 三条必须带走的纠正

1. **"live 影响为零"≠"巧合无影响"**。原因结构在 `internal/paramreg/dialect.go:catalogToDialect`：本环境 `code∈{zhipu,glm,deepseek,doubao,volcengine,volcano,ark}` 全部不在映射表，调用 `Resolve(code, protocol)` 回退协议得到 `DialectOpenAIChat` 或根本不经 paramguard-effort 路径。glm-5.2 真机落到 provider 36（vapeur），deepseek-v4-flash 落到 provider 33089（sensenova），与方舟方言不同源。
2. **"max_tokens=128 必空"是错判**。真实边界在 ~100–200 token：64 档 3/3 空、128 档 1/3 空、≥256 档 0/3 空。本轮重新采了 n=15，每档均 100~300 字符 reasoning。引用"128 必空"作确定性结论之前必须重测。
3. **方舟 `reasoning_effort:"disabled"` 不是关闭**。方舟真正关闭思考是 `thinking:{type:disabled}`（实测 reasoning_tokens=0 / 0.9s）。"disabled" 在方舟被收窄到 `minimal`（最便宜档），方舟**仍会思考**，只是档位最低。客户端若想真关，必须显式发 `thinking{type:disabled}` 字段——网关不会替它翻译。

## 5. 已知语义限制（不是新发现，复述以便下游使用）

- 本轮修复让"关闭意图"在所有四族方言上都收窄为最便宜档。这对 GLM/DeepSeek 是**净改进**（避免 medium/high 反转）；对方舟是**保真**（方舟本身就没有零档，"minimal"是它能给的最低）。
- **没改的代码**：29d326f10 的两处修复本身没有任何代码改动。`Ark_EffortIsTierNotKillSwitch` 是新增测试，不是改主逻辑。
- **`catalogToDialect` 缺口**维持 R45 立场："登记不修+逐上游真机验证接受度"。本环境 `provider 7 (doubao)` 0 凭据，无法真机验证；本轮不动该缺口。

## 6. 未决风险（勿当成已解决）

1. **8782 在 17:13:57Z 被升级到 `2.5.6.2314`**——不是我操作；升级可能引入其它未审计改动。若新一轮想用 8782 作对照组，需重做 2308 镜像回滚或换新对照。
2. **`/metrics` 端点鉴权在 18784 上行为不一致**——`GET /metrics` 401，`GET /admin/metrics` 200。本轮没取到 `llm_gateway_empty_response_attempts_total` 的非零值，**接线是否真的触发未用真机空响应探针证实**。仅靠单测覆盖（metrics 包内）。
3. **本环境未制造真"空响应"**——GLM max_tokens=64 触发的是 `finish_reason=length` + content 空，**不是** stream 层的 empty_stream_no_content。空响应探针的真实触发需要：上游返回 200 但首字节即 `[DONE]`（stream.go:235）或早空检测命中（stream.go:392）。本轮未做此形态的端到端验证——记录这块是真机验证的盲区。
4. **`reasoning_effort:disabled` 在方舟上仍是档位下移**。如果用户后续依赖这个取值来"关闭思考"而客户端又未带 `thinking{type}` 字段，方舟会照收思考成本。客户端应该自行带 `thinking{type:disabled}`。
5. **`ReasonDialectMatchesParamDialect` 在 anthropic-messages 路径完全不会被调用**（paramguard 全仓唯一调用点是 `domains/streaming/executors/paramledger_integration.go`，而 anthropic 走 `domains/streaming/messages.go`），所以 Ark/GLM/DeepSeek 即使将来被映射到 anthropic 方言，也**不会**经 effort 收窄路径——这是双路径分裂，应留意不与本轮修复混为一谈。

## 7. 下一轮提示词（接力要点）

- 若接力方拿到的是本 commit `ee75da15a` 之后的世界，先 `git fetch origin main && git log HEAD..origin/main --stat` 看上游是否已把 `RecordEmptyResponseAttempt` 接线做到位或换实现位置。
- 若接力方想做"方舟线上是否真接 reasoning_effort 字段"的取证：先解决 `provider 7` 0 凭据问题（让本环境能路由到 volcengine）。可走 volcano-tokenplan / volcano-normal（已有活跃凭据），但注意它们 catalog code 不是 ark——必须先在 catalogToDialect 加 `volcano-tokenplan→DialectArk`、`volcano-normal→DialectArk`，再走真机探针；**任何 catalogToDialect 改动必须先 R45-style 登记"逐上游真机验证接受度"门**。
- 若接力方想动 29d326f10 的 `disableIntentEfforts` 词表或 `ClampEffort`：先看 `internal/reasonnorm/clamp_disable_intent_test.go` 与本轮新增的 `TestArk_EffortIsTierNotKillSwitch`。方舟那条是**契约**不是测试偏好，改之前必须确认方舟能力表是否引入 `none` 零档（`reasoning_defaults.go:249`）。
- 若接力方想做"max_tokens 与 glm 思考预算"的真机边界探针：保留本轮 §7 表 2 的判据——128 不再是确定性边界，按 ~100~200 token 实测消耗分布来决定客户端语义层怎么做提示。

## 8. 命令清单（可重放）

```bash
# 旁挂实例
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor -ldflags="-s -w" \
  -o .build-local/llm-gateway-go ./cmd/gateway
docker build -f .build-local/Dockerfile.reasonfix \
  -t kx-llm-gateway-local:reasonfix-20260929-v2 .build-local

# 健康/真机
KEY=$(cat /tmp/llmgw-key.txt)   # sk-auto-audit-20260914
curl -s http://127.0.0.1:18784/healthz
/tmp/ab_probe.sh glm-5.2 18784 NEW   # /tmp/ab_run.sh <model> <n> <gap>
/tmp/mt_probe.sh glm-5.2             # max_tokens 逐档 n=3

# 回归
go test -count=1 ./internal/reasonnorm/... ./internal/paramguard/... \
  ./internal/reasoncap/... ./internal/paramreg/... ./metrics/... ./domains/streaming/

# SQL 取证
docker exec llm-gateway-pg psql -X -q -U llm_gateway -d llm_gateway -c \
  "select ... from provider_models pm join providers p on p.id=pm.provider_id \
   where pm.standardized_name='glm-5.2' ..."
```