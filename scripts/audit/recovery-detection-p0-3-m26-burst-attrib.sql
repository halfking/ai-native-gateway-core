-- ★★ 关窗前例行核查取证（★ 2026-11-11 01:55 +08，★ 211 真库）：★ 01:28–01:33 探针突发（峰值 103/min）的归因
-- ★★ 实测结论：★ 突发 = `credential_recovery` 触发（285/307）+ 263 行落 `next_retry_seconds=21600`
-- ★★   ⇒ ★★ **pre 窗口同样相位同样指纹**（19点/01点/07点 = 257/254/254；post 19点/01点 = 269/276）
-- ★★   ⇒ ★★ ⇒ **既有 6h 周期批量**（★ 主档 §5.9.21.13.19 已记录 13:30→19:30→01:30→07:30）
-- ★★   ⇒ ★★ ⇒ **不是**「第二次部署的 tier 冷启动窗口」⇒ ★★ **era 切点仍有效 ⇒ 不重划**
-- ★★ 突发归因：① 是「一次性事件」还是「路径级缺口」？② 减半修复是否覆盖 credential_recovery 路径？
SET TIME ZONE 'Asia/Shanghai';

-- ① post 窗口内：每小时 credential_recovery 的量 + 其中 21600 顶格占比
SELECT 'A_hourly_cr' AS sec,
       to_char(date_trunc('hour', created_at),'MM-DD HH24') AS h,
       count(*) FILTER (WHERE trigger_kind='credential_recovery') AS cr,
       count(*) FILTER (WHERE trigger_kind='credential_recovery' AND next_retry_seconds=21600) AS cr_21600,
       count(*) FILTER (WHERE trigger_kind='credential_recovery' AND next_retry_seconds=3600)  AS cr_3600,
       count(*) FILTER (WHERE trigger_kind='credential_recovery' AND next_retry_seconds=1800)  AS cr_1800,
       count(*) FILTER (WHERE trigger_kind='periodic') AS periodic
FROM node_probe_runs
WHERE created_at >= '2026-11-10 13:42:30+08' AND created_at < now()
GROUP BY 2 ORDER BY 2;

-- ② ★ 对照：pre 窗口（切点前 24h）同样的分解 ⇒ 减半是否本来就没覆盖 credential_recovery
SELECT 'B_pre' AS sec,
       to_char(date_trunc('hour', created_at),'MM-DD HH24') AS h,
       count(*) FILTER (WHERE trigger_kind='credential_recovery') AS cr,
       count(*) FILTER (WHERE trigger_kind='credential_recovery' AND next_retry_seconds=21600) AS cr_21600,
       count(*) FILTER (WHERE trigger_kind='periodic') AS periodic
FROM node_probe_runs
WHERE created_at >= '2026-11-09 13:42:30+08' AND created_at < '2026-11-10 13:42:30+08'
GROUP BY 2 ORDER BY 2;

-- ③ ★ 突发窗口里的 6 个 credential：是谁？（★ 是不是 featured 名单内的）
SELECT 'C_creds' AS sec,
       credential_id,
       count(*) AS n,
       count(DISTINCT raw_model_name) AS models,
       count(*) FILTER (WHERE success) AS ok,
       count(*) FILTER (WHERE next_retry_seconds=21600) AS rs21600,
       min(created_at)::text AS first_at,
       max(created_at)::text AS last_at
FROM node_probe_runs
WHERE created_at >= '2026-11-11 01:28:00+08' AND created_at < '2026-11-11 01:34:00+08'
GROUP BY 2 ORDER BY n DESC;

-- ④ ★ 同一天里 21600 顶格在「非突发时段」的存量（★ 判断 263 是不是常态）
SELECT 'D_rs21600_by_min' AS sec,
       to_char(date_trunc('minute', created_at),'MM-DD HH24:MI') AS m,
       trigger_kind,
       count(*) AS n
FROM node_probe_runs
WHERE created_at >= '2026-11-11 00:00:00+08' AND created_at < now()
  AND next_retry_seconds = 21600
GROUP BY 2,3 ORDER BY 2,3;

-- ⑤ ★ 突发时段前后各 30 分钟的 21600 顶格总数（★ 冷启动瞬态 vs 常存）
SELECT 'E_window_21600' AS sec,
       CASE WHEN created_at < '2026-11-11 01:28:00+08' THEN 'pre30'
            WHEN created_at < '2026-11-11 01:34:00+08' THEN 'burst6'
            ELSE 'post30' END AS seg,
       count(*) AS n_21600,
       count(*) FILTER (WHERE trigger_kind='credential_recovery') AS cr,
       count(*) FILTER (WHERE trigger_kind='periodic') AS periodic
FROM node_probe_runs
WHERE created_at >= '2026-11-11 00:58:00+08' AND created_at < '2026-11-11 02:04:00+08'
  AND next_retry_seconds = 21600
GROUP BY 2 ORDER BY 2;
