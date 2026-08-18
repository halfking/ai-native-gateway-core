# Finding — 245 网关 `/metrics` 不暴露 `llmgw_credential_reveal_failure_total`

> 状态：**修复落地**（2026-08-18）。原始 P0 finding 见本文件 §Root cause §Fix。来源：2026-08-18 credential-17 加固链第十会话接力验证。
> 关联：
>   - `docs/handoff/2026-08-18-credential-17-fernet-ciphertext-fix.md`
>   - handoff: `/tmp/handoff-20260818-195012.md`（第九/十会话主交接）
>   - runbook: `deploy/prometheus/NATIVE-245-DEPLOY.md`
>   - fix commit: `3b6bdce18`（已 push 到 `origin/main`）

## 标题

245 网关 binary 含 `llmgw_credential_reveal_failure_total` 注册代码，但 `/metrics`
端点**未暴露该 metric**，导致 5 条 credential-reveal 告警规则恒为空。

## 严重级 / 影响

- 级别：**P0**（之前会话声明的"P0 已闭环"实际未闭环——runtime/规则加载/派发链路都验
  证了，但**告警可观察性本身未验证**）
- 影响：
  1. `llmgw_credential_reveal_failure_total` 在 245 prometheus 的
     `query_range` 永远返回 `result=[]`，5 条 rule 的 `rate(...)` 永远为 0/NaN
  2. 即便真实 credential reveal 失败（如 Fernet→AESGCM 迁移漏写、密文格式漂移），
     **告警永远不会触发**，等于 credential17 类事故的早期预警机制失效
  3. 真实 reveal E2E（P1 #2）的 metric 增量验证因此无法进行
- 不影响：promtool 语法校验、rule 加载、Alertmanager 派发链路

## 复现

```bash
# 在 245 上（前提：admin token 在 /opt/monitoring/prometheus/secrets/admin_token）
TOKEN=$(cat /opt/monitoring/prometheus/secrets/admin_token)

# 1) gateway 自身 /metrics（确认无 metric 暴露）
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8781/metrics \
  | grep -E "^# (HELP|TYPE) llmgw_credential_reveal_failure_total"
# → （无输出）

# 2) prometheus 是否 scrape 到（确认是 scrape-empty 而非 query-empty）
curl -s -G "http://127.0.0.1:9090/api/v1/query" \
  --data-urlencode 'query=llmgw_credential_reveal_failure_total' | jq .data.result
# → []

# 3) 控制 metric（确认 scrape 链路整体工作）
curl -s -G "http://127.0.0.1:9090/api/v1/query" \
  --data-urlencode 'query=dispatch_cred_queue_depth' | jq '.data.result | length'
# → 1（说明 scrape + token + TSDB 都正常）
```

## 根因分析（已收敛）

### 已排除

| 假设 | 验证手段 | 结果 |
|---|---|---|
| `/metrics` 401 拒 scrape | bearer_token_file 检查（`prometheus.yml:62`，`secrets/admin_token` 25B 0600） | ✅ 已正确配置，scrape UP |
| 网关没编译进 metric 代码（原始故障时） | `strings` on `releases/1614-ce8920cf/gateway` | ✅ 含 `"llmgw_credential_reveal_failure_total"` + `"provider.registerCredentialRevealMetrics"` |
| 源码缺失 metric 注册 | `git log -1 --format="%ai" e6bcf4723` → 2026-08-18 14:17；`6b083b70` 在 17:30，`ce8920cf` 更晚 | ✅ main 上源码完整 |
| 网关进程跑旧 binary（原始故障时） | `/proc/$(pgrep gateway)/exe` 验证 | ✅ 原始 PID 使用 `1614-ce8920cf/gateway` |
| registry 不匹配（default vs 私有） | 源码 `middleware/prometheus_mw.go:37-39` 是 `promhttp.Handler()` → `HandlerFor(DefaultGatherer, …)`；repo 无 `prometheus.NewRegistry()` | ✅ 共享同一 default registry |
| reveal 路径从未执行，0 值 lazy-export skip | —— | ❌ **不成立**：prometheus/client_golang 的 *Vec 类型**只要没有 child，gather 完全不输出**（连 HELP/TYPE 都不在）——不是 lazy，而是 absent |

### 根因（100% 确定）

**`prometheus/client_golang` 的 CounterVec（及所有 *Vec 类型）只有当**至少一个 child 系列
通过 `WithLabelValues(...).Inc()/.Add(...)` 创建后，gather 才会输出 MetricFamily。
`MustRegister` + 无 child = **整个 MetricFamily 在 gather 输出中完全不存在**。

证据：本地复现：
```go
// 1. 仅 MustRegister，无 child
prometheus.MustRegister(NewCounterVec(Opts{Name:"x"}, []string{"a"}))
gather() → 不含 x

// 2. 加一个 WithLabelValues + Add(0)
counter.WithLabelValues("a").Add(0)
gather() → 含 x，1 metric，Help + Type + series
```

credential_decrypt_metrics.go 旧版只有 `MustRegister`，没有 `Add(0)` pre-warmup。
metric 又是**仅在 error path Inc**（healthy traffic 不触发）—— 245 自 17:50 启动以来
没有 reveal failure 事件，metric 完全从未被 Inc，gather 输出完全不含它。

对比同包内 `candidate_diagnostic_metrics.go` 的正确做法：
```go
func registerCandidateDiagnosticMetrics() {
    ...
    prometheus.MustRegister(candidateDiagnosticMetrics)
    initializeCandidateDiagnosticMetrics()  // ← pre-warm with Add(0)
}
func initializeCandidateDiagnosticMetrics() {
    for _, event := range candidateDiagnosticEvents {
        candidateDiagnosticMetrics.WithLabelValues(event).Add(0)
    }
}
```
这就是为什么 `llmgw_routing_candidate_diagnostics_total` 在 /metrics 上看得到，而
`llmgw_credential_reveal_failure_total` 看不到。

## 修复方案（已落地）

修改 `provider/credential_decrypt_metrics.go`：

1. 暴露公开函数 `RegisterCredentialRevealMetrics()`（保留 init() 兼容路径）
2. 注册后**循环 7 个 closed-vocabulary reason** 调用 `WithLabelValues("0", reason).Add(0)`：
   - `provider_id="0"` 是 sentinel —— 让 pre-warm series 与真实 provider_id 系列共存
     且不冲突
   - dashboard 可过滤 `provider_id!="0"` 排除 pre-warm
3. pre-warm 移出 sync.Once 包住（每次调用都重做）—— 这样 `Reset()` 之后再次调用能
   恢复 pre-warm（test fixture 友好）
4. sync.Once 仍包 CounterVec 构造，避免双重 `MustRegister` panic

修改 `cmd/gateway/main.go`：

在 `metrics.SetGlobal(metrics.NewPrometheusRecorder())` 之后**显式调用**
`provider.RegisterCredentialRevealMetrics()` —— 让 wiring 在 main 启动路径上可审计，
不再依赖隐式 init 顺序。sync.Once 保证 idempotent。

新增 `provider/credential_decrypt_metrics_test.go`：

- `TestCredentialRevealMetricVisibleAfterPrewarm`：显式 gather 检查 7 个 pre-warm series 存在
- `TestRegisterCredentialRevealMetricsIdempotent`：多次调用不 panic 且 metric 仍可见

## 验证（修复后）

1. 本地 `go test ./provider/ -count=1`：全部 7 个测试 pass（含 5 个原有 + 2 个新增）
2. 本地 build `cmd/gateway`：成功
3. 245 部署：scp binary + systemctl restart llmgo-245
4. ssh 245：
   ```bash
   TOKEN=$(cat /opt/monitoring/prometheus/secrets/admin_token)
   curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8781/metrics | grep "^llmgw_credential_reveal_failure_total"
   # 期望：2 行 HELP/TYPE + 7 条 `llmgw_credential_reveal_failure_total{provider_id="0",reason=...} 0` 系列
   ```

## 引用

- 真实 metric 注册代码（修复后）：`provider/credential_decrypt_metrics.go`
- main 显式调用：`cmd/gateway/main.go`（`metrics.SetGlobal` 之后）
- 修复 commit：`3b6bdce18`（已 push 到 `origin/main`）
- 当前 245 binary：`/opt/llm-gateway-go/releases/1617-3b6bdce18/gateway`（Linux x86_64，systemd `llmgo-245.service`）
- systemd unit：`/etc/systemd/system/llmgo-245.service`
- 告警规则：`deploy/prometheus/rules/credential-reveal-failures.yml`（5 条）