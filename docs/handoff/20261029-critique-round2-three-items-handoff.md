# Handoff · 批判式审计第二轮遗留三项收口轮（2026-10-29）

> 交接对象：下一轮接手 llm-gateway-go-3 的会话。
> 输入：docs/audit/2026-10-29-批判式审计第二轮遗留三项收口轮.md
> 通用式：LESSONS.md §531（复原）+ §557（本轮新增）

## 0. 本轮做了什么（一句话）

把《批判式审计第二轮》§6 的三项遗留全部收口：① alternativesSQL 的 $5 模态
过滤块纳入锁步（并经自我批判审计加固两次：域外闭合门 + 分母锚事实源）；
② 部署引擎三道身份闸在隔离环境跑通真实 --via 流程（21/21）；③ ursmcheck 5 条红
的上游处置逐条复核（2 条上游已修、3 条上游在飞 ⇒ 不接手）。

## 1. 关键产物

| 文件 | 作用 |
|---|---|
| `domains/streaming/model_alternatives_modality_lockstep_test.go` | ②↔③ 锁步门（3 主测试 + 2 detect 域闭合测试 + 5 变异臂） |
| `provider/modality_accepts_lockstep_test.go`（改） | ①↔③ 门：登记侧补 `embedding`（修「声明 > 事实」）+ 加派生域断言 |
| `tests/testutil/modality_value_domain.go` | 登记模态值域的**唯一**派生实现（读迁移 451 的 CHECK 约束） |
| `tests/deploy_via_identity_gates_iso_test.sh` | 三道身份闸的隔离 --via 集成门（21 断言 + 2 变异臂 + 4 还原/隔离臂） |
| `domains/streaming/model_alternatives.go`（改） | $5 注释块加「第二份 SQL 抄本」指针 + 闭世界结构差异登记 |
| `LESSONS.md` | §531 复原（合并时丢失）+ §557 新增（四条通用式） |
| `docs/audit/2026-10-29-批判式审计第二轮遗留三项收口轮.md` | 本轮审计档（含自伤记录） |

## 2. 三条判据的口径（下一轮要改判据时必读）

- **三份模态表达**：① `provider.candidateQuerySQLBase` 的 $3 臂（路由器候选集）
  / ② `domains/streaming/model_alternatives.go` 的 alternativesSQL $5 过滤块
  （零候选建议列表）/ ③ `provider.ModalityAccepts`（诊断 Go 判据）。
  ①↔③ 由 provider 门锁；②↔③ 由 streaming 门锁 ⇒ 三角闭合。
- **②是闭世界、①是开世界**：②无兜底精确臂 ⇒ 未知请求值 ⇒ 建议列表为空；
  ①有兜底臂 ⇒ 未知请求值精确匹配。已知值域上等价；域外语义相反。
- **登记侧值域 = DB CHECK 派生**（text/vision/audio/video/multimodal/embedding
  ∪ 空）：由 `tests/testutil.CanonicalModalityValueDomain` 从
  `sql/migrations/startup/451_*.sql` 派生（取最后一份 ADD CONSTRAINT 定义）。

## 3. 下一轮可以直接领的活（都不在本轮范围内）

1. **五份表达的统一锁步**（收口轮 §8 登记的更大工程）：两份 SQL 抄本仍是独立
   文本；本轮只做到「改一份忘另一份会当场红」，没做到「只有一份」。
2. **`ModalityAccepts` 域外语义与闭世界的对齐**：请求值 embedding 的分歧仍在
   登记状态（诊断入口不产该值 ⇒ 不可达）。若诊断入口扩到产 "embedding"，
   必须先统一三份表达再扩域。
3. **ursmcheck 剩余 3 条红**：等上游两线（台账快照刷新 / P1-D probe_service）
   落地后再评估；当前代修会与上游冲突。
4. **`design-spacing-scale-decisions.json` 读方在基线就红**（live-topology 8 处
   离阶间距）：上游登记为「别人在飞的 UI 决策」，等上游落定。
5. **`scripts/ursmcheck` hollowout 的 icon-size 子测试 flaky**：5 次运行红过 1 次
   （失败详情未存档）⇒ 只能归类 flaky，原因未取证。

## 4. 纪律提醒（本轮踩过的坑，别重复）

- **变异还原不许用 `git checkout --`**：它还原 HEAD ⇒ 会连带还原同文件里自己
  的未提交改动（本轮真发生过）。用 cp 备份/还原，还原后比 md5。
- **`$var` 紧跟全角括号**会被并进变量名（UTF-8 locale）⇒ 用 `${var}`。
- **共享测试替身放 `tests/testutil/`**（仓内既有约定），别在两个包各写一份。
- **派生的输入路径**：provider 在一级子目录、streaming 在两级 ⇒ 派生函数自己
  向上找仓根，别让调用方各算。
- **go 读数必须串行**：并发跑 go 命令会抢坏 build 缓存，报「could not import
  … no such file」，看着像编译错误，根因在缓存。

## 5. 合并与推送记录

- 合并前 fetch：origin/main = 2d66c3ac3（远端 LESSONS 已到 §555 ⇒ 我方新号取
  §557；§531 远端空闲 ⇒ 复原段留在 §531，与并行会话 §535 取号记录相容）。
- 合并方式：`git merge origin/main`（禁 stash/rebase）；LESSONS.md 冲突按
  「两侧都保留、远端在前 + 我方段落在尾」解决。
