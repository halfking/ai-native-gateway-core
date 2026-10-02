-- Migration 813 down: 不提供 heap→columnar 反向转换。supplier_errors 的
-- 列存形态是 10-01 事故漂移（R18 正典单族之外），恢复只会重新打开
-- UPDATE/DELETE/tableoid 读毒面，并让 ensure 函数的自愈反噬环复活
-- （每小时 enforce_columnar_partition）。详见 813 头注。
DO $$
BEGIN
    RAISE NOTICE '813 down: no-op by design (columnar partitions on supplier_errors are the defect; see migration header)';
END $$;
