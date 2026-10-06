-- ===========================================================================
-- File:          sql/migrations/startup/835_modality_verify_probe_ledger.sql
-- Migration:     835
-- Database:      llm_gateway
-- Purpose:       给自检台账 system_probe_runs 的 task_type 词表加一个值
--                modality_verify，让多模态定时核实的每一次尝试都进台账。
--
-- 立项依据（2026-10-06 真库 + 代码实测，非推断）
--
-- 目标里有一句是「能自动对未曾标注核实过的模型定时进行核实。**这个需要加入
-- 到自检任务中**」。逐项复核接线之后，前半句是满足的、后半句**不是**：
--
--   · 满足的部分：bg.ModalityVerification 是定时循环，cmd/gateway/main.go
--     两处 authoritative 块里都有 `bg.NewModalityVerification(...).Run(ctx)`，
--     且 `cmd/gateway/main_authoritative_wiring_test.go` 钉着这两行不许消失。
--
--   · **不满足**的部分：它在运维的「自检任务」视图里**完全不存在**。四条
--     独立证据，全部本轮实测：
--     1) 不进统一自检队列。该队列是 bg.ProbeQueue（带 Enqueue/Cancel/Claim
--        lease/RequeueExpiredLeases），背靠 public.credential_probe_queue。
--        核实循环走的是自己的 time.NewTicker，从不 Enqueue。
--     2) 不进台账。真库读数：system_probe_runs 的 task_type 只有一个取值
--        `chat_tool`（1487 行，最近 2026-10-03），无任何 modality 相关行。
--     3) 不进请求遥测。语义探针（bg/modality_semantic_probe.go）用
--        net/http 经 internal/upstreamurl **直连上游**，不走网关自己的
--        handler，所以不产生 request_logs，探针流量在 request_logs 的
--        成本/延迟统计里完全不可见。
--     4) 尝试记录只在内存。m.attempts 是进程内 map，重启即丢。
--
-- ⇒ 后果不是「数据没存」，而是**运维没有任何办法知道它在跑**：跑了多少、
--   哪些模型被跳过、为什么跳过、失败在哪一步，全在 slog 里。实测
--   modality_verification 的日志只到 slog.Info/Warn，没有任何落库出口。
--
-- 为什么本次只接台账，不接统一自检队列
--
-- 决定是「只接台账」（人裁决，2026-10-06）。理由也在这里记下来，免得下
-- 一个读代码的人以为队列接入已经做过了：
--
--   · 队列接入不是小改动。credential_probe_queue 按 (credential, model)
--     派发，而语义探针是**两阶段**的（bg/probe_modality.go 先判结构级
--     「上游收不收这个模态的内容块」，再由 bg/modality_semantic_probe.go
--     发随机色块挑战图做语义级「真能读」判别），且带日预算
--     (LLM_GATEWAY_MODALITY_VERIFY_DAILY_BUDGET) 与准入闸门
--     (modalityVerifyAdmit，9 条)。把这套塞进 Claim/lease 模型是一次实质
--     重构，不是一段 INSERT 能顺带做完的。
--   · 台账是**先让事情可见**的那一步。看不见的问题没法排期，可见之后
--     才谈得上要不要为它做队列化。
--   · 本迁移**不改**探针的出网方式。直连上游是刻意的：它保证探针不占用
--     网关的路由/记账/限流通道，也就不会因为网关自身故障而把「核实」变成
--     「测网关」。改动它的代价（隔离性、凭据边界）远大于本条收益。
--
-- 为什么 CHECK 可以安全地「先删再加」
--
-- system_probe_runs 是按 created_at RANGE 分区的表，CHECK 挂在父表上并
-- 向叶子传播。本迁移**只放宽**词表：原 6 个值全部保留，只多一个
-- modality_verify。因此：
--   · 既有行一定仍然满足新约束（它们的 task_type 都取自原 6 值）⇒
--     **结构上不可能因存量数据失败**，不需要 833 那套 NOT VALID 手法。
--   · 与 833 的差别是实质性的：833 是**收紧**（挡住负价），存量里若有负价
--     就会让整轮部署中止，所以必须 NOT VALID；本条是放宽，加 NOT VALID
--     反而会让它永远不被规划器纳入检查，白白浪费一次验证扫描。
--   · 幂等：DROP ... IF EXISTS + ADD，可安全重放。
--
-- ⚠ 重放会覆盖后续新增的取值：本条 ADD 的词表是**硬编码**的。若将来有
--   别的迁移再往 task_type 加值，而本条被内容指纹通道重放，那一个新值会
--   被抹掉。与 833 同一套语义（内容未变则不重放），但方向相反，这里放宽的
--   代价是「丢掉别人后来加的东西」。改法见 down 段末尾的提醒。
--
-- 台账行的形状（由 bg/modality_verification.go 的 recordProbeLedger 写入）
--
--   task_id       = 0     —— **刻意**：多模态核实不是队列任务，没有队列
--                              任务号。0 是「本行不属于 credential_probe_queue
--                              任何任务」的显式标记，合成一个哈希任务号会
--                              在按 task_id 分组的看板上伪装成别的任务的执行。
--   task_type     = 'modality_verify'
--   automaticity  = 'automatic'
--   credential_id = 目标的 credential_id（NOT NULL，见下）
--   raw_model     = 目标的 raw_model（NOT NULL，见下）
--   source        = 'modality_verification'
--   worker_id     = 'modality-verification-worker'
--   status        = success / failed / skipped（取值在既有 CHECK 词表内）
--   skip_reason   = 准入闸门原因（纯函数 modalityVerifyAdmit 的第二个返回值）
--
-- ⚠ 「每轮一行」与「每次核实一行」的差异（2026-10-06 显式记录，不隐瞒）
--
--   决策原文是「每轮往 system_probe_runs 写一行」。实现按**每次核实一个目标
--   写一行**落地，原因是这张表按 (credential_id, raw_model) 造：两列都是
--   NOT NULL，且各带一个 btree 索引（idx_system_probe_runs_credential /
--   idx_system_probe_runs_model）。一轮汇总要落成一行，就只能给
--   credential_id / raw_model 塞哨兵值，那样的行在按凭据、按模型的看板上
--   既没有归属也没有可行动性。逐目标写则每行都指向一个具体的
--   (凭据, 模型) 与一个具体的跳过原因。
--
-- 幂等：不改数据、不动索引、不动分区。只重建一个 CHECK 约束。
-- ===========================================================================
BEGIN;

