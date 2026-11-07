# T2 · 2026-11-07（wasteround）— 探测浪费与恢复及时性根修轮（Maestro 全量验证）

> 触发轮：docs/audit/2026-11-07-探测浪费与恢复及时性根修轮.md
> 树：提交 60c7de29f（W1–W8）+ 004cde017（W9，线上验证暴露后补修）；211 已部署 2966。

## 三门

| 门 | 结果 |
|---|---|
| `go build ./...` | ✅ 0 error |
| `go vet ./bg/... ./cmd/gateway/... ./tests/48h-audit/...` | ✅ 0 warning |
| `go test -race -short ./... -timeout 1200s` | ✅ rc=0（全过，最终树） |

## 17 域（run-all.sh，留档 run-all-T2-2026-11-07-wasteround.log）

**domains: 17 pass: 17 fail: 0 —— ALL DOMAINS PASS**

- D09-node-state-selfcheck 本轮更新为**两档契约**（business B-01：阈值以下 ≤ 恢复 SLA
  逐字保留、阈值起 ≤ sustained 档；stress S-01 parks 增加 sustained 档）——
  上一轮钉的「任何 attempt ≤ SLA」单档契约正是本轮浪费的根因口径，按用户目标反转。
- 其余有测域全 PASS；占位域按验收门允许 no .go files。

## 压测（19 场景，mock 网关 :18901）

- **18/19 PASS**；唯一 fail = s17_committed_eof_all_broken（README 验收门已披露的已知缺口）。
- 强制门：**s18 5500/5500（100%）✅；s19 12000/12000（100%，p95 78ms）✅**。
- 内存稳定性：goroutines 4 → 20（Δ+16 < 20 ✅）；memstats 前后留档
  tests/stress/results/memory-T2-2026-11-07-wasteround-{before,after}.json。
- 启动提示词 4.3 步（`go test ./tests/stress/`）在当前布局无 Go 测试文件——以 4.2 场景跑为证据（登记）。

## 本轮新增判据面

- bg/probe_sustained_failure_20261107_test.go：S1 决策表 / S2 冷却同档 / S3 rearm 守卫+分级 /
  S4 到期复检梯子守卫 / S5+S6 证据时刻贯通 / S7 失败镜像 upsert。
- 六处旧门按新口径更新（各自带反转理由注释）：probe_cost_p02（两档 + 跨代升级）、
  credential_recovery_test（07-24 门反转）、probe_rollback_tentative / node_probe_gateway_side
  （签名）、probe_schedule_clamp（$3 豁免）、probe_schedule_planning（钉两臂）。

## 线上终验（211=2966 槽 a）

- 高频对 next_retry ~25-30min（featured 50%×1h）/ 1h；绑定冷却 ~1h。
- node_probe_runs 15min 窗口 155 → **38**（≈4×；余量为 cf<21 的设计内 SLA 档）。
- 凭据 35（无状态行族）W9 后开始建行爬梯。
- probe-schedule-audit 211：R1-R5=0、M0=0、findings=0。
