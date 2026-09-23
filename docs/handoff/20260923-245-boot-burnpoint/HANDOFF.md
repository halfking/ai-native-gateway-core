# 245 boot 烧点轮（recent-success-rate）移交文档

**工单**: deploy-245.sh 部署失败（09-23 15:02 seq 2221 healthz 探针超时 + Connection refused）
**最后更新**: 2026-09-23 晚（批判式复审轮）
**状态**: ✅ 已闭环（根修上线 2215 + 预存测试缺陷 2 处修复 + 探针默认对齐）
**关联审计**: docs/audit/2026-09-23-252-sql-log-audit.md §十（烧点家族补遗）

---

## 一、结论 / 根因

### 1.1 表象 vs 真根因（两回事，勿混淆）

| 层 | 内容 |
|---|---|
| 表象 | 候选 ensure 链 30s 走完 4 条 `catalog short-circuit` 日志后，healthz 探针 `probe timeout after 60s` + `Connection refused`，旧实例保持服务 |
| 真根因 | `db/db.go ensureRoutingRecentSuccessRate` 单体 Exec 首句对 **request_logs_hot（最热大表）执行 no-op `ALTER ... ADD COLUMN IF NOT EXISTS`**——列全在位仍要 ACCESS EXCLUSIVE 锁逐列确认，共享 252 PG 持续写流下锁等待烧穿 `applyMigrationsOnce` 的 **3 分钟 context deadline**（15:02:36 挂起 → 15:05:12 `context deadline exceeded`，attempt 2 重跑同链被探针击杀） |
| 为什么不是探针窗口 | ensure 链是秒级走完的（catalog short-circuit 快路径）；烧点在 ensure **之后**的迁移步骤。调大 PROBE_TIMEOUT_SECS 无效——候选结构性起不来。与 09-19/09-22 的"ensure 慢"失败模式**不同因**（那两轮是 ensure 本身慢） |

### 1.2 证据链

- 245 journalctl（候选单元 15:02-15:06 全量）：ensure 后沉默 156s → deadline 告警 → 探针击杀时间轴吻合
- 修复后行为回证：守卫版中 backfill UPDATE + CREATE OR REPLACE 保留且实测毫秒级 → 排除其余语句；该步总耗时 156s+ → 0.7s
- 代码序：governor revision 之后第一步即 ensureRoutingRecentSuccessRate（db.go:322→325）
- **未做** 252 PG 语句级日志归因（强 circumstantial，见 §四 遗留风险）

---

## 二、改动文件与关键行为

### 2.1 第一轮（70e47c4f，已推 main + official-deploy 克隆同步 + 上线验证）

| 文件 | 关键行为 |
|---|---|
| `db/db.go` | ensureRoutingRecentSuccessRate 拆四段：① task_type/origin_stage 列在位 → 跳过 ALTER（catalog short-circuit 日志）；② recent_success_rate 4 参签名在位（pronargtypes='20 25 23 23'）→ 跳过 DROP；③ CREATE OR REPLACE **始终执行**（函数体同步不可短路，函数级锁毫秒级）；④ backfill UPDATE **保留**（成熟库空集毫秒级，有真实 broken_confirmed 行是必须的数据回填）。单体事务拆分，任一段失败不再回滚已成功段 |
| `scripts/deploy-seamless.sh` | 探针失败诊断：自动拉 journal 尾 + `systemctl show ActiveState/SubState/NRestarts` + `ss -ltnp` 端口状态；排查提示按 boot 标记四分类（gateway listen failed / 迁移重试烧点 / 已到 gateway listening / ensure 推进中） |

### 2.2 第二轮（批判式复审，本轮提交）

| 文件 | 缺陷 → 修复 |
|---|---|
| `tests/deploy_readiness_contract_test.sh` | **B1**：断言 `TimeoutStartSec=90s` 与 9cbd60e40 已提交的 700s 模板脱节（F2"改默认值漏改断言"同型第 6 犯，契约长期红）→ 断言改 700s，语义"systemd 预算覆盖 600s 探针窗口" |
| `tests/deploy_local_contract_test.sh` | **B2**：redis harness `DOCKER_PING_NAME='x' redis_discover` 临时赋值未 export，传不进 `bash -c` 子进程，假 docker exec 永不命中——**初始版本即坏，named/scan 用例形同虚设**（手工复现证实 lib 逻辑本身正确）→ 4 处改 `export` |
| `scripts/deploy-seamless.sh` | **B3**：探针失败诊断的标记 grep 只看末 60 行 journal，09-23 第二次部署实证迁移重试告警恰被截掉、分类降级 → grep 源扩 `-n 400`，展示仍取尾 60 行 |
| `scripts/deploy-245.sh` | 探针默认 180→600s：seq 2214 实证烧点已修后晚间 PG 争用下全链仍 3-4 分钟（work_type 区 ~74s、Maas 区 ~2min），180+60 不够；600s 与单元 TimeoutStartSec=700s 对齐。**deploy-154.sh 保持 120s 未动**（生产回滚窗口 tradeoff 留给 owner 决策） |
| `docs/全面测试/deploy-test.md` | 245 入口默认值同步 600 |

