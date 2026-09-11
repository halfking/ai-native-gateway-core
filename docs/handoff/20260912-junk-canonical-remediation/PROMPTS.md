# junk canonical 治理收口 — 后续会话提示词

## 上下文

前序会话（sess_05d464ee-ecb4-4edb-a1db-cca167ffefa6，2026-09-12）已完成运营者侧 junk canonical 治理全部落地：fixable 1 经 `-apply` 修复、withheld 13 逐条定夺后按 manual 路径单事务修复（`4.6` 行按家族拆分、`5.3-flash` 裸名排除）、浏览器抽屉复测通过、0/37 可路由根因（凭据 77 状态残留）经 reset 端点修复为 37/37。详见 `docs/handoff/20260912-junk-canonical-remediation/HANDOFF.md`。治理备份 `/tmp/backup_junk_canonical_20260912_0147.sql`。

**未做**（有意）：review `free` 行处置；全局别名多目的地卫生轮；本机部署从 2082 升级到 2084 线。

---

## 主提示词（用于新会话）

聚焦目标：junk canonical 治理后的独立收尾项。本机在跑 2.5.4.2082（cf7256f6），main@aaf8d9edb（2084 线）。

三项可选任务（按优先级）：

1. **review `free`（models_canonical id 2664333）定夺**：`govern-junk-canonical` 诊断唯一的剩余 suspect（review 级，证据 glm-5.2:free / inkling:free / minimax-m3:free 等 0.99 同分）。先查 `openrouter/free`（provider 21，pm 2661145）的实际语义与流量（request_logs 按 raw_model_name 查 90 天），再定夺：若确认是 OpenRouter 免费池伪模型则保留并加白名单说明；若确认应指向具体模型则按 manual 模板单事务处置并复测。
2. **全局别名卫生轮（范围先探后定）**：`SELECT raw_name, string_agg(...) FROM model_aliases ma JOIN models_canonical mc ... WHERE status='active' GROUP BY raw_name HAVING count(DISTINCT canonical_id) > 1` 找出全部多目的地活跃别名；区分 (a) 目的地都是 canonical 自名（resolver 精确匹配拦截，惰性）与 (b) 非 canonical 拼写的真歧义（`LIMIT 1` 不确定路由，如 `deepseek-v4` → 120 与 260425 快照行）。只处置 (b)，逐条定夺，禁止批量盲改。
3. **本机部署升级 2082→2084 线**：deploy-local.sh 配方见记忆 local-deploy-gotchas（.env.local 从 bin/current/env 重建、先 docker stop）；注意新 migration 694（request_logs promote self-heal）会随二进制启动应用，升级后核对 schema_migrations 与 gateway_db_revision_sequences。

约束：不自动开新一轮审计；数据操作先 pg_dump 三表备份、单事务、事务内复核后 COMMIT；测试数据操规照旧；浏览器验证用 cua 坐标点行首列、evaluate 现已可用（滚动仍禁用 cua/dom_cua.scroll）。建议 skills：handoff、session-audit-gate、browser-use:control-browser
