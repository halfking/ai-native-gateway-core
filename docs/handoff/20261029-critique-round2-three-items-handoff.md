# Handoff · 批判式审计第二轮遗留三项收口轮（2026-10-29）

> 交接对象：下一轮接手 llm-gateway-go-3 的会话。
> 输入：docs/audit/2026-10-29-批判式审计第二轮遗留三项收口轮.md
> 通用式：LESSONS.md §531（复原）+ §559（本轮新增）

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
| `LESSONS.md` | §531 复原（合并时丢失）+ §559 新增（四条通用式） |
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
3. **ursmcheck 的 5 条红在合并上游后只剩 1 条**：post-surface 台账 / probe mirror
   留痕 / design-spacing 棘轮三条**已被上游自己修好**（⇒ 印证「不接手」正确）；
   icon-size 仍是环境型 flaky（根因已取证 = 浏览器驱动 fetch failed）。
4. **icon-size 读方把「环境失败」与「判据失败」折叠在一起**（上游既有门缺陷）：
   掏空门在负载环境下会随机误判「没牙」。修法：读方把 Chrome/CDP 连不上归一到
   独立信号，或掏空门把 wrong-red 与「读方环境失败」分成两类。归上游。

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

- ★★ **让号三次**（合并前核过号、合并后又核过号、push 前还要再核一次）：
  ① 合并前 fetch：远端最高 §555 ⇒ 我方新号**初取 §556**；
  ② 合并后 fetch：§556 已被 r1048 轮占用 ⇒ 改取 §557；
  ③ **push 前第三次 fetch**：§557 已被 nvidia 直通事故轮占用、§558 亦被占
  ⇒ 按「后到者取下一个空闲号」再让 ⇒ **§559**。
  ⇒ ★ 若只在合并前核一次号，必撞号；⇒ **让号必须复核到 push 前那一次**。
  ⇒ §531 远端始终空闲（并行会话的 §535 取号记录写着「本地最高 §531」⇒ 双方
  各自让号）⇒ 复原段留在 §531，无冲突。
- 合并方式：`git merge origin/main`（禁 stash/rebase）；三次合并**均无冲突**
  （远端新增 `scripts/audit/post-surface-ledger-refresh.py` ⇒ 实证上游确实在刷
  post-surface 台账 ⇒ 印证「不接手」判断正确）。
