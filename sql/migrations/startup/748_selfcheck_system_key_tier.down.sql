-- Down for 748: 不可逆的数据修复，down 保持 no-op。
--
-- 把自检系统密钥降回 'default'（12 RPM）会原样复活探测成本方案
-- §2.3 的自增强风暴（86% 自检请求被网关 RPM 弹回），任何环境都不应
-- 执行该方向。若确需回滚 P0-1，行为开关在 settings（P0-3 的
-- probe.selfcheck.ratelimit_abort），key tier 修复按设计保留不回滚。

SELECT 1; -- intentional no-op (see comment above)
