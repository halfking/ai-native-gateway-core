# 接力 handoff — llm-gateway-go 三十七轮续（熔断三面死 + bg panic 收口 + 守卫缺口）

**日期**：2026-09-30
**检出**：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`（official-deploy 树；注意与 ai-native-tools 检出是两条独立工作线）
**基线**：重锚 ff 至 `4b00d57f8`（并行会话三十八~四十一轮已推进 12 提交），本续轮在其上新增提交（见 git log）
**状态**：已提交、待推送（或已推送，视本文件生成时点）

---

## 一、结论与根因

本轮消化三十七轮 §三 清单三项并批判式复审定稿：

1. **§三#4 熔断 OPEN 三面死（P2）根修**——`credentials.circuit_state` 此前只有 RestoreOnSuccess 一个 'closed' 写入方（生产恒 'closed'）、`RecordCircuitStateChange` 零调用、routing 健康检查 `circuit_open` 永假。修法：Breaker 迁移观察者（四迁移点 + OPEN 原地重入顺延也上报）→ main.go 装配 → transitions 计数器 + DBStateSync 异步落库（128 队列、QUARANTINED→'open' 映射 schema 三值约束、写失败不重试）。健康检查与 admin 消费面**零代码改动复活**。
2. **§三#5 bg panic 无包装（P2，bg/ 域）根修**——新增 `bg/spawn.go`（Go/GoArg/SpawnLoop 三包装 + `llm_gateway_bg_goroutine_panics_total`），112 处站点迁移、24 处 skip（自带 recover）。定式：**带握手（close done/wg.Done）的循环禁用 SpawnLoop**（重启二次触发握手）；循环无握手用 SpawnLoop 自愈重启。
3. **§三#14 守卫缺口（P3）根修**——pre-push §0 自愈前移到 github-only 早退前（推 codeup 也触发）+ pre-commit-check.sh 补同款。**「core.worktree 未清」按现勘证伪**：它是 submodule 型 gitdir 的合法承重配置，清除即废检出。

### 批判式复审抓出的自我错误（全部已修，教训在 §四）

机械批量转换 112 处站点的代价：GoArg 闭合行多带实参（编译抓出）、probe_service 心跳父 ctx 错选（`hbCtx` 才随 cancel 退出）、monitor.go 双参闭包实参被丢弃、首批漏网 4 处（残差 grep 抓出）、main.go 自加的裸 `go` 自我收口。**spawn 源码钉测**（probe_model_not_served_test）因字面量变化变红——更新 pin 后守卫力不变。

---

## 二、改动文件与关键行为

| 文件 | 改动 |
|---|---|
| `domains/credential/breaker.go` | Breaker+Manager 观察者装配；notify 四迁移点；OPEN 重入上报 |
| `domains/credential/state_sync.go`（新） | DBStateSync：异步单 worker 落库 circuit_state/circuit_opened_at/cooling_until |
| `domains/credential/state_sync_test.go`（新） | 6 钉测：迁移通知/隔离装配/重入顺延/pgxmock 四 SQL 形态/丢包计数 |
| `bg/spawn.go`（新） | Go/GoArg/SpawnLoop 三包装 |
| `bg/metrics.go` | +goroutine panics 计数器 |
| `bg/*.go`（48 文件） | 裸 `go` 迁移到三包装（含 systemmonitor/monitor.go，加 bg 导入） |
| `bg/probe_model_not_served_test.go` | spawn 字面量 pin 更新（守卫力不变） |
| `cmd/gateway/main.go` | 熔断接线（observer → metrics + circuitSync）；自加 goroutine 用 bg.Go |
| `.githooks/pre-push` | §0 前移到 github-only 早退前 |
| `scripts/pre-commit-check.sh` | 补 §0 bare 自愈（worktree 不清，已裁决） |
| `docs/全面审计v3/2026-09-30/25-修订审计三十七轮.md` | §三 三行改已修 + §六 续轮账 |
| `docs/全面审计v3/README.md` | 续轮条目 |

## 三、测试命令与结果

```bash
go build ./...                                 # ✅
go vet ./bg/... ./domains/credential/ ./cmd/gateway/   # ✅
go test ./bg/ -count=1                         # ✅ 25s（钉测更新后复跑）
go test ./bg/systemmonitor/ -count=1           # ✅
go test ./domains/credential/ -count=1         # ✅ 15s（+6 钉测）
go test ./cmd/gateway/... -count=1             # ✅
go test ./admin/ -count=1                      # ✅ 66s
go test ./domains/hooks/audit/ -count=1        # ✅ 棘轮门
go test ./tests/48h-audit/D07-hot-columnar/data/ -count=1  # ✅ 棘轮门
```

残差核查：bg/ 非测试裸 `go` 余 24 处 = 全部文档化 skip（体内自带 recover）+ 包装自身。

## 四、教训（本轮付出代价换来的）

- **机械转换必须过三道**：编译（抓 GoArg 实参）、全量测试（抓钉测字面量）、残差 grep（抓漏网 4 处）——只跑前一道时我一度以为完成。
- **包装选择是语义决策不是格式**：SpawnLoop 的重启语义对握手类是负计数 panic；父 ctx 要选「随生命周期退出」的那个（hbCtx vs 函数入参 ctx）。
- **源码钉测迁移**：改 spawn 形态会红 source-scan pin——更新 pin 时必须声明守卫力不降。
- **「登记不修」里的断言也要现勘**：§三#14 写的「core.worktree 未清」在ff 后实测是合法配置——登记项照搬不核实就会误修。

## 五、遗留风险 / 未做

- **部署冒烟未做**：本机 8782 跑 4b00d57f（先于本轮修复）；并行会话在途（deploy-local-lib.sh hotzone D1 未提交），不抢部署。修复前基线已存档：circuit_state 83 closed + 3 NULL + opened_at 全零。
- **per-credential 熔断 gauge 未接**（Recorder 接口无凭据维度）——沿用登记。
- dispatch 族 `_ = recover()` 静默吞属 streaming 域，本轮未动。
- §三 剩余：#1 N21-5 六族 TTL（owner）/ #2 附件 LRU / #3 suppress-restore（owner）/ #6-#13。
- bg panics_total 指标 >0 = goroutine 已死待重启，建议接告警。

## 六、下一轮提示词

```
继续 llm-gateway-go 三十七轮续接力（official-deploy 检出）。

【第一步必做】git fetch codeup main && git log --oneline HEAD..codeup/main
—— 并行会话活跃（hotzone D1 在途），动手前重新锚定；不盲 pull。

【已完成，不要重做】
- §三#4 熔断三面死（breaker 观察者 + state_sync + main 接线）
- §三#5 bg/ 域 panic 收口（spawn.go 三包装 + 112 迁移 + 24 skip）
- §三#14 守卫缺口（pre-push 前移 + pre-commit §0；core.worktree 不清已裁决）
- 两道棘轮门验证（audit_whitelist_ratchet / D07 data/）全绿

【下一轮优先】
1. 部署本续轮（等并行 hotzone D1 收口后），核对三件事：
   credentials.circuit_state 出现非 closed 值；llm_gateway_circuit_state_transitions_total
   非零；llm_gateway_bg_goroutine_panics_total 基线 0。
2. 部署后补三十七轮 §五 挂账冒烟：egress 池推理模型首字节>30s、
   R24-C 治理开启流式 wire 非空、附件 responses 入口 245。
3. §三 剩余按序：#6 SK≠CEK Go 侧 fail-fast（无需 owner，可做）→
   #1/#3 owner 拍板项催决 → #2 附件 LRU 反查修法。
【纪律】
- 子代理引用逐字核对后再动手；每逻辑单元精确路径提交。
- 并行会话文件（deploy-local-lib.sh 等）不扫入提交。
- 白名单/豁免/钉测修改必须变异验证守卫力。
```
