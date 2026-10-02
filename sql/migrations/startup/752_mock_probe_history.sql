-- 752: mock_probe_history 历史表 + 按日分区函数（Mock Probe 生产入口收口轮，2026-09-27）
--
-- 背景：Mock Probe 通道子系统（mock-fast / mock-slow × stream / non-stream
-- = 2x2 探测，docs/design/2026-09-23-mock-probe-channel）的历史落库表。
-- DDL 原以 migrations/036_mock_probe_history.sql 存放，头注释拍板"ops 在
-- 维护窗口手工执行、不走 startup revision-sequence"——本轮回看判定为
-- 745_report_snapshots 同款病灶（死放顶层目录、无执行器接线）：仅 252
-- 生产库被手工跑过，本机库等其余环境永远缺表，MockProbeEnabled=true 时
-- 历史落库静默降级为 Warn 噪音。子系统本轮接入生产入口（cmd/gateway）
-- 后历史落库成为生产能力，DDL 收编进 startup 正典通道，顶层 036 删除
-- （与 db/migrations/036_fp_slot_limit 撞号异文件的混淆一并消除）。
--
-- R73 审计 A-6 显式登记：本表按日分区只建不删——partition_manager 与
-- 任何 worker 均无本表的 drop/清理路径，保留期无界（头注自估 ≈1.2 万行
-- /日 ≈ 4.4M 行/年，有界增速）。与 750 usage_facts 同病；TTL 由 owner
-- 拍板后以独立迁移收口（届时与本声明一并更新）。
--
-- 内容相对 036 的两处加固（751/750 修订的先例对偶）：
--   ① 时区钉扎：036 的函数用 current_date + d::timestamptz 求值，随会话
--      时区漂移——UTC 会话产出与上海日边界错位 8h 的分区窗口，且与后续
--      上海日历分区相邻日重叠后 ATTACH 必报 overlap、永不自愈（750 R69
--      修订实证的同款病）。函数级 SET timezone = 'Asia/Shanghai'（proconfig，
--      函数入口生效、先于 DECLARE 初始化器），日期参数显式上海日历派生。
--   ② 搬移后挂接（move-then-attach，750 同款）：DEFAULT 分区已存当日行
--      时直接 CREATE TABLE ... PARTITION OF 必被 DEFAULT 约束校验击杀
--      （boot 序：先跑探测落 DEFAULT、后建当日分区的窗口真实存在——
--      启动兜底 bootstrap 只在建表后调用一次，跨午夜长期运行靠 writeLoop
--      每日 ensure tick 补建，彼时 DEFAULT 已累积当日行）。advisory lock
--      串行化多实例并发 ensure；INCLUDING DEFAULTS INCLUDING INDEXES 让
--      ATTACH 的分区索引（PK + 两查询索引）走元数据挂接。
--
-- 表设计（承 036 不变）：独立于 request_logs——mock 探测流量绝不写
-- request_logs（设计原则"mock 数据不污染真实表"，且避免吃满 request_logs
-- 配额）；DEFAULT 分区兜底（当日分区函数失败时探测记录仍可落库）。
--
-- 幂等性：CREATE TABLE/INDEX IF NOT EXISTS + CREATE OR REPLACE FUNCTION
-- + 函数内 pg_inherits 短路——252 库已按 036 手工建过表与旧版函数，
-- 重放本迁移安全（OR REPLACE 顺带把存量旧版函数升级为钉扎版）。
--
-- 调用方：internal/mockprobe HistoryStore 启动 bootstrap + writeLoop 每日
-- ensure tick（SELECT mock_probe_history_daily_partition()，无参签名与
-- 036/存量调用点兼容）。

CREATE TABLE IF NOT EXISTS mock_probe_history (
    id              BIGSERIAL,
    probe_time      TIMESTAMPTZ NOT NULL DEFAULT now(),
    channel         TEXT NOT NULL,            -- e.g. 'mock-fast:stream'
    supplier        TEXT NOT NULL,            -- 'mock-fast' | 'mock-slow'
    stream          BOOLEAN NOT NULL,
    protocol        TEXT NOT NULL,            -- 'openai' | 'anthropic' | 'response' | 'gemini'
    latency_ms      INTEGER NOT NULL,
    status_code     INTEGER NOT NULL,
    error_code      TEXT,                     -- nullable，e.g. 'timeout' / 'http_500'
    request_id      TEXT,                     -- 来自上游 mock 响应
    failure_streak  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (id, probe_time)
) PARTITION BY RANGE (probe_time);

