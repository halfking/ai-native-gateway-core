# R93 域B 报告：auto-route settle 停滞告警面 + selection 产出侧告警（第三十三轮）

审计人：分域子代理 B（只读；仓库未改动，全部变异在 /tmp scratch 树实跑后销毁）。
HEAD=f609ecab1。手段：git show 对账、promtool（docker，check+test 两套）、go test（含
`-tags integration` 真库钉测，testcontainers 真实 PG）、scratch-tree 变异复跑（M1/M2/P1）、
生产者指标与 main.go 接线直读。

## 一、逐提交判定

| 提交 | 判定 | 关键证据 |
|---|---|---|
| 2e68487a8 §9.57 | **成立** | 生产者对账逐字一致：`llm_gateway_auto_selections_total` 是 CounterVec{task_type,affinity_applied,explore}、dropped 是无标签 Counter、注释原句「worth alerting on rather than merely graphing」均在 domains/hooks/observability/telemetry/selection_metrics.go:9-33。CounterVec 陷阱断言被变异证明：scratch 树删掉 `or (0 * max by (job, instance) (up{...}))` 项 → promtool 场景 A 红（且仅 A）、Go 门 `or\s*\(?\s*0\s*\*` 正则同红——「恒不触发」死告警形态确有场景承重（R33 12h 在 baseline 族抓的那两类，本族用场景 A/D 兑现）。括号层次正确：`(A or B) == 0 and on(job,instance) C`，场景 C（有产出不响）依赖左臂非零时 `or` 右臂按标签集丢弃，实跑绿。`up{job=~"llm-gateway.*"}` 与 prometheus.yml:71 实际 job 名 'llm-gateway' 及既有 alerts.yml:85 写法一致。两笔文档追加（docs/audit/2026-09-30-session-request-data-re-audit.md §9.54-9.57、docs/handoff/20261001-session-request-data-critique.md 第三十九轮）与提交声称一致：dropped=0+total 缺席（§9.56.2）、进程 2026-10-01 05:19:09 起（§9.56.2）、30 天日量表 09-07=10,933 / 09-08=11,411 / 09-09 起 0–14（§9.55.3 表逐格核对）。已知局限两条（非 auto 部署常驻红 / 252 无 Prometheus）如实登记且有第三道 Go 门守「必须写下来」 |
| f609ecab1 | **成立（附 P2-1）** | error 臂接线 bg/auto_route_settle_worker.go:303（settleBatch err 后）、panic 臂 :254（safeSweep recover），reason 闭集二值 :134-137；M2 变异（删 Inc）→ `-tags integration` 钉测红「delta = 0, want +1」，HEAD 上真库绿。M1 变异（unless→`==0`）→ promtool 红在且仅在场景 10（提交声称精确复现），Go 门 auto_route_settle_baseline_test.go:116 同红。SweepFailing 窗口语义真承重：场景 7 在 20m firing、45m 自愈，窗口滑动推演（失败点 t≤5m，30m 窗口 t=35m 起不含）与 promtool 一致。Go 门注册表 3→5 条+unless 钉+产侧前提钉均在 diff 落地 |
| 208bc393a §9.58 | 成立 | 纯追加实证：`git show | grep '^-' | grep -v '^--- a/'` 零输出，2 文件 +147 |
| 3ae6c5295 §9.54-9.56 | 成立 | 同法零删除，2 文件 +623；§9.54/9.55/9.56 三节内容与后继 yml 注释引用的数字互洽 |

## 二、重点核查面结论

**1. §9.57 promtool 是否真管语义**：是。场景 A（无任何 selection 序列必须触发）、D（有序列零增量触发）、C/E/F/G（正/停机/dropped 触发/静默）在 HEAD 全绿；变异证明 A 与 Go 门分工如注释所述——Go 门只证「项存在」，A 证「有它就对」。6 场景命名跳过 B（A,C,D,E,F,G），计数与提交一致，纯外观。

**2. 计数器接线第三条「没结算」路径**：db==nil（worker:262-264）与 distlock 非leader 提前 return（worker:272-277）**有意不计数**，作为「失败计数」语义正确（不是失败）。后果不落在 SweepFailing，而落在 Stalled 的 per-instance 配对上——见 P2-1。run() 循环体在 safeSweep 之外的 panic（worker:218-222 兜底）会使 goroutine 永久退出且无计数，但该面由 Stalled 传递覆盖（见 §四）。

**3. `unless on(job, instance)` 多实例推演**：单实例停滞正确触发（场景 9/10）。多实例+无 Redis（双双 sweep）时双方计数都增长，判据成立。**多实例+Redis（R30 审计记录的双实例拓扑；main.go:5470 在配置了 Redis 时无条件接线）时存在结构性误报面**，见 P2-1。

**4. 场景 7-12 假绿排查**：未发现。8 的无关 source_total 夹具不与 SweepFailing 表达式交互；9 与 10 的分工诚实（9 在 ==0 变异下仍绿、只有 10 能鉴别——与提交「红在且仅在场景 10」一致）；12 的 lhs 前提（产侧停增→空向量）是设计前提且确由 §9.57 族接管。

