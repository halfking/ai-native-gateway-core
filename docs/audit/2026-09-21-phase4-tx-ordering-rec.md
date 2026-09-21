# Phase 4 Transaction Ordering 长期建议 — R51 范围

**Status: 建议稿;critical-audit 实测验证 + 2026-09-21 重跑踩坑共 3 例。**

## 0. 引子

2026-09-20 Phase 1+2+3+4+5 跑完后做 critical audit(同一会话 commit f9449a79a),发现 4 处 phase transaction 静默丢弃的 bug:

1. `deepseek-v4-flash-ga` (id 2203408) + `deepseek-v4-pro-ga` (id 2438472) 在本地 + 252 双库
2. `qwen3-max-cn` (id 5318) 在本地 + 252 双库

根因:Phase 4 设计的 `UPDATE provider_models SET canonical_id=winner ... WHERE canonical_id=loser` 与 `UPDATE models_canonical SET status='disabled' ... WHERE id=loser` 写在同一事务里,**UPDATE 返回 row count 报告说改了 2 行,但 COMMIT 后 SELECT status 仍是 active**。

2026-09-21 重跑 dedup-cleanup 又踩一处:`ursm_node_snapshot_min` 的 UPDATE 因为 2083 万行无索引超时,**EXECUTE 在同 DO 块里无 SAVEPOINT 隔离**,超时回滚整段事务。

## 1. 两个 bug 的共同模式

| 维度 | 2026-09-20 -ga rows | 2026-09-21 ursm timeout |
|---|---|---|
| 症状 | UPDATE 返回 row count > 0,但 SELECT 后未持久 | EXECUTE 在长事务中失败,回滚前面所有改动 |
| 暴露点 | critical audit 主动 SELECT after-commit | 后续 phase 触达 ursm EXECUTE 时 |
| 假阴性 | 看起来脚本成功,实际状态没变 | 看起来脚本失败,实际什么都没改 |

两种 bug 都违反**「事务边界即真相」**这条最朴素的 SQL 工程原则:**不要相信 UPDATE 的 row count 报告,只看 COMMIT 后 SELECT 的结果。**

## 2. 长期建议(本轮已部分落地)

### 2.1 每行/每 EXECUTE 包嵌套 SAVEPOINT

**适用场景**:清理脚本里 `DO $$ BEGIN ... END $$` 的 EXECUTE 列表。任一 EXECUTE 失败只回滚自身 SAVEPOINT,不影响前后 EXECUTE。

```sql
DO $$
DECLARE
  i int;
BEGIN
  FOR i IN 1..array_length(target_tables, 1) LOOP
    BEGIN  -- SAVEPOINT 自动开始
      EXECUTE format('UPDATE %I SET canonical_id = ...', target_tables[i]);
      RAISE NOTICE 'OK %', target_tables[i];
    EXCEPTION WHEN OTHERS THEN
      RAISE NOTICE 'skip %: %', target_tables[i], SQLERRM;
      -- 自动 ROLLBACK TO SAVEPOINT,继续下一行
    END;
  END LOOP;
END $$;
```

**当前 canonical-dedup-cleanup.sql 缺失此模式** —— 所有 EXECUTE 在同一 DO 块,任一失败整段失败。本轮 (2026-09-21) 重跑踩坑即此。

### 2.2 COMMIT 后 SELECT verify-after-commit

**适用场景**:任何 phase transaction 在 COMMIT 之后立即 SELECT,断言状态确实落库。

```sql
BEGIN;
UPDATE models_canonical SET status='disabled' WHERE id = ANY($loser_ids);
-- expected N rows
COMMIT;

-- 关键: 不能信 UPDATE row count, 必须 SELECT 一次
DO $$
DECLARE
  not_disabled int;
BEGIN
  SELECT count(*) INTO not_disabled
  FROM models_canonical WHERE id = ANY($loser_ids) AND status <> 'disabled';
  IF not_disabled > 0 THEN
    RAISE EXCEPTION 'COMMIT did not persist disable for % rows', not_disabled;
  END IF;
END $$;
```

**本轮 2026-09-21 unmapped-cn-rename.sql 已采用此模式**(见 §4 commit-after verify 块)。建议反向回填到 `2026-09-20-canonical-dedup-cleanup.sql` 和 R51 新写的所有清理脚本。

### 2.3 per-snapshot re-point + disable 嵌套事务

