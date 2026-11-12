# 2026-11-13 24h 审计与完善轮 · 第七轮 handoff

## 结论与范围

本轮＝第六轮（`HANDOFF-48h-audit-round6-20261112.md`，终点 `ff1f17a6c`）之后的
**合并 → 分支/stash 清扫 → 在途 WIP 审计落库 → 重点域逐条审计（顺延项兑现）→ 修复 → 文档 → 推送**一轮。
完整主档：`docs/audit/2026-11-13-脱敏恢复域逐条审计轮-主档.md`；通用式：`LESSONS.md` **§704**。
工作区 `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`（主 checkout）。
本轮生产代码改动＝**media generations 两端点接入 SmartSaniGuard**（脱敏/恢复域 P2 收口）。

## 改动与根因

1. ★ **合并**：origin/main 与 github/main 同一 SHA `ff1f17a6c`（7 笔 docs/版本戳）⇒ FF 合入。
   工作树 15 个未提交文件三分：4 个过期版本戳（丢弃）、db-changelog 154-2967 回填行（stash→合并→pop）、
   10 个真实 WIP ⇒ 审计后落库。
2. ★ **分支清扫**：本地/三远端**零未合并分支**；cursor clone 内容 0 领先但 IDE 占用 ⇒ 不动。
   **stash：drop 10**（逐项 diff 验证被 HEAD 超越/自陈产物）；余 73 个（2 个近期存疑 + 63 个 8~10 月
   preserve 族）**登记另案**，不属本轮。
3. ★★ **在途 WIP 落库两笔**：
   - `21abf0fec` 租户新增用户前端（8 语种×20 键 + TenantDetailView + 4 钉）——后端
     `admin/users.go:358` 契约逐条核过（闭集/限租户/复杂度/禁用租户拒）⇒ 对齐；登记不改：
     super_admin 建号落查看中租户的 tenant_id（既有设计，收窄归 owner）。
   - `8bd57f957` deploy-211.sh ssh 双通道（--via-mesh + 自动探测）——修掉「后置重复实现无条件
     DD_FORCE_MESH=0 洗掉前置 flag」真缺陷；dry-run 双臂 rc=0。
4. ★★★★ **重点域 P2 修复（本轮最重要交付，§704）**：`/v1/images|/videos/generations` 完全绕过
   SmartSaniGuard（prompt 明文出上游 + 响应占位符无从还原），既有量具全绿（反射矩阵只管 IR 参数轴）。
   修＝envelope media 分支 + `RestoreMediaBody`（Context 活字典 + 通用树行走，还原/打码/fail-closed）
   + 两 handler `SetSanitizeGuard` 成对装配（**还原失败撤回输入中间件——半装配比不装配更糟**）
   + installSmartSaniGuard 扩参接线。钉测 8 条 + 变异验证（禁分支 ⇒ 两包红 ⇒ 门有牙）。
5. ★ **注入检测域（F-41）维持登记不改**：detector/checker nil（main.go:7415）、user_content 无人写、
   armor=observe-only+MockJudge 恒 0 分 + 0.7 硬编码；测试直接构造 ⇒ 接线漂移不可见；修复方案在
   全面审计v3/03 移交清单，归 owner 排期。新增登记：ARMOR_JUDGE_* 未入配置文档。
6. ★ 近 24h 提交批判审计：92 笔已被既往各轮覆盖；本轮补审 7 笔 docs + d6828e463（主题收口，成立）。
   Slice 2 / F-58 / F-44 三处落地经包级测试独立复验绿。

## 测试命令与结果

- `go build ./...` / `go vet ./...` rc=0；gofmt 触碰文件干净。
- `go test ./security/sanitize/ -count=1` ok 10.1s；`go test ./domains/streaming/ -count=1` ok 86.2s；
  `go test ./cmd/gateway/ -run 'TestOutputSensitiveGuard|...'` ok；变异臂 ×1（禁 media 分支）两包红。
- `go test ./internal/reasoningdialect/ ./internal/tooldialect/ ./internal/ir/` ok；
  admin `TestHotLegGateWindowClampLogic` ok；executors `TestStickyCacheClose` ok。
- ursmcheck `TestCjkVar|TestPipefailScope|TestPipefailRace` ok 16.8s；节号唯一性门 ok 2.8s（含 §704）。
- 前端：`vue-tsc` rc=0；`vitest src/views/TenantDetailView.test.ts` 7/7；8 语种键集对拍=1；
  i18n 死键读数=0（=基线）/ dead-key-guard PASS。
- Go 全量 `go test ./... -count=1`：**382 包 ok**；唯一 FAIL＝scripts/ursmcheck 607s 撞 go test 默认
  10min 单包帽（超时伪影；帽后真红＝死键棘轮 0→1，由 40b5d5a31 修复——见主档 §六诚实留痕）。
- ursmcheck 单包重跑（-timeout=2400s，树=d8e4cc17e）：全包撞掏空门探针容量域（TestTsvHollowout
  33m38s ⇒ panic；**0 个 --- FAIL**——字母序在前的全部测试含死键读方已 PASS）＝第六轮定性复现；
  **门判据面单跑收口**：TestStructuredRegistryHollowout+AGENTS 棘轮+节号门 standalone ok 129.4s；
  死键读数直证=0（=基线）。
- 读数对应提交：Go 面 d8e4cc17e 同内容；文档提交后快门（节号/棘轮/死键）复核绿。

## 遗留风险（按优先级）

1. **注入检测 F-41 桥接**（nil 接线 + user_content 空转 + armor 零判别力）——方案在案，owner 排期。
2. **monitor-summary 冷态 >30s**（245 实测 500 未消失）——异步物化/分页，第五轮登记。
3. **「端点×挂载点」对拍门缺位**——§704 通用式尚未机器化；下一个骨架端点会重演同款缺口。
4. responses 入站占位符端到端钉测（批5 后）；ASR multipart prompt 面。
5. 上游错误体入 `_request_logs` 无脱敏（取证面，owner 裁决）；ARMOR_JUDGE_* 文档补注。
6. 63 个老 stash 逐条清算（另案）；cursor clone 删除（IDE 退出后）。
7. Token 桶方案 v2 修正清单 D-①~⑧ 待方案方采纳；magpie H2 前置缺陷未修；pii.address 误伤 owner 裁决
   （均第六轮顺延，非本轮新增）。

## 下一轮提示词

```
/goal
请对此24小时的修正任务进行审计与完善：
- 拉取主分支最新代码合并到本地，编译测试修正发现的问题。
- 检查一天内1小时前所有没有合并的不活跃的子分支…（同前轮目标句）
- 对所有的git中的提交及本地修改进行总结，逐个批判审计，修正发现的问题，同步更新文档，提交推送。
- 特别注意：① 注入检测 F-41 桥接（全面审计v3/03 移交清单方案：detector/checker *sql.DB 桥接 +
  user_content 写面 + ARMOR_JUDGE_* 文档补注）——上两轮均顺延，若 owner 未排期则本轮实施；
  ② 把 §704「数据面端点清单×守卫挂载点」落成机器对拍门（端点清单来源=mux 注册面扫描）；
  ③ monitor-summary 预物化/分页方案取证（245 冷态 >30s 实测复现路径）；
  ④ responses 入站占位符端到端钉测补齐；⑤ 上轮顺延项（Token桶 D-①~⑧ / magpie H2 / pii.address）
  逐条核对是否有主。
完成后输出：结论/根因、改动文件与关键行为、测试命令与结果、遗留风险、更新本 handoff、下一轮提示词。
```
