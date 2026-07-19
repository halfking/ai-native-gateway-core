-- Down: 448 — 保留列；如需回退 VIEW，用 migration 341 的 SELECT * 形式重建
-- （注意：hot/parent 类型漂移时 SELECT * UNION ALL 会失败）。

DO $$
BEGIN
  RAISE NOTICE '448 down: columns retained; recreate VIEW manually if needed';
END $$;
