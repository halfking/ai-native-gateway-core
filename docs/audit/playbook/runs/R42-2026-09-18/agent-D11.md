# D11 auto 模型全量实现 子代理报告(窗口:0a015af51^..b75c91900,实际边界 2026-09-17 15:32:38 +0800 → 2026-09-18 04:25:32 +0800)

> 窗口内 D11 相关改动:e9d46b37e/6276a3ff9(任务分布 Sprintf 修复,双分支重复落账)、93f530901(settle worker 合成轮过滤)、78b3acdef(shadow_actors.go 仅注释 + routing_health_checks probe_missing 换 compat 视图,后者属 D09 面)。affinity worker、auto_summary_generator、auto_title_generator 窗口内净改动为 0。R37 ef408a026(09-17 10:18)在窗口基线之前,其改动应视为基线而非窗口改动。

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **settle 奖励的 session 健康分量仍摄入 goal-\* 合成轮**。goal 影子轮复用父会话 X-Gw-Session-Id,session_summaries 计数按 gw_session_id 聚合且无 origin_actor 排除;settle 的 session 归因读的正是这些计数。R37 口径只盖了 baselines/mr LATERAL/work_types,settle 对 session_summaries 的 JOIN 是残留缺口(窗口提交 93f530901 正触碰此面,属口径收尾未完,非新回归)。偏向:sessionReqs 被 goal 轮放大 → 0.80 归因门更难达标(保守向);goal 轮失败计入 errRate → 真实轮 routingOnlyHealth 被压 | domains/streaming/response_interceptor_helpers.go:242(复用父 session_id)、internal/summarystore/store.go:265-268(计数无合成排除)、bg/auto_route_settle_worker.go:394-395(JOIN)、:520-526 + autoroute/affinity.go:413-422(errRate 与归因分母被污染) | 按 R37 同法在计数侧或 JOIN 侧排除合成轮;先确认影响幅度 |
| 2 | P3 | **audit 任务分布双路都无法剔除合成轮,与 work_types 面口径分叉**。routing_analytics_source 未投影 origin_actor(MV 与 fallback 均无合成排除);R37 遗留记载"扩投影需迁移 718",但 718 实为索引清理,扩投影未落地。另 MV 路径多一个 effective_model IS NOT NULL 过滤而 fallback 没有,MV 不可用时回退数字略抬 | db/db.go:1971-2006(无 origin_actor)、:2035(MV 独有过滤)vs admin/auto_route.go:594,723-736(fallback 无);对照 admin/work_types.go:276(已排除面);718 内容 | 登记 R37 遗留现状属实;扩投影迁移走 revision-sequence 通道,勿撞号 |
| 3 | P3 | **shadow_actors R39 注释断言"origin_actor 唯一写入口是 origin_mw.go"不准确**:第二写入口是 handler 入口的 X-Gw-Source-Actor(loopback/goal 链)。两侧当前都 TrimSpace,不变量成立,但注释会误导未来维护者只护一边 | autoroute/shadow_actors.go:53-59(注释)vs domains/streaming/handler.go:1727-1729 → request_log_pipeline.go:489(第二写入口,亦 TrimSpace) | 修注释一句,不动代码 |
| 4 | P3 | **合成轮 abandon 不计入 llmgw_autoroute_settled_total{abandoned}(有意),但 DB 侧该行仍 stamped settled(reward_source='request', reward NULL)** → Prometheus 计数与库内 abandon 行数系统性偏差。注释已自述意图,counter help 未说明口径 | bg/auto_route_settle_worker.go:449-457(无 counter)、:576-583(abandon 落库)、:74-80(counter 定义) | 在 counter Help 或注释补口径 |
| 5 | P3 | **handleAudit fallback 查询错误仍被吞**:R41 修了 SQL 本体,但 if err == nil 无 else、rows.Err() 未查,查询再失败时 task_distribution 键静默缺失(R41 注释自认此模式仍在) | admin/auto_route.go:595-605 | 顺手补 err 日志或 500,低优先 |
| 6 | P3 | R41 修复双分支重复落账:两 commit 各 +79 行,diff 为空,内容零漂移;仅按 commit 计数会双计 | git show --stat 两 commit | 溯源备注,无需处置 |

## 二、核实为健康的面

- **任务分布查询修复正确(重点 1)**:builder 将含裸 % 的 businessFrag/tenantFrag 作为 Sprintf 参数传入,SELECT/GROUP BY 双轴 taskExpr 均展开;全仓 businessRequestFilter 其余 6 个消费点均为参数传递或纯拼接;R37 work_types 同为参数传递;钉桩测试(NoFmtCorruption + FragmentParity)覆盖。
- **合成轮过滤后奖励闭环自洽(重点 2)**:基线查询排除 → mr LATERAL 排除 → Go 侧 IsSyntheticActor 判定 abandon;rl 未落地的合成行仍走 4h abandon 不堵批;abandon 行 reward=NULL → affinity 聚合天然剔除;钉桩三测。
- **settle/affinity distlock 协同(重点 3)**:独立 key(TTL>sweep 周期),同 namespace 同 slot;follower 非阻塞跳过;Redis 不可用回退双实例幂等扫;启动 stagger 保"先 settle 后学习"顺序。
- **决策时/终态双门与 settle 口径一致**:recordFeedbackAsync 单 choke 点门 + ReportRoutingOutcome 计数器前丢弃 + emitTuningSignal 门;合成轮在五个面全部封闭,普通流量闭环各环有产出与消费方(R30 基准无回退)。
- **session_intent_cache 与 settle 无回归交集(重点 5)**:cache 命中早返回,不 stash 反馈;R37 typed-nil 归一化只影响 Redis 回源 fail-open;affinity 只读 selections 与 cache 零共享状态。
- **summary/title generator 闭环(重点 4,窗口净零改动)**:loopback 自带 X-Gw-Source-Actor+per-boot token,gt_/gs_ 命名空间避免污染父会话聚合;selection 行由 R38 过滤兜住;伪造入口被 StripUntrustedCorrelationHeaders 封死。
- **未知 action 的影子轮边界**:followUpSourceActor 对未知 action 返回 "" → origin_actor NULL → R38 Go 侧过滤不命中,但已文档化为对账口径,非缺陷。

## 三、未覆盖项与原因

session_summaries 计数最终 upsert 写手未逐行定位(链到 analysis/optimizer 一带即止,发现 #1 的 join 侧证据已独立成立);灰度门槛与复算需真机 Prometheus/库;ML/ONNX 配对、回放小写 auto、多维进度 RFC 窗口零触碰;build/vet/test 未实跑(只读约束)。