**适用场景**:Phase 4 风格的「快照禁用 + 引用重定向」组合。原版把 re-point 和 disable 写在同一事务,**disable 的 status 持久性依赖事务提交顺序的隐式保证**。

正确写法应该是 per-snapshot SAVEPOINT:

```sql
DO $$
DECLARE
  loser record;
BEGIN
  FOR loser IN
    SELECT mc.id, mc.canonical_name,
      (SELECT id FROM models_canonical WHERE canonical_name = base_of(mc.canonical_name)) AS winner_id
    FROM models_canonical mc
    WHERE mc.canonical_name ~ '-cn$' AND mc.status='active'
  LOOP
    BEGIN
      UPDATE provider_models SET canonical_id = loser.winner_id
      WHERE canonical_id = loser.id;
      UPDATE model_aliases SET status='deprecated'
      WHERE canonical_id = loser.id;
      UPDATE models_canonical SET status='disabled',
        disabled_reason='folded into '||(SELECT canonical_name FROM models_canonical WHERE id=loser.winner_id)
      WHERE id = loser.id;
      -- COMMIT-after-verify, 即使本 SAVEPOINT 内亦要 SELECT 一次
      PERFORM 1 FROM models_canonical WHERE id=loser.id AND status='disabled';
      IF NOT FOUND THEN
        RAISE EXCEPTION 'disable did not persist for id=%', loser.id;
      END IF;
      RAISE NOTICE 'OK disabled % -> %', loser.canonical_name, loser.winner_id;
    EXCEPTION WHEN OTHERS THEN
      RAISE NOTICE 'skip %: %', loser.canonical_name, SQLERRM;
    END;
  END LOOP;
END $$;
```

外层不需要 BEGIN/COMMIT —— 因为每条 loser 已经 SAVEPOINT 包住,失败的不会污染成功的。

### 2.4 长事务场景的 statement_timeout 上限

**适用场景**:dedup-cleanup 这种跨 20+ 表的脚本。`ursm_node_snapshot_min` 2083 万行 UPDATE 必须 `SET statement_timeout = 0` 才能跑完。

但这又引入了风险:如果是 SQL bug 引起的死循环,statement_timeout=0 会把库锁死。

建议折中:
1. 脚本入口 `SET LOCAL lock_timeout = '5min'`(防无限等待锁)
2. 慢表 `SET LOCAL statement_timeout = 0` 只在该 EXECUTE 内生效
3. 主流程仍保持 `statement_timeout = '60s'` 默认

或者:**给慢表加索引**(最治本):
```sql
CREATE INDEX CONCURRENTLY ursm_node_snapshot_min_canonical_name_lower_idx
  ON ursm_node_snapshot_min USING btree (lower(canonical_name));
```

R51 应该把这条索引加进去。

## 3. 落地清单

| 优先级 | 项 | 落点 | 估时 |
|---|---|---|---|
| P1 | 给 ursm_node_snapshot_min 加 lower(canonical_name) 索引 | R51 migration 732 | 1h |
| P1 | 改写 canonical-dedup-cleanup.sql 的 §2 DO 块为 per-EXECUTE SAVEPOINT | R51 提交 | 2h |
| P1 | 改写 §7 DELETE 段为 per-snapshot SAVEPOINT + COMMIT-after verify | R51 提交 | 2h |
| P2 | 给 sql/fixes/ 下所有清理脚本统一加「COMMIT 后 SELECT verify」模板 | R51 | 4h |
| P2 | 写一个 SQL lint:检测 cleanup 脚本里裸 UPDATE 没有配套 SELECT-after-COMMIT | R52 | 2h |
| P3 | 把 verification 查询改为可以 fail-fast 的 SELECT (例如用 assert) | R52 | 1h |

## 4. 已有正面案例

- `sql/fixes/2026-09-21-unmapped-cn-rename.sql`(本会话) — 已采用 per-row SAVEPOINT + COMMIT-after SELECT 断言模式,可作为 R51 改写模板。

## 5. 不会改变的设计

`govern-junk-canonical -json` 仍是唯一对外契约。其内部可以加任何 SAVEPOINT/verify-after-commit 改造,但其**输出契约**(Suspects 列表、ActiveCanonicalRows 数)保持稳定。

任何清理脚本的输出「dedup 干净」必须能反向验证:
```bash
govern-junk-canonical -json | jq -e '.Suspects == null and .ActiveCanonicalRows > 0'
```

这一步放在 CI 或 cron 里就是下个层级的防护。