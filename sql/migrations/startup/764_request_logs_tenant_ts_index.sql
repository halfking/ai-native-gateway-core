-- 764: request_logs 分区家族补 (tenant_id, ts DESC) 索引（R36-B3，三十六轮）
--
-- 缺陷（2026-09-30 三十六轮真库实证，252-dev）：341 只给 request_logs_hot
-- 建了 idx_request_logs_hot_tenant_ts，分区父表 request_logs 及各月分区从未
-- 有 tenant 前导索引——tenant 维度的 days>7 聚合（admin 租户统计 byModel/
-- byApp/daily/credits，R36-A1 修复后读 hot ∪ 父表）对每分区全表扫：
--   · request_logs_2026_09（1.4M 行）上 3 行租户的谓词计数也要并行
--     seq scan 6.5s（EXPLAIN 实测，Rows Removed by Filter: 465665×3 workers）；
--   · default 租户 30 天计数 21.5s。
-- usage_ledger 家族分区自带 tenant_id_ts 索引（分区模板），无需对齐。
--
-- 设计：在分区父表上 CREATE INDEX 自动级联全部现存分区；后续 ATTACH 的新
-- 分区同样获得（分区索引语义，新分区的月度建表路径无需改动）。形态对齐
-- hot 侧 idx_request_logs_hot_tenant_ts：(tenant_id, ts DESC)。
--
-- 注意（运维窗口）：分区表不支持 CREATE INDEX CONCURRENTLY；百万行级分区
-- 的索引构建为秒级，SHARE 锁窗口可控，随部署序列执行。
--
-- 幂等：IF NOT EXISTS + 台账自登记（695-705 定式）。顶层语句，无显式事务
--（installer psql --single-transaction 纪律，runner.go:416）。

CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_ts
    ON public.request_logs USING btree (tenant_id, ts DESC);

INSERT INTO public.schema_migrations (version, description)
VALUES ('764', 'request_logs partition family tenant_ts index (R36-B3: 341 only indexed the hot side; tenant-scoped >7d aggregates seq-scanned every partition on real data)')
ON CONFLICT (version) DO NOTHING;