ALTER TABLE public.system_probe_runs
    DROP CONSTRAINT IF EXISTS system_probe_runs_task_type_check;

ALTER TABLE public.system_probe_runs
    ADD CONSTRAINT system_probe_runs_task_type_check
    CHECK (task_type = ANY (ARRAY[
        'direct_ping'::text,
        'gateway_ping'::text,
        'chat_minimal'::text,
        'chat_tool'::text,
        'chat_stream'::text,
        'http_ping'::text,
        -- 835 新增：多模态定时核实的语义探针（bg/modality_semantic_probe.go）。
        -- 它直连上游、不走网关，因此不产生 request_logs —— 台账是它**唯一**
        -- 的落库出口。没有这个值，核实循环的运行状况在运维侧完全不可见。
        'modality_verify'::text
    ]));

COMMENT ON CONSTRAINT system_probe_runs_task_type_check ON public.system_probe_runs IS
    'Vocabulary of self-check task kinds. 835 added modality_verify so the multimodal verification loop (bg.ModalityVerification) leaves a row per attempt in the self-check ledger. The semantic probe calls the upstream directly (internal/upstreamurl) and therefore never reaches request_logs, so this table is its only durable trace; without the value, "is the verifier running, what did it skip and why" is answerable only from logs that survive until the next restart. task_id=0 on those rows is deliberate: they are not credential_probe_queue tasks and must not be grouped as if they were.';

COMMIT;
