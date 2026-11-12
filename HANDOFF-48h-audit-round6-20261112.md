# 2026-11-12 48h 审计与完善轮 · 第六轮 handoff

## 结论与范围

本轮＝第五轮（`HANDOFF-48h-audit-round5-20261112.md`，终点 `a5e6a87d5` 一侧 / `f023a0ebc` 另一侧）之后的
**合并收口 → 台账对齐 → 全量验证 → 重点域取证**一轮。完整主档：`docs/audit/2026-11-12-48h审计第六轮-主档.md`。
工作区 `/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`（同仓 worktree 群之一）。
本轮生产代码**零改动**（两条 fix 均为量具面）⇒ **未触发重部署**（三台仍跑第五轮的 `422e2c90e`）。

## 改动与根因

1. ★ **两轮合并**（并行会话在第五轮收尾后又推两批）：
   - `36a83d52c` ← github/main（Slice 2 实施链：reasoning 字段族收敛 `internal/reasoningdialect`
     + tooldialect 架构档 B 行置 ✅ + 窗口批判审计 23 笔）。
   - `f2eea7279` ← origin/main（第五轮续收口链：AGENTS 归档 + §701 让号 §703 扩写版 + 终态读数回填）。
   - LESSONS.md 两处冲突：① §700 撞号（两侧各自「实测最高 §699 ⇒ 连号 §700」，合并必撞）
     ⇒ 按已推一侧不动先例保留两侧 + 豁免表补 `"700": 2` + pinned 34→35；
     ② §701 第五轮续 vs origin §703 让号扩写版 ⇒ 取 origin 版（其内容取代本侧旧 §701）
     + 保留本侧独有的 Slice 2 §700 ⇒ §701×2 消失 ⇒ pinned 35→34（**净零，一加一减**）。
2. ★ **台账重算**（全走官方生成器）：admin-routes 快照因 `81b0c1d39` 改 prompt-optimizer
   绑定面（4 条路由行号 274-290→280-296）⇒ `extract-admin-routes.sh --update-snapshot`
   重算（698 条不变）；节号豁免表同步两轮合并；AGENTS 棘轮被并行会话的归档修复（366,770 < 524,288）。
3. ★ **全量套件终态**：`go test ./... -count=1 -timeout=2400s` ⇒ **EXIT=0 / 383 包 ok / 0 失败**
   （`/tmp/lgw_suite_final.log`）。合跑时 ursmcheck 在 300s 包预算内超时 panic 属负载伪影族
   （并行会话同时在跑套件），静置单跑复绿——§696 纪律再次验证。
4. ★ **重点六域取证（无新增缺陷，四项登记裁决面）**：
   - **IR 转换**：Slice 2 已落地并随本轮合并验收——Arbitrate 单源 + ForVendorFamily 注册表
     + 死权威 `ir.reasoningDisabled` 真删；`go test ./internal/ir/ ./internal/reasoningdialect/` 绿。
   - **路由**：两层路由 + B1 兜底（`router.go:1891`）+ B2 沉底（`executor_dispatch.go:1281-1286`）
     在位；Token 桶方案 v2 修正清单 D-①~D-⑧ 待方案方采纳（硬拒绝无兜底是红线）。
   - **注入检测**：admin 管理面四道钉测族全绿；攻击面 vs 检测面逐条缺口审计仍是下轮首项。
   - **脱敏**：r1038b 收口态保持；`pii.address` 前导「号」误伤（7 天 9 万次）仍待 owner 一句话裁决。
   - **用量**：预算门半窗封顶根修在位；magpie 交接项 H2（hot-ledger 归因记 resolve-top 而非
     serving credential）未修——W4 桶成功率 rollup 的前置。
   - **token 桶/用户桶**：W1+B2+B1+breaker 四源投影与 211 取证一致
     （24h concurrent|429 = 618 行，617 行聚 sensenova tpm/rpm 同文）。
