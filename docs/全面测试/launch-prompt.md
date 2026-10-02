# 完整测试启动提示词（可复用 · R56+ 入口）

> **用途**：每次需要跑"48h 审计 + 完整 4 类测试 + 留档 + 推送"时，把下面整个代码块拷贝到新会话作为任务提示词。
> **与 00-PLAN.md 的区别**：
> - `00-PLAN.md` 是 **审计视角**（带发现、定级、修复）
> - 本文件是 **测试视角**（跑全部、收集结果、出报告），不强制审计流程
> - 两者互补：审计 → 修复 → 全量测试 → 出报告 → 推送
> **首次创建**: 2026-09-23 00:05 CST  
> **对应仓库 commit**: 见 `git log -1 --format="%H %s"` 时所在 commit

---

## 主提示词（拷贝到新会话）

```text
你是 llm-gateway-go 仓库的完整测试主代理。
任务：跑全量 48h 审计测试中心的 4 类测试（business/data/stress/safety），收集结果，出报告，并按需推送。
工作目录：<仓库绝对路径，默认当前工作目录>
轮次编号：T<N>（接 R56+ 编号体系，自增）

## 第 1 步：装载知识库（必做，顺序执行）
1. Read tests/48h-audit/README.md（整合目录结构 + 与 docs/audit/playbook/ 关系图）
2. Read tests/48h-audit/MASTER_REPORT.md（R56 入口总览）
3. Read tests/48h-audit/00-PLAN.md（如需审计视角）
4. Read tests/48h-audit/TEMPLATE-domain.md（域目录结构 + 测试规范）

## 第 2 步：环境预检
- 仓库根目录存在 go.mod 且与本仓一致
- go version 与仓库一致（go 1.27+）
- 工作区无未提交的本地修改：`git status --porcelain | wc -l` 必须为 0 或仅含计划内的待提交项
- 与 origin/main 同步：`git fetch origin && git status -sb` 显示无 "ahead/behind"

## 第 3 步：构建 + 三门
```bash
go build ./...                                # 必须 0 error
go vet ./...                                  # 必须 0 warning (除已知 vendor 警告)
go test -race -short ./... -timeout 240s      # 必须全过；不包含 stress（防 30s+ 长跑）
```

任意一门失败 → 立即停手，不要继续跑下游。

## 第 4 步：跑全量 48h 测试（核心）
```bash
# 4.1 全 17 域（占位域会显示 no .go files，正常）
bash docs/全面测试/48h-audit/scripts/run-all.sh 2>&1 | tee /tmp/run-all.log

# 4.2 已知 stress 场景（来自 docs/全面测试/stress/）
go build -o /tmp/stress-mock     ./tests/stress/mocks
go build -o /tmp/stress-gateway  ./tests/stress/gateway
# scenario.go 真实位置在 tests/stress/scripts/scenario.go
# docs/全面测试/stress/scripts/scenario.go.txt 是文档快照
go build -o /tmp/scenario        ./tests/stress/scripts/scenario.go
bash docs/全面测试/stress/scripts/runner.sh restart
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_before.json
/tmp/scenario \
  -gateway=http://127.0.0.1:18901 \
  -scenarios=docs/全面测试/stress/scenarios.json \
  -results=tests/stress/results/report.json
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_after.json
bash docs/全面测试/stress/scripts/runner.sh stop

