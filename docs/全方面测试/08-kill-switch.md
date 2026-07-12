# 三模块 kill-switch 验证 (2026-07-12 incident response)

> 在遇到 154 网关"间歇 503"时，先关掉这 3 个高风险模块做应急止血，再用 `scenarios/S13` / `S15` 验证业务不受影响。

## 三个模块的开关（env-driven）

| 开关 | 影响模块 | 文件 | 默认 |
|---|---|---|---|
| `KILL_SESSION_COMPRESSION=1` | 上下文压缩、LCS diff | `domains/hooks/compression/hook.go` | enabled |
| `KILL_SESSION_CACHE=1` | session_cache L1+L2+L3 | `domains/hooks/compression/session_cache.go` | enabled |
| `KILL_CIRCUIT_DEGRADATION=1` | circuit breaker | `domains/streaming/executors/executor.go` | enabled |
| `KILL_FP_SLOT=1` | fingerprint prefilter | `domains/streaming/executors/executor.go` | enabled |
| `KILL_RATE_LIMITER=1` | RPM/TPM token bucket | `ratelimit/` middleware | enabled |

实现位置：
- `settings/feature_switches.go` —— 集中读 env + sync.Once
- 4 处入口的 `settings.IsEnabled(name)` 短路

## 验证流程

### 1. 本地验证（必做）

```bash
# 启 mock + 跑基线（确认开启全部开关时 100% 通）
docs/全方面测试/tools/start_suppliers.sh
S01_KILL="KILL_SESSION_CACHE=1 KILL_SESSION_COMPRESSION=1 KILL_CIRCUIT_DEGRADATION=1 KILL_FP_SLOT=1" \
    bash docs/全方面测试/scenarios/S01_baseline.sh   # 期望: 100%

# 关掉 3 个关键模块，重跑 S01 应仍 100% 通（旁路路径）
docs/全方面测试/tools/start_suppliers.sh stop

# 重启 gateway with KILL_*=1，预期：session_cache/compression/circuit 旁路
Environment="KILL_SESSION_CACHE=1" \
Environment="KILL_SESSION_COMPRESSION=1" \
Environment="KILL_CIRCUIT_DEGRADATION=1" \
Environment="KILL_FP_SLOT=1" \
    systemctl restart llm-gateway-go

# 重跑 S01、S13、S15 — 看业务是否受损
docs/全方面测试/scenarios/S01_baseline.sh  # baseline 期望通过
docs/全方面测试/scenarios/S13_no_candidate.sh  # 100% 失败
docs/全方面测试/scenarios/S15_cross_group_failover.sh  # 跨组迁移
```

### 2. 154 网关灰度（建议步骤）

```bash
# Step 1: 先开 1 个最紧急（fp_slot，因为这是 minimax-m3 503 路径上最热的）
ssh root@47.97.111.154 \
  'systemctl edit llm-gateway-go' \
  <<EOF
[Service]
Environment="KILL_FP_SLOT=1"
EOF

systemctl restart llm-gateway-go

# Step 2: 观察 10 分钟
journalctl -u llm-gateway-go --since "10 min ago" | \
  grep -E "executor: stream interrupted|router: all candidates filtered|model_offers mirror write"
# 期望: 大幅减少

# Step 3: 5xx 占比 < 0.3% 后，继续加 KILL_CIRCUIT_DEGRADATION=1
systemctl edit llm-gateway-go
Environment="KILL_FP_SLOT=1"
Environment="KILL_CIRCUIT_DEGRADATION=1"
systemctl restart llm-gateway-go

# Step 4: 加 KILL_SESSION_CACHE=1
# Step 5: 加 KILL_SESSION_COMPRESSION=1

# 每步重启 5 分钟观察
```

### 3. 回滚

```bash
# 删 env + 重启
ssh root@47.97.111.154 'systemctl edit llm-gateway-go' <<EOF
[Service]
# 全部清空 — 回到默认（全部 enabled）
EOF
systemctl restart llm-gateway-go
```

## 验收点

| 步骤 | 期望 |
|---|---|
| 关 fp_slot 前 | minimax-m3 503 占比 ≥ 5% |
| 关 fp_slot 后 | minimax-m3 503 占比 ≤ 1% |
| 关 circuit_degradation 后 | credential 19 仍会在路由中尝试 |
| 全关 3 个后 | 业务成功率 ≥ baseline × 0.95（即下降低于 5%） |

## 注意

1. 关闭 fp_slot 会让 fingerprint 隔离失效 — 并发场景下可能造成同一 credential 被 N 个 session 同时占用，导致 upstream 触发并发限流
2. 关闭 circuit_degradation 会让已 degrade 的 credential 仍会被尝试，造成 503 雪崩
3. 必须**先关 fp_slot 再关 circuit** —— 顺序很重要
4. 这个方案是应急止血，**不是修复**。根本修复需要修 `credentialhealth/checker.go:139` 的 `model_offers` mirror write（SQLSTATE 42703）+ migration 292 中 VIEW 不能 ALTER 的问题
