# 全面测试（Comprehensive Testing · 2026-09-23 之后唯一入口）

> **目的**：把 `tests/48h-audit/`（业务/数据/压力/安全 4 类域测试）和 `tests/stress/`（18 + 1 场景压测）的**所有提示词、方案、脚本**统一归档到本目录，使未来所有"全面测试"工作可在一处发起。
> **关系图**：
> - 本目录 = **规划层**（提示词、方案、脚本、报告）
> - `tests/48h-audit/`、`tests/stress/` = **执行层**（Go 源码、scenarios.json、mock 网关）
> - 执行层由规划层通过路径引用驱动，**不在执行层存放重复文档**。

---

## 目录结构

```
docs/全面测试/
├── README.md                          # 本文件：总入口 + 验收门
├── launch-prompt.md                   # 完整测试启动提示词（含 4 变体）
├── plan.md                            # 主代理 v2 提示词（48h 审计 + 4 类测试整合）
├── master-report.md                   # R56 总览：当前状态、下一步
├── template-domain.md                 # 域目录 + 测试文件 + 报告三套模板
│
├── 48h-audit/                         # 17 域测试整合中心
│   ├── README.md                      # 索引 + 与 docs/audit/playbook/ 关系图
│   ├── D01-ir-lifecycle/              # 17 个域之一
│   │   ├── plan.md                    # 审计要点 + 验收门
│   │   └── reports/latest.md          # 本轮 48h 审计结论
│   ├── D02-protocol-adaptation/
│   ├── ... D03-D17 ...
│   ├── scripts/
│   │   ├── run-all.sh                 # 一键跑指定域的全部测试
│   │   ├── aggregate-reports.sh       # 聚合各域 latest.md → reports/INDEX.md
│   │   └── new-domain.sh              # 按模板新建一个域目录
│   └── reports/
│       └── INDEX.md                   # 跨域聚合索引（自动生成）
│
└── stress/                            # 压测（含 200 并发 + 150K TPM 门禁）
    ├── scenarios.json                 # 19 个场景定义（s1–s18 原有 + s19 新增 200c/150K TPM）
    └── scripts/
        ├── runner.sh                  # harness 生命周期：start/stop/restart/status
        ├── scenario.go.txt            # 场景驱动源码（含 TPM 聚合）· .txt 后缀避免被 Go build 误编
        └── capacity_matrix.sh         # 容量矩阵（参考）
```

---

## 快速开始

```bash
# 0. 三门预检
go build ./...                                    # 必须 0 error
go vet ./...                                      # 必须 0 warning
go test -race -short ./... -timeout 240s          # 必须全过

# 1. 业务/数据/压力/安全 17 域
bash docs/全面测试/48h-audit/scripts/run-all.sh

# 2. 压测（含 s19 200c/150K TPM）
go build -o /tmp/stress-mock     ./tests/stress/mocks
go build -o /tmp/stress-gateway  ./tests/stress/gateway
# 注意：源码真实位置在 tests/stress/scripts/scenario.go
# docs/全面测试/stress/scripts/scenario.go.txt 是文档快照（.txt 后缀避免被 Go build 误编）
go build -o /tmp/scenario        ./tests/stress/scripts/scenario.go
bash docs/全面测试/stress/scripts/runner.sh restart
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_before.json
/tmp/scenario \
  -gateway=http://127.0.0.1:18901 \
  -scenarios=docs/全面测试/stress/scenarios.json \
  -results=tests/stress/results/report.json
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_after.json
bash docs/全面测试/stress/scripts/runner.sh stop

# 3. 聚合报告
bash docs/全面测试/48h-audit/scripts/aggregate-reports.sh \
  > docs/全面测试/48h-audit/reports/INDEX.md
```

---

## 验收门（PASS/FAIL 判定）

### 三门（每次跑必须全过）

| 门 | 命令 | 必须结果 |
|---|---|---|
| 编译 | `go build ./...` | 0 error |
| 静态检查 | `go vet ./...` | 0 warning（vendor 例外） |
| 单元测试 | `go test -race -short ./... -timeout 240s` | 全 PASS |

### 业务/数据/压力/安全 17 域

- 所有非占位域必须至少 1 个 PASS
- 占位域（plan.md 占位 + reports/latest.md "待留档"）允许 no .go files
- 报告路径必须在 `docs/全面测试/48h-audit/reports/history/` 或 `docs/全面测试/stress/results/` 下

### 压测（19 场景）

- s17 已知缺口（REPORT.md §3.1 已披露，本机测试网关 + 生产 cmd/gateway 行为差异）
- s18 必须 PASS（5500 @ c50 长突发稳定性）
- **s19 必须 PASS**（200 并发 + 150K TPM 门禁）
- 内存稳定性：跑前 vs 跑后 goroutine 增长 < 20（绝对 < 50）；heap 不单调

---

## 19 个压测场景概览

| ID | 名称 | 关键指标 | 备注 |
|---|---|---|---|
| s1 | baseline | 200/c20 success ≥99% | 三家健康 |
| s2 | single 5xx | 200/c20 success ≥90% | failover |
| s3 | consecutive 5xx | 100/c10 success ≥90% | 降级 |
| s4 | provider slowdown | 80/c16 ≥ 99% | 权重漂移 |
| s5 | quota exhausted | 120/c20 ≥ 90% | 429 |
| s6 | all 5xx | 40/c8 100% 503 | 全故障 |
| s7 | recovery | 140/c14 ≥ 99% | 回流 |
| s8 | burst | 2000/c50 ≥ 99% | 突发 |
| s9 | sustained | 200/c25 ≥ 99% | 短突发 |
| s10 | stream | 100/c10 success ≥99% | SSE |
| s11 | stream broken | 80/c12 success ≥99% | 截断流 |
| s12 | flaky | 200/c20 ≥ 85% | 抖动 |
| s13 | long prompt | 200/c20 ≥ 99% | ≥20k 字符 |
| s14 | 3×503+1 healthy | 80/c12 ≥ 90% | 灾备 |
| s15 | dynamic weighting | 600/c30 ≥ 99% | 三家均分 |
| s16 | timeout | 40/c8 ≥ 80% | 25s 上限 |
| s17 | committed-eof all broken | 80/c12 success 100% + envelope | **已知缺口** |
| s18 | sustained stability | 5500/c50 ≥ 99% | 大压力 |
| **s19** | **200c + 150K TPM** | **12000/c200 ≥ 99% + tpm ≥ 150000** | **T-series 门禁** |

---

## 历史轮次留档

每轮按 `T<N>-<YYYY-MM-DD>` 命名归档到：
- 域级：`docs/全面测试/48h-audit/DXX-*/reports/history/`
- 轮级：`docs/全面测试/history/`（待建）

---

## 跨会话接力模板

```text
承接 T<N-1>：
  - 起点 = docs/全面测试/history/T<N-1>-<日期>.md §遗留
  - 知识库入口 = docs/全面测试/README.md + docs/全面测试/launch-prompt.md
  - 主计划 = docs/全面测试/plan.md
  - 当前轮提示词 = docs/全面测试/launch-prompt.md
其余按 docs/全面测试/plan.md 流程跑。
```

---

**版本**: v1 · 2026-09-23 00:33 CST · T1 入口
**配套**:
- `launch-prompt.md`（执行提示词）
- `plan.md`（主代理 v2 整合提示词）
- `master-report.md`（R56 总览）
- `48h-audit/`（17 域测试整合中心）
- `stress/`（19 场景压测）