# 4.3 内嵌回归（tests/stress/ 单元测试）
go test ./tests/stress/ -v -timeout 60s
```

## 第 5 步：聚合报告
```bash
bash docs/全面测试/48h-audit/scripts/aggregate-reports.sh > docs/全面测试/48h-audit/reports/INDEX.md
```

## 第 6 步：判定 PASS / FAIL
- 48h 测试：所有非占位域必须有至少 1 个 PASS；若失败 P0/P1 → 进 fix 流（详 00-PLAN.md §5）
- 压测（tests/stress）：除 s17（§11.6 已知缺口）外必须 100% PASS；s18 必须 PASS
- 内存稳定性：跑前 vs 跑后 goroutine 增长 < 20（绝对 < 50）；heap 不单调

## 第 7 步：留档
- 把 /tmp/run-all.log 复制到 tests/48h-audit/reports/history/run-all-T<N>-<日期>.log
- 把 /tmp/mem_before.json + /tmp/mem_after.json 复制到 tests/stress/results/memory-T<N>-<日期>.json
- 写 tests/48h-audit/reports/history/T<N>-<YYYY-MM-DD>.md（按 R 文档模板）

## 第 8 步：提交（若结果有新增/改动）
```bash
git status
git add tests/48h-audit/ tests/stress/
git diff --cached --stat
git commit -m "test(T<N>): 完整测试通过 + 报告留档（详细见 reports/history/T<N>-<日期>.md）"
git fetch origin
git status -sb   # 确认 push 之前无新冲突
git push origin main
```

## 第 9 步：上下文预算
- 每步输出 ≤ 8KB；超长强制重写
- 上下文接近 500K → 用 handoff 生成续跑提示词 → /new 清空 → 继续

## 硬性约束
- 任何一步失败必须明确登记（PASS/FAIL/BLOCKED），不得写"基本正常"
- 不允许跳过 stress 跑（除非显式 -short）
- 占位域（plan.md 占位 + reports/latest.md "待留档"）允许 no .go files，但不能宣称"已覆盖"
- 报告路径必须在 tests/48h-audit/reports/history/ 或 tests/stress/results/ 下，不允许散落
```

---

## 简化变体（按场景）

### A. 仅跑快速回归（≤2 分钟）

```text
跳过第 4.2 步（stress 18 场景）+ 跳过 4.3 中耗时项；
只跑 4.1 跑全 + 4.3 中 -short 子集。
适用：CI 每次 push 触发。
```

### B. 仅跑 stress（≤3 分钟）

```text
跳过 4.1（48h 域）+ 跳过 4.3；
只跑 4.2（stress 18 场景）。
适用：压测专项、PR 性能评估。
```

### C. 全量（含所有 race detector + 30s soak）

```text
跑全部 + 加 `-race -count=1` 防止缓存；
stress 跑 2 遍（warm cache 验证）。
适用：发布门禁。
```

### D. 单域审计（worker 子代理派发）

```text
派发到 tests/48h-audit/DXX-name/plan.md 末尾"子代理派发提示词"。
子代理类型：worker（带写权限），并发 ≤ 6。
子代理输出 ≤ 5KB。
```

---

## 输出物清单（每轮必出）

| 物 | 路径 | 生成时机 |
|---|---|---|
| 全量运行 log | `tests/48h-audit/reports/history/run-all-T<N>-<日期>.log` | 第 4 步后 |
| 内存前后快照 | `tests/stress/results/memory-T<N>-<日期>.json`（双文件） | 第 4 步后 |
| 跨域聚合索引 | `tests/48h-audit/reports/INDEX.md` | 第 5 步后（commit 触发更新） |
| 域 latest 更新 | `tests/48h-audit/DXX-*/reports/latest.md` | 第 5 步后（如有变更） |
| 轮次文档 | `tests/48h-audit/reports/history/T<N>-<YYYY-MM-DD>.md` | 第 7 步 |
| commit + push | git log +1 | 第 8 步 |

---

## 失败时的升级路径

| 失败层级 | 处置 |
|---|---|
| 三门（build/vet/test-short） | 立即停手 → 列出所有失败点 → 走修复 → 重跑 |
| 48h 测试某域 FAIL | 派 worker 子代理定位 → 修 → 钉桩 → 重跑该域 |
| stress 某场景 FAIL（除 s17） | 派 worker 子代理定位 + 看 tests/stress/REPORT.md 累积记录 |
| goroutine 泄漏 | 看 s17/s18 memstats → 派 worker → review `tests/48h-audit/DXX-*/safety/` |
| 三轮连续失败同点 | 转 `update_goal` blocked，等用户介入 |

---

## 跨会话接力模板

```text
承接 T<N-1>：
  - 起点 = tests/48h-audit/reports/history/T<N-1>-<日期>.md §遗留
  - 知识库入口 = tests/48h-audit/README.md + tests/48h-audit/MASTER_REPORT.md
  - 主计划 = tests/48h-audit/00-PLAN.md
  - 本提示词 = tests/48h-audit/COMPLETE_TEST_LAUNCH_PROMPT.md
```

---

**版本**: v1 · 2026-09-23 00:05 CST · R56 入口  
**配套**: `00-PLAN.md` (审计视角) + 本文件 (测试视角) + `TEMPLATE-domain.md` (域模板) + `MASTER_REPORT.md` (总览)