# Handoff: junk canonical 治理后的独立收尾轮 —— review free 白名单 / 别名卫生（含审计返工）/ 694 通道 + 本机部署 2086

**日期**: 2026-09-12（含同日审计返工）
**状态**: ✅ 三项闭环 + 审计发现一处重大缺陷已根修；本机 kx-llm-gateway-local:**2.5.4.2086**（f494d069）@8782
**前置**: 20260912-junk-canonical-remediation（14 行弃用、37/37 可路由）遗留三项

---

## 结论 / 根因

1. **任务① review `free`（2664333）= 免费池伪模型，保留 + 白名单**。证据：openrouter（provider 21）全历史零流量（canonical 2664333 与 provider 21 在 request_logs 均 0 行）；provider 21 discovery 里所有 per-model `:free` 变体各有专属 canonical，唯独裸 `openrouter/free` 被剥前缀种成 junk；0.99×5 同分（glm-5.2:free / inkling:free / minimax-m3:free / laguna-s-2.1:free / lfm-2.5-2.6b:free），重指向任一皆错。落地为工具机制：govern-junk-canonical 新增 `VerdictWhitelisted` + `OperatorWhitelist`（白名单覆盖一切 verdict、永不作为重定向落点，诊断仍透明展示）。
2. **任务② 别名卫生 —— 初版数据修复被审计判定无效，已根修为 resolver 确定性解析**。
   - 初版：371 多目的地活跃拼写 = 369 惰性（241 同名遮蔽 + 128 跨形 variant 拦截）+ 2 真歧义（`deepseek-v4`、`doubao-embedding`），按"bare 家族名 → undated 基准行"定夺做了单事务弃用（5 行）。
   - **审计发现（重大）**：部署 2085 后 33 分钟内 5 行全部被复活（updated_at=03:13:29-31 = 新容器 discovery 首轮）。根因：`NormalizeRouteKey` 剥日期后缀（`-260425`）、`stripWrapperVariants` 再剥 wrapper token（`flash`/`vision`），因此**每个 provider 的 dated raw 都按设计生成 bare 家族别名指向自己的 canonical**（实测 `deepseek/deepseek-v4-flash-260425` → 变体集含 `deepseek-v4-flash` 与 `deepseek-v4`），`EnsureCanonicalAndAliases` 用 `ON CONFLICT DO UPDATE SET status='active'` 无条件复活。discovery 每小时一轮——数据层弃用永远撑不过一个周期。
   - **真缺陷是两个 resolver 的别名查询 `LIMIT 1` 无 ORDER BY（不确定命中）**。根修（f494d0695）：`resolve/resolve.go` 与 `provider/client.go` 的别名相 + raw_fallback 共 4 处加 `ORDER BY length(mc.canonical_name), mc.canonical_name`——最短名 = 最基准/undated 行，与 matcher `betterMatch` 的 tie-break 同一约定。operator 语义（`deepseek-v4`→deepseek-v4-flash(120)、`doubao-embedding`→doubao-embedding-vision(47)）从数据层搬到解析层，**天然持久**；5 条复活的别名行保留 active（无害且不可消除）。
3. **任务③ migration 694 升级通道修复 + 部署**。预检发现 694 与 693 同款缺口（只进仓库文件，db.go 无 ensure、apply 清单止于 693）→ 已加进 `scripts/apply-db-revision-sequence.sh`（46e3bc7e2）并本机入账（双账本 + 函数体含 demote 自愈）。部署 2085（5a877aa9d）后审计轮再部署 **2086**（f494d0695 / bump b89799340）；一次 cutover 因启动耗时 >60s 竞态失败回滚，重跑即成（VERIFY_PASS=1）。
4. **端到端实测（真实请求）**：`POST /v1/chat/completions` model=`deepseek-v4` → 响应 success，`request_logs_hot`: `canonical_model=deepseek-v4-flash`、`canonical_id=120`、provider 34——在别名行被复活（122350 行 active）的情况下由 ORDER BY 决定落点，持久性得证。

## 改动文件与关键行为

| 对象 | 变更 | 提交 |
|---|---|---|
| scripts/govern-junk-canonical/{plan.go,main.go,plan_test.go,README.md} | whitelisted verdict + OperatorWhitelist(free，含全部证据) | e3ff2e45f |
| scripts/apply-db-revision-sequence.sh | 增补 694（693-class 升级通道缺口） | 46e3bc7e2 |
| VERSION/version.json/web/public/version.json | 2085 bump | 5a877aa9d |
| docs/handoff（本文初版） | ebee70a8f + merge win11 R14 docs 886860aa6 | |
| resolve/resolve.go, provider/client.go | 4 处别名解析加确定性 ORDER BY（根修） | f494d0695 |
| scripts/govern-junk-canonical/{plan.go,plan_test.go} | 白名单不变量结构化：whitelisted 行从证据对手目录（minusC）与 apply gate catalog 排除，杜绝"建议/重定向到白名单行"；+ 合成白名单测试直查 gateCatalog | f494d0695（同笔） |
| VERSION 等 | 2086 bump | b89799340 |
| DB | 694 双账本入账 + promote 函数自愈体；task② 的 5 行弃用被 discovery 复活后**保留 active**（无害） | 数据态 |

注：历史中 7f468a888（necessity gate round 18）为并行会话提交，先于 f494d0695 入本机 main，随本轮一并推送。

## 测试命令与结果

```
go build ./...                                                    # ok
go test ./resolve/ ./provider/ ./scripts/govern-junk-canonical/ ./modelname/
                                                                  # 全 ok
set -a; source .env.local; set +a
go run ./scripts/govern-junk-canonical                            # suspects: 1 (0 fixable, 0 review, 0 withheld, 1 whitelisted)
bash scripts/apply-db-revision-sequence.sh                        # 仅 694 真正应用（CREATE FUNCTION），余 already applied
docker exec llm-gateway-pg psql ...                               # 694 双账本 1/1；pg_get_functiondef 含 demote 自愈块
# 实证 SQL：新 ORDER BY 下 deepseek-v4→120 / doubao-embedding→47
docker stop llm-gateway-local-8782 && bash scripts/deploy-local.sh deploy
                                                                  # 首次 cutover readyz 60s 竞态失败回滚；重跑 VERIFY_PASS=1
curl /healthz                                                     # 2.5.4-f494d069-20260911-2086, ready
# 端到端：真实 chat 请求 model=deepseek-v4 → success
# request_logs_hot: canonical_model=deepseek-v4-flash, canonical_id=120, provider 34
# 部署后日志：0 条 42703/42P17/23505，无 panic/fatal
```

## 审计结论（双轴自审）

- **Standards 轴**：Fix A 与 matcher tie-break（短名优先、字典序）同一约定，注释引用依据；4 处同改保证两个 resolver 一致；Fix B 把 README 已声明的不变量（白名单行永不作为重定向目标）结构化，合成白名单测试同时断言建议目标与 gateCatalog。
- **Spec 轴**：任务边界未扩大——未动 GenerateAliasVariants（bare 别名覆盖是有意的客户端兼容面，删除会影响生产拼写习惯）；未动 687/688（无错误信号）；数据层不再反复弃用（与 discovery 设计对抗无意义）。
- **过程轴（本次审计的核心教训）**：数据态变更的"外部复核"必须在**部署完成且 discovery 首轮跑过之后**再做一次——本轮初版复核（部署前）通过、部署 33 分钟后即被系统自身行为推翻。

## 遗留风险

1. **bg/taxonomy_sync.go 与 discovery/alias_sync.go 的 `ON CONFLICT (raw_name)` 无对应唯一索引**——两路径一旦执行必报错（被 warn 吞掉）。属既存缺陷，本轮未处理；若 taxonomy/别名索引功能要启用需先修。
2. 白名单 `free`（4 字符）理论上可在未来某个多目的地拼写集里因"最短名优先"胜出——当前唯一入边是自名别名且 provider 21 零流量，风险理论性；若 OpenRouter 接入变现实应整体重审该行。
3. 687/688 本机未入账为既存状态（无错误信号）；694 体已含 688 全量，promote 函数已是对齐形态。
4. 备份均在 /tmp（`backup_junk_canonical_20260912_0147.sql`、`backup_alias_hygiene_20260912_030557.sql`）——重启即失。
5. request_logs 主表 15 分钟窗口查不到新请求是正常现象（先落 `request_logs_hot`，8h promote），验证时查 hot 表。
6. deploy-local 的 readyz 60s 窗口在冷启动重负载（apihub sync + discovery 首轮 + 587 providers）下可能不够——失败重跑即可，非代码缺陷。

## 下一轮提示词

> 聚焦目标：收尾轮审计返工（f494d0695/b89799340，本机 2086 在跑）后的独立项。三项可选（按优先级）：① 修 bg/taxonomy_sync.go 与 discovery/alias_sync.go 的 `ON CONFLICT (raw_name)` 无唯一索引缺陷（先确认两路径是否有启用方与调用频率，再决定是建 partial unique index 还是改写 SQL，注意 rebuildAliasIndex 语义是 INSERT IGNORE）；② 白名单机制运营化——`free` 的"最短名胜出"理论风险与 OperatorWhitelist 的增补流程文档化（评估是否值得加 -whitelist flag 或 DB 表驱动）；③ 观察一轮 discovery 周期（约 1h）后复核两 bare 拼写的解析仍落 120/47，确认 ORDER BY 在生产形态下稳定。约束：不自动开新审计轮；数据操作先备份、单事务、事务内复核；建议 skills：handoff、session-audit-gate、browser-use:control-browser

## 相关记忆

`provider-model-drawer-verification`（已更新：②持久性修正、2086）、`local-deploy-gotchas`、`routing-local-provider-gotchas`、`llm-gateway-local-db`。