-- DEFAULT 分区兜底（当日分区函数失败时探测记录仍可落库）
CREATE TABLE IF NOT EXISTS mock_probe_history_default
    PARTITION OF mock_probe_history DEFAULT;

-- 查询索引（分区父表普通索引，自动向现有及未来分区传播）
CREATE INDEX IF NOT EXISTS idx_mock_probe_history_supplier_time
    ON mock_probe_history (supplier, probe_time DESC);
CREATE INDEX IF NOT EXISTS idx_mock_probe_history_channel_time
    ON mock_probe_history (channel, probe_time DESC);

-- 按日分区函数：无参（兼容 036 存量调用点），内部按上海日历派生当日 +
-- 次日滚动预建。函数级 SET timezone 钉扎边界换算（751 的对偶：这里直接
-- 写进函数定义，存量库由 CREATE OR REPLACE 收敛）。
CREATE OR REPLACE FUNCTION mock_probe_history_daily_partition()
RETURNS void LANGUAGE plpgsql SET timezone = 'Asia/Shanghai' AS $$
DECLARE
    d0 DATE := (now() AT TIME ZONE 'Asia/Shanghai')::date;
    d  DATE;
    pname    TEXT;
    start_ts TIMESTAMPTZ;
    end_ts   TIMESTAMPTZ;
BEGIN
    FOREACH d IN ARRAY ARRAY[d0, d0 + 1] LOOP
        pname    := format('mock_probe_history_%s', to_char(d, 'YYYYMMDD'));
        start_ts := d::timestamptz;
        end_ts   := (d + INTERVAL '1 day')::timestamptz;

        -- 幂等短路：该日分区已挂接则无事可做。
        IF EXISTS (
            SELECT 1
            FROM pg_inherits i
            JOIN pg_class c ON c.oid = i.inhrelid
            WHERE i.inhparent = 'public.mock_probe_history'::regclass
              AND c.relname = pname
        ) THEN
            CONTINUE;
        END IF;

        -- 串行化并发 ensure（多实例共享库 / boot 与 writeLoop tick 竞态）。
        PERFORM pg_advisory_xact_lock(
            hashtext('mock_probe_history_daily_partition:' || pname));

        -- 拿到锁后复查：等锁期间别的入口可能已建好。
        IF EXISTS (
            SELECT 1
            FROM pg_inherits i
            JOIN pg_class c ON c.oid = i.inhrelid
            WHERE i.inhparent = 'public.mock_probe_history'::regclass
              AND c.relname = pname
        ) THEN
            CONTINUE;
        END IF;

        -- 独立表承接当日行：INCLUDING INDEXES 让 ATTACH 的分区索引
        -- （PK + 两查询索引）挂接走元数据匹配。
        EXECUTE format(
            'CREATE TABLE %I (LIKE mock_probe_history INCLUDING DEFAULTS INCLUDING INDEXES)',
            pname);

        -- 挡住并发写入落进 DEFAULT 的搬移窗口；持锁段 = 搬当日行 + 挂接。
        EXECUTE 'LOCK TABLE mock_probe_history_default IN ACCESS EXCLUSIVE MODE';

        -- 把 DEFAULT 内落入本日边界的行搬进新表（否则 ATTACH 的约束
        -- 校验必炸）。mock probe 低频（默认 4 行/30s ≈ 1.2 万行/日上限），
        -- 搬移代价有界。
        EXECUTE format(
            'WITH moved AS (
                 DELETE FROM mock_probe_history_default
                 WHERE probe_time >= %L AND probe_time < %L
                 RETURNING *
             )
             INSERT INTO %I SELECT * FROM moved',
            start_ts, end_ts, pname);

        EXECUTE format(
            'ALTER TABLE mock_probe_history ATTACH PARTITION %I FOR VALUES FROM (%L) TO (%L)',
            pname, start_ts, end_ts);
    END LOOP;
END $$;

-- 首次迁移滚动预建当日 + 次日；此后由网关侧 HistoryStore 启动 bootstrap
-- 与 writeLoop 每日 ensure tick 接管（internal/mockprobe/runner.go）。
SELECT mock_probe_history_daily_partition();