---

## 三、测试命令与结果

```bash
# Go 侧（第一轮已跑，本轮无 Go 改动）
go build ./db/... ./cmd/...          # PASS
go vet ./db/                          # PASS
go test ./db/ -count=1                # ok 0.297s

# 部署契约套件（本轮补跑 + 修复后回归，全部 exit=0）
bash tests/deploy_blue_green_contract_test.sh      # 30 PASS / 0 FAIL
bash tests/deploy_port_rotation_test.sh            # 15 PASS / 0 FAIL
bash tests/deploy_rollback_test.sh                 # 11 PASS / 0 FAIL
bash tests/deploy_seamless_postcondition_test.sh   # 4/4 PASS（offline）
bash tests/deploy_seamless_state_machine_test.sh   # 14 PASS / 0 FAIL
bash tests/deploy_local_contract_test.sh           # 修复前 3P/1F → 修复后 26 PASS / 0 FAIL
bash tests/deploy_readiness_contract_test.sh       # 修复前 16P/2F → 修复后 18 PASS / 0 FAIL
bash -n scripts/deploy-seamless.sh scripts/deploy-245.sh   # SYNTAX_OK

# 线上验证（seq 2215，2026-09-23 17:52）
#   245: healthz 200 ready:true + readyz 200 + nginx443 200 + /version 身份匹配（active=8781，切换 67s）
#   公网: https://llmgo.kxpms.cn/healthz → 2.5.6-70e47c4f-20260923-2215 ready:true
#   守卫日志实证: "request_logs_hot task_type/origin_stage columns ensured (catalog short-circuit)"
```

---

## 四、遗留风险（实事求是）

1. **烧点归因未做 PG 语句级取证**——修复后行为回证强，但若后续同点位复发，应先拉 252 PG 日志锁等待记录再归因（§十 10.4）。
2. **下一批烧点候选未修**：`ensureWorkTypeSchema` 区 ~74s、tuning_signals views→route_incident 区 ~2min（晚间争用实测），均无守卫无进度日志。3min 迁移预算内能过但吃掉大半；争用高峰全链 4min+，届时已靠 600s 探针兜底。修复需 PG 语句级取证定位具体语句。
3. **applyMigrationsOnce 3min 预算未动**：争用时链长可超预算 → attempt 2 重试（有 5s backoff + 双窗口探针兜底）。是否上调到 6min 未决——盲调有 statement_timeout 5min pin 拉长风险，需 PG 证据。17:44 boot 链运行超推算 deadline 仍在推进的矛盾未逐行核实（怀疑 attempt 2 重启点落在观察窗口外）。
4. **deploy-154.sh 探针默认仍 120s**：154 与 245 同 PG 同 ensure 链，生产部署争用窗口会撞同类问题；但窗口加倍=坏候选多占生产回滚时间 7 分钟，决策留 owner。
5. **烧点家族缺系统性防线**：每个新 ensure 步骤仍可能引入热表 no-op DDL（本次即漏网）。建议后续在 pre-commit/审计清单加"新 ensure 必须自答成熟库锁需求"检查项（接线 wiring-gap-recurrence 五点同步同族）。
6. **B2 说明测试曾长期红**：deploy_local_contract 的 redis 用例自初始版本即坏，说明该套件有一段时间没人跑全量——契约绿≠健康，"套件是否被运行"本身需要监督。

---

## 五、记忆与物证

- 记忆库：`2026-09-23-245-boot-burnpoint-recent-success-rate`（含 How-to-apply 与挂账）
- 物证：245 journalctl 候选单元 15:02-15:06 / 17:44-17:48 / 17:49-17:53 三段时间轴（本 handoff 未附原文，可用上述单元名+时间窗在 245 复拉）
- 部署入口 SSOT：`~/workspace/official-deploy/services/llm-gateway-go`（已同步到本轮 main HEAD）

---

## 六、下一轮提示词（建议）

> 以 docs/audit/2026-09-23-252-sql-log-audit.md §十 + 本 handoff §四 为起点：
> 1) **work_type/Maas 区烧点取证**：252 PG 日志锁等待 + 逐 ensure 步骤计时日志（给 ensureRoutingAnalyticsMaterializedViews 与 ensureWorkTypeSchema 之间补进度日志再实测一轮），定位具体语句后按 D1 守卫家族模式修复；
> 2) **applyMigrationsOnce 3min 预算决策**：拿到 1) 的链长分布后再定是否上调（先回答 statement_timeout pin 与预算的交互）；
> 3) **deploy-154.sh 探针默认决策**：120s 维持 or 对齐 600s——需 owner 权衡生产回滚窗口；
> 4) **烧点家族防线**：审计清单/pre-commit 加"新 ensure 步骤热表 no-op DDL 必须配 columnsAllPresent 守卫"检查项；
> 5) 252-dev/154 网关二进制尚未带 70e47c4f 修复（245 已上 2215）——随下一部署窗口收敛，部署后验证守卫日志出现。
> 纪律沿用：上轮结论不当本轮公理（A2）；击杀归因先分层 timeout（A3）；"重跑自愈"先验证烧点可推进（⑯）。
