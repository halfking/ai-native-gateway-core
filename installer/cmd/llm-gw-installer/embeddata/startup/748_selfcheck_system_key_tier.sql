-- Migration 748: 自检系统密钥 key_tier 存量修复（probe-cost-optimization P0-1）
--
-- 背景（docs/03-design/perf-2026-09-25-probe-cost-optimization.md §2.3）：
-- bg.EnsureSystemAPIKey 的历史 INSERT 未设置 key_tier，自检 worker 自动生成
-- 的系统密钥落到 api_keys.key_tier 列默认值 'default' —— 对应网关 RPM 限流
-- 的 12 RPM 档（domains/authentication/verifier.go tierDefaults）。自检实际
-- 发射 ~100 请求/分钟 → 48h 实测 86%（35,440/41,056）被自家网关弹回
-- （gw_rpm_exceeded），429 又把模型逐个打入"到期失败"矩阵，形成自增强风暴。
--
-- 修复：把自检系统密钥提升到 'system' 档（300 RPM）。RPM 限流按 key 分桶
-- （checkGatewayRateLimit → AdmitRPM(keyInfo.ID, limit)），本 UPDATE 只影响
-- 自检自己的桶，不触碰其他密钥。
--
-- 双通道：installer 走本文件；网关进程启动另有等价自愈
-- bg.HealSelfCheckSystemKeyTier（覆盖久未跑 installer 的存量部署）。
-- WHERE 全限定 + 目标值为常量，天然幂等（二次执行 0 行）。

BEGIN;

UPDATE public.api_keys
SET key_tier = 'system'
WHERE COALESCE(is_system, FALSE) = TRUE
  AND owner_user IN ('self-check-worker', 'credential-selfcheck-worker')
  AND COALESCE(key_tier, 'default') = 'default';

COMMIT;