**5. R32 91466ce63 注释订正核对**：worker:9「Per sweep (every 5 minutes)」与 :353「每 5 分钟跑一次、每次 500 行（settleInterval/settleBatchSize）」均与常量 settleInterval=5m（:44）、settleBatchSize=500（:51）一致。bg/ 内未再发现「每 30 秒/100 行」残留。

## 三、发现清单

**P2-1 AutoRouteSettleStalled 与 distlock 拓扑结构性冲突：多实例+Redis 下 follower 常驻 critical 误报，且未登记**。settled_total 只在赢得当轮 SetNX 竞争的实例上进程内自增（worker:663/614）；锁在每轮 sweep 结束即 Release（worker:273 defer），下一轮是相位固定的重新竞争——相位靠后的实例要么 SetNX 失败跳过、要么 sweep 到 0 条 pending（leader 刚结完），**两种情况其 settled 序列都不增长**。该实例只要还在产出 auto selection（蓝绿双实例都在接流量），`sum by (job,instance)(increase(selections[45m]))>0 unless …settled…>0` 对它恒真，for:15m 后进入**常驻 critical**。告警的已知误报面只登记了合成流量（yml:169-172、提交信息），runbook 四步对中招实例全部走不通。缓解面：单实例或无 Redis 部署不触发（当前 deploy/prometheus/prometheus.yml:71-74 本地单 target 'llm-gateway:8781' 掩盖了它）；leader sweep 持续失败时 pending 会留给 follower 结算，误报反而消失、由 SweepFailing 接管——但稳态误报成立。建议方向（本轮不做）：判据改 `sum by (job)`（实例聚合上移）或以 distlock leader 身份标签守卫 lhs。

**P3-1 注释引用了不存在的告警名 `SelectionWritesStalled`**（实际名 AutoRouteSelectionsNotProduced）：deploy/prometheus/rule_tests/auto-route-settle-baseline_test.yml:251 与 deploy/prometheus/rules/auto_route_settle_baseline_test.go:121。全仓仅此两处；按名回查交叉引用会扑空。

**P3-2 Stalled 对「静默回 v1/停写」面的可见性是瞬态的**：行龄过 settleAbandonAfter(4h) 后走 abandon 路径，abandon **也**递增 settled_total{outcome="abandoned"}（worker:613-615）→ rhs 复活、告警自清。即提交信息声称的「§四#3 设置门读失败静默回 v1 的无 settle 信号面可见」只在停写后前 ~4h 窗口成立；此后「每条 selection 都被 abandon、reward 永久 NULL」的稳态没有任何门盯着（全 rules 目录无 outcome="abandoned" 增长类判据）。

**P3-3 三个已注册 settle 指标无任何消费者**：llmgw_autoroute_settle_lag_seconds / llmgw_autoroute_reward / llmgw_autoroute_settle_retry_state_total 在 deploy/（rules+dashboards+grafana）grep 零命中——§9.37 镜像形态仍开着三个口。本轮 Go 门注册表注释「五个注册的指标」措辞易被读成「已全覆盖」，实际 worker+baseline+source 共注册 8 个。既有欠账，非本轮引入，如实登记不扩 scope。

**P3-4 promtool 场景不在 CI**：.github/workflows、scripts/、Makefile 均无 promtool 调用，场景文件只靠文件头 docker 命令人工执行；自动防线只有 Go 形状门（go test 会跑）。承重语义（unless 项、by (job,instance)、increase 窗口）已被形状门钉住大半，与 role-fallback 既有模式一致，非本轮新增缺口。

**P3-5 措辞漂移**：yml 头注/告警 description/提交信息主打「`or vector(0)` 不是可选的」，但最终表达式用的是机制等价的 `or (0 * max by (job, instance) (up{...}))`，yml 内 grep 不到字面 `vector(0)`；换用理由只在 Go 门注释（auto_route_selection_output_test.go:105-121）里。机制无错，读者按字面找会落空。

## 四、合并审视（读侧 baseline 族+停滞族+产侧 §9.57）

- settle worker goroutine 整体死亡（进程活着）：settled 停增 → 产侧在走时由 Stalled 覆盖；产侧同停由 §9.57 覆盖；进程死由 up 门（alerts.yml:85）覆盖。无专门心跳门，传递覆盖成立，登记即可。
- `SelectionWritesStalled` 名下两门分工（场景 12）实跑成立：产侧停增时 Stalled 的 lhs 为空不响，归 §9.57。
- 其余无人盯的面即 P3-2/P3-3，不另扩。

## 五、实跑记录

- promtool check rules：auto-route-selection-output.yml SUCCESS(2) / auto-route-settle-baseline.yml SUCCESS(5)。
- promtool test rules：两套全 SUCCESS（6+12 场景）。
- go test ./deploy/prometheus/rules/ -count=1 → ok。
- go test -tags integration ./bg/ -run TestAutoRouteSettleSweepFailureCounterWiresUp → ok（testcontainers 真 PG）。
- 变异：M1（unless→==0）→ 仅 promtool 场景 10 红 + Go 门 :116 红；M2（删 error 臂 Inc）→ 钉测红「delta=0, want +1」；P1（删空转 0 项）→ promtool 场景 A 红 + Go 门红。全部在 /tmp scratch 树执行，仓库零改动。
