# Handoff: 运营者侧 junk canonical 治理落地与验证

**会话**: sess_05d464ee-ecb4-4edb-a1db-cca167ffefa6
**日期**: 2026-09-12
**状态**: ✅ 全部落地——14 行垃圾 canonical 弃用、路由零漂移、0/37 可路由根因修复为 37/37
**运行环境**: 本机容器 kx-llm-gateway-local:2.5.4.2082（cf7256f6）；main 已 ff 至 aaf8d9edb（2084 线）

---

## 结论 / 根因

1. **Junk canonical 治理全部完成**。诊断 15 suspects = fixable 1 + review 1 + withheld 13：
   - fixable `3.1-pro` 由 `govern-junk-canonical -apply` 修复（→ gemini-3.1-pro），复诊 0 fixable。
   - withheld 13 条逐条定夺后按 README manual 路径单事务 DML 修复。**12 条单家族行**（refs/别名全属目标家族）整体指向唯一目标；**`4.6` 行是真分叉行**（同时压着 36994 的 `grok/4.6` 与 provider 32 的 `glm-4.6`），按家族拆分：裸 `4.6`/`4-6`→grok-4.6、`glm-4.6` 三拼→真 glm-4.6；`5.3-flash` 行排除裸 `5.3`/`glm-5.3` 拼写（归真 glm-5.3），顺带消除了 resolver 别名 `LIMIT 1` 无排序导致的既存双重路由。
   - review `free`（id 2664333）按设计保留：证据 0.99 同分分歧（glm-5.2:free / inkling:free / …），非自动可修。
2. **可路由性 0/37 根因不在探活/绑定**：凭据 77（apinext-1）自身 `availability_state='suspended'` + `quota_state='permanently_exhausted'`（余额 1000 却标耗尽，历史状态残留）。37 个 cmb 绑定与 provider_models 全部 available。视图 `v_routable_credential_models` 的 `availability_suspended` 细分来自**凭据级** credentials.availability_state。经 `POST /api/providers/36994/credentials/77/reset-availability` + `/reset-quota`（自动失效候选缓存）后 **37/37 可路由（100%）**。
3. **浏览器复测通过**：`claude/opus-5` 抽屉"最佳 100%"落 claude-opus-5；`grok/4.6` 抽屉"最佳 100%"落 grok-4.6；junk 行从匹配列表消失；表格 standardized name 全部修正。

## 改动文件与关键行为

**代码零改动**（纯数据态治理）。DB 变更（单事务，已提交）：

| 对象 | 变更 |
|---|---|
| models_canonical | 14 行（3198338-3198341、3198350-3198359，3.1-pro 由工具处理）→ status='deprecated' |
| provider_models | 55 行机械重定向（canonical_id+standardized_name）+ 2 行 4.6 拆分（pm 3195175→grok-4.6、pm 125777→glm-4.6） |
| model_aliases | 66 条 upsert 到目标行（junk 名+别名保路由；4.6/4-6→grok-4.6 单独处理）+ 73 条 junk 行别名弃用 |
| credentials (77) | 经 admin API reset：availability_state→ready、quota_state→ok |

工具/脚本：`scripts/govern-junk-canonical`（-apply 只处理 fixable，withheld 走 `sql/fixes/fix-discovery-junk-canonical-rows.sql` 第 3 部分 DML 模板）。备份：本机 `/tmp/backup_junk_canonical_20260912_0147.sql`（三表 data-only）。

## 测试命令与结果

```
set -a; source .env.local; set +a
go run ./scripts/govern-junk-canonical -json        # 治理前: 1 fixable + 1 review + 13 withheld；治理后: 仅 review free
go run ./scripts/govern-junk-canonical -apply       # 修复 fixable，post-apply 0 fixable
go test ./scripts/govern-junk-canonical/ ./modelname/   # ok / ok
```

SQL 复核（治理事务内 + 会后审计，全过）：
- pm 指向 junk 行 = 0；junk 行上活跃别名 = 0；junk 行 deprecated = 14
- 全库 provider_models 指向任何 deprecated canonical = 0
- canonical 正确但 standardized_name 仍是 junk 名 = 0（含 canonical_id NULL 形态）
- models_canonical / model_aliases 无 FK 依赖表（唯一引用面就是 provider_models）
- 22 个裸名拼写（opus-5、4.6、5.3、3.8、k3、v4…）别名全部单目的地且家族正确
- work_type_model_route 无 junk 名引用；request_logs 历史数据按设计不动

浏览器验收（IAB，/providers/36994?tab=models）：两行抽屉"最佳 100%"徽标落真 canonical；可路由性摘要 0/37(availability_suspended:37) → **37/37 (routable_ratio: 100%)**。

## 审计结论（双轴自审）

- **Standards 轴**：SQL 镜像工具 -apply 语义（重定向→别名 upsert→弃用别名→弃用行，含事务内三重复核）；幂等可重放；备份先行。事务 step-2 upsert 未写 client_profiles（NULL=全 profile），与 junk 时代别名一致，非缺陷。
- **Spec 轴**：任务四项（fixable -apply / withheld 逐条定夺 manual 路径 / 抽屉复测 / 可路由性观察与追查）全部完成，无越界（`free` 未动，pre-existing 别名歧义未扩大）。
- **合并轴**：本地 main 落后 origin/main 10 提交（win11 线，2083/2084 发版），无本地独有提交 → `merge --ff-only` 快进至 aaf8d9edb，禁 force-push 未触发；10 提交均不触及 modelname/ 与 govern 脚本，治理判定不受影响。

## 遗留风险

1. **review `free`（id 2664333）仍在**：junk 形态成立但目标同分（0.99×5），工具与人工都不该盲指。若要处置需运营者确认 `openrouter/free` 的语义（疑似 OpenRouter 免费池伪模型，重连到任一具体模型都是错的），建议单独一轮或加白名单。
2. **既存别名多目的地歧义（非本轮引入）**：如 `deepseek-v4` 同时活跃于 deepseek-v4-flash(120) 与 deepseek-v4-flash-260425、`claude-haiku-4-5` 标点双胞胎等——canonical 精确匹配优先可拦截同名 canonical，但非 canonical 拼写仍走 `LIMIT 1` 不确定命中。本轮治理严格缩小了该问题面（消除了 5.3/4.6 双重路由），剩余面属全局别名卫生，建议独立治理轮。
3. **本机部署滞后**：运行容器仍为 2082（cf7256f6），main 已至 2084 线（含 migration 694 self-heal）。下次 deploy-local 会跨越两个版本；migration 694 在 request_logs 侧，与本次数据态无关。
4. **request_logs.canonical_model 历史数据**保留 junk 名（按工具设计，仅分析用途不影响路由）。
5. 治理备份在本机 /tmp（重启即失），如需长期留存请转移到持久存储。

## 相关记忆

`provider-model-drawer-verification`（治理落地详情）、`routing-local-provider-gotchas`（reset 端点配方）、`iab-browser-automation-quirks`（evaluate 已可用等新结论）、`llm-gateway-local-db`（auth username 字段）已于本会话更新。
