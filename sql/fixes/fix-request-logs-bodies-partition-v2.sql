-- 修复 request_logs_bodies 分区问题（修订版）
-- 问题：当前时间无法找到对应分区，且 columnar 存储不支持 DELETE
-- 解决方案：创建 default 分区作为 heap 存储

BEGIN;

-- 创建 default 分区（heap 存储，不使用 columnar）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_tables 
        WHERE schemaname = 'public' 
        AND tablename = 'request_logs_bodies_default'
    ) THEN
        CREATE TABLE request_logs_bodies_default 
        PARTITION OF request_logs_bodies DEFAULT;
        
        RAISE NOTICE 'Created request_logs_bodies_default partition';
    ELSE
        RAISE NOTICE 'request_logs_bodies_default already exists';
    END IF;
END $$;

-- 验证修复：插入到父表，确认路由可达，并**从父表**清理验证行
--
-- R89-DU（209 号）更正：原实现把**清理**与**判定**都限定在 default 分区
-- （`FROM request_logs_bodies_default WHERE request_id = test_request_id`）。
-- 而本脚本存在的目的恰恰是「让写入别再落到越界分区」⇒ **目的达成之后**，
-- 写入会落在真正的日期分区上，于是：
--   ① `test_result` 恒为 0 ⇒ 报 `Insert test FAILED` —— **误诊**（写入其实成功了，
--      只是没落在 default）；运维会照着这个假象去查分区路由，而路由是好的；
--   ② `DELETE` 够不着那行 ⇒ **每次运行都在生产 bodies 表里永久留一行**；
--   ③ 该 WARNING 非致命，且本脚本**没有** `ON_ERROR_STOP` ⇒ 退出码 0
--      ⇒ 泄漏与误诊对 CI/自动化**完全不可见**。
-- 改法：判定与清理都回到**写入发生的那一层**（父表），default 分区的命中数
-- 只作为诊断信息输出、**不参与**通过与否的判定。
DO $$
DECLARE
    test_request_id TEXT := 'fix-verification-' || extract(epoch from NOW())::TEXT;
    test_result    INTEGER;   -- 父表命中：唯一的通过判据
    default_result INTEGER;   -- default 分区命中：仅诊断
BEGIN
    -- 尝试插入测试数据
    INSERT INTO request_logs_bodies (request_id, ts, request_body)
    VALUES (test_request_id, NOW(), '{"test": true}'::jsonb);

    -- 判定看**父表**：不管它落在哪个分区，写入成功就该数得到。
    SELECT COUNT(*) INTO test_result
    FROM request_logs_bodies
    WHERE request_id = test_request_id;

    -- default 分区命中数只作诊断（0 是**正常**的——分区路由修好后就该是 0）。
    SELECT COUNT(*) INTO default_result
    FROM request_logs_bodies_default
    WHERE request_id = test_request_id;

    IF test_result > 0 THEN
        RAISE NOTICE 'Insert test PASSED: 父表 % 行（default 分区 % 行，0 表示路由已修复）',
          test_result, default_result;
        -- 清理也必须回到父表：只清 default 分区会漏掉落在日期分区上的那行。
        DELETE FROM request_logs_bodies WHERE request_id = test_request_id;
        RAISE NOTICE 'Verification row cleaned up from request_logs_bodies';
    ELSE
        -- 写不进去是硬问题（分区约束/缺列/表不存在）⇒ 必须中止，而不是留个
        -- WARNING 让脚本以 0 退出，那会让「验证失败」与「脚本成功」无法区分。
        RAISE EXCEPTION 'Insert test FAILED：父表 request_logs_bodies 里数不到刚插入的验证行（回滚）';
    END IF;
END $$;

COMMIT;

-- 显示分区信息
\echo ''
\echo '=== Partition Status ==='
SELECT 
    c.relname as partition_name,
    CASE WHEN am.amname = 'columnar' THEN 'columnar' ELSE 'heap' END as storage,
    pg_size_pretty(pg_total_relation_size(c.oid)) as size
FROM pg_class c
LEFT JOIN pg_am am ON c.relam = am.oid
WHERE c.relname LIKE 'request_logs_bodies%'
  AND c.relkind IN ('r', 'p')
ORDER BY c.relname;