5. ★ ai-native-maintain 仓（控制面，独立仓库）本轮另修一处真缺陷并推送：
   `ebeae33`——`internal/releasebuild/autoplan_disk_test.go` 引用已被 90b84c0（2026-10-02）
   重命名删除的 `diskProbeCandidates` ⇒ 该包 `go vet`/`go test` 构建失败 40 天无人见
   （所有门只在宿主跑、该包测试从未单独跑）。修到新签名 `diskFreeCandidates(dir, filepath.Dir)`；
   `go test ./...` 25 包全绿。

## 验证读数

| 项 | 读数 |
|---|---|
| 全量套件 | EXIT=0 / 383 ok / 0 FAIL（@`2545d34b8`，`/tmp/lgw_suite_final.log`） |
| 关键包 | ir / reasoningdialect / bucket/ratelimit / ursmcheck 节号 4 道 + admin-routes 3 道 + AGENTS 棘轮全绿 |
| 推送 | origin/main = github/main = `ee4e6683a`（含并行会话后续 d6828e463 的合并） |
| 三台部署 | 未触发（应用 sha 仍 `422e2c90e`，本轮零应用代码变化；211 槽 b@8783 / 245 systemd / 154 active@8781） |

## 遗留与下一轮

1. 注入检测 + 脱敏的**逐条缺口审计**（连续两轮登记未扩面）——下轮首项。
2. magpie H1/H2 交接回执（复现路径在 Token 桶第二轮批判审计 §五）。
3. 运营面：211 六个 disabled∧active 凭据 + F1 口径分叉（第五轮 §4.4）。
4. `pii.address` 前导「号」最小修法待 owner 裁决（r1038b §九）。
5. `/api/credentials/monitor-summary` 的 total_requests 两臂 COUNT（245/154 冷态 >30s）
   ——四条路径已列（第五轮主档 §六之三之三），按 ③ 异步物化立项需运营/产品拍板。
6. **部署注意**：三台自第五轮 `422e2c90e` 后未再部署；下轮若有应用面变化，
   按 deploy-211.sh → deploy-245.sh → deploy-154.sh 顺序，冻结树部署（git archive HEAD →
   LLDM_GATEWAY_ROOT + DD_SKIP_WEB_BUILD=1），admin 冒烟用
   `scripts/audit/deploy-smoke-admin.sh`（口令只经 stdin，不进命令行/环境变量/仓库）。

## 资产与记录

- 本轮提交序列：`36a83d52c`（merge github/main）→ `f2eea7279`（merge origin/main）→
  `2545d34b8`（admin-routes 快照重算）→ `183ed5054`（第六轮主档）→ `ee4e6683a`（merge d6828e463）
- ai-native-maintain：`ebeae33`（autoplan_disk_test 修复，origin/main 已推）
- 主档：`docs/audit/2026-11-12-48h审计第六轮-主档.md`
- 套件日志：`/tmp/lgw_suite_final.log`

## 下一轮提示词

```
对 llm-gateway-go 做下一轮 48h 审计与完善（第七轮），按优先级：
1) ★★★ 注入检测与敏感信息脱敏/恢复两域的逐条缺口审计（连续两轮登记未扩面）：
   先跑仓内门（make guards / scripts/run-injection-test.sh），再判「门绿但门测的不是那件事」；
   每条缺口给 file:line + 可复现触发路径；修复带钉测 + 变异验证。
2) magpie 轮 10 两条交接项（H1 dispatch 单次 transient 终止 / H2 hot-ledger 归因错位）：
   先分侧（magpie dispatch vs gateway）再修；H2 是 W4 桶成功率 rollup 的前置。
3) 运营面收口：211 六个 disabled∧active 凭据 + F1 口径分叉（第五轮 §4.4）由运营拍板后
   实施并复测 ASR/TTS 不再 503。
纪律：合并后先跑全量（go test ./... -count=1 -timeout=2400s）；台账一律走官方生成器
（extract-admin-routes.sh --update-snapshot 等）；节号取号前在合并后树上按「未占用」实测；
推送前再 fetch；口令只经 stdin 传给 scripts/audit/deploy-smoke-admin.sh。若本轮有应用代码
变化，按 deploy-211.sh → deploy-245.sh → deploy-154.sh 顺序部署并验证（冻结树 +
LLM_GATEWAY_ROOT + DD_SKIP_WEB_BUILD=1）。
```
