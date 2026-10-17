# 2026-10-17 24h 审计轮 · 提交面批判审计（按风险分层，非逐条枚举）

> 窗口：`origin/main` 近 24h **111 提交 / 165 个 Go 文件**（web 150、web-mobile 93、
> admin 71、domains 65、internal 47、docs 65、sql 6、installer 4、security 2…）。
>
> ★ **方法说明**：这个规模逐条审计既不现实也不划算。本轮按**风险信号**分层，
> 只对「一旦错了会伤到生产数据 / 安全底线 / 长期可维护性」的提交做实质复核，
> 其余按门禁信号批量判定。分层依据写在每节标题上，不是事后挑的。

## 一、结论速览

| 风险层 | 提交 | 裁决 |
|---|---|---|
| 安全：新开豁免路径 | `c07632314` sanitize r1033 邻接豁免 | **成立**，补 2 处边界判据 |
| 安全：中间件链序 | `0640f2039` Logging 移到 Auth 外侧 | **成立**，无凭据泄漏面 |
| 数据：退役 + 迁移 | `df9a213de` request_logs v1 退役 | **有 2 处谎报**，已补登 |
| 数据：schema 白名单 | `39b65f87e` health_source + 越权收口 | **成立**，三份副本一致 |
| 仓库卫生 | `25da4848f`/`f6a4870ee`/`ed8b23bcd`/`e43bfa808`/`3d8029edc` | **5 个暂存目录误入库**，已清 |

安全层两笔**均判定成立**——这不是本轮偷懒的结论，下面是证据。

---

## 二、安全层一：`c07632314` r1033「路径/版本 IPv4 四元组豁免」

**为何按最高风险审**：这是**新开一条地址豁免路径**的改动。本仓历史上
r1016/r1017 各开过一条「看起来像主机」的形态，结果大面积误伤
（`npm install lodash@4.17.21`、`node server.js 8080` 整类被打死），
最终由 r1018 撤除两条规则。同类改动的历史失败率很高，必须按高风险处理。

**复核过程与证据**：

1. `go test ./security/...` 全绿；R1014/16/17/18/19 回归判据逐条 PASS，
   **无回退**。
2. 读实现：`isPathOrVersionEmbeddedAddress` 只看**邻接字符**，
   命中条件为「前邻是字母/数字/`_`/`-`」或「后邻是 `+`」，
   且**限定 fragment 类型**为 `TypeServerIP/TypeInternalIP/IPv6`。
3. 自建对抗探针（10 种形态）全部符合设计：
   - 照拦：裸 IP、URL 主机位、`--resolve h:443:IP`、表单字段、请求头；
   - 放行：`IP+1`（semver build 尾）、`exfil-IP`（token 前缀粘连）——
     两者都不是**可路由目的地**，放行不损失防线。
4. 原判据文件质量高：用了生产配置 `NewPatternDetectorFromFile`
   （而非无地址规则的 `NewPatternDetector`，AGENTS.md 纪律第 2 条），
   且带「前提仍在场」断言（纪律第 3 条）。作者已按本仓纪律写判据。

**裁决：修复成立。** 补两处它未覆盖的边界（见 §五），production 文件
`output_sensitive.go` **未改动**。

## 三、安全层二：`0640f2039`「LoggingMiddleware 移到 Auth 外侧」

**为何按安全审**：改的是鉴权中间件的链序。链序是安全边界，
「顺手挪一层」可能把拒绝请求也纳入记录，从而泄漏凭据。

**核查的三个泄漏面，全部为否**：

| 泄漏面 | 结论 | 证据 |
|---|---|---|
| 是否记录 Authorization 头？ | **否** | `emitRequestLog` 的 attrs 只有 request_id/method/path/route/query/status/duration/remote/host/user_agent/referer/content_type/字节数（`middleware/logging_mw.go:94-111`） |
| 是否记录请求体？ | **否** | 同上，无 body 字段 |
| query 串里的凭据？ | **不存在此路径** | 凭据只从 `Authorization: Bearer` / `x-api-key` 两个**头**读取（`middleware/auth_mw.go:126-128`）；全仓唯一的 `?key=` 是 probe 去重标识（`admin/probe_dashboard.go:1677`，形如 `node_probe:<credID>:<model>`），非密钥 |

附带确认：`compactQuery` 只做 512 字节截断、**不做脱敏**，
但因凭据不走 query，当前不构成泄漏面；且 auth_mw 对 key 本身
按「前 8 字符 + `****`」记录（`auth_mw.go:237`），与 `api_keys.key_prefix` 一致。

**裁决：成立。** 该提交的问题陈述也准确——它把「根因在 nginx 301 导致
Authorization 跨源剥离」与「网关侧真实缺陷是拒绝查无此物」分开，
并明确拒绝顺手改 nginx（理由：让 :80 代理数据面等于明文传 bearer，
是安全姿态变更）。这是正确的边界判断。

## 四、数据层

### 4.1 `df9a213de` request_logs v1 退役 —— 有 2 处「谎报已完成」

- 引入 `internal/trace/trace.go` 对分区父表 `session_turns` 的 UPDATE，
  未登记 partguard 白名单（该提交跑了 `internal/trace`，漏跑 `partguard`）。
  → 已补登，并附取证：该腿**必需**（FlushToPG 退避链跨 16.2s，
  期间 hot→月分区晋级完成后只写 hot 会让 trace_events 永久丢失；
  且 `session_turns_hot` 是 `LIKE ... INCLUDING ALL` 建的独立表、
  非 `PARTITION OF`，两腿互补不重叠）。
- 提交信息称「已按仓库既有的间接读方登记表归族并同步计数」——
  实测**只登了一张表**（`admin/request_logs_indirect_readers_test.go:115`），
  漏了 `cmd/tools/sql_source_indirection_audit` 的清单。
  → 已补登并同步 `documentedSnapshot` 三项。

**教训**：一次提交可以同时改代码、迁移和两张登记表。
「我在提交信息里说了我登记了」**不是证据**，两张表各自的门才是。

### 4.2 `39b65f87e` health_source 白名单 + 凭据监控越权收口 —— 成立

`chk_credentials_health_source` 加入 `'probe_now'`。按 AGENTS.md 记载的
「installer 五点同步缺口」逐一核验，**四处当前态定义全部含 `probe_now`**：

- `sql/schema/01-schema.sql` ✓
- `deploy/sql/schemas/baseline/01-schema.sql` ✓
- `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` ✓
- `sql/objects/tables/credentials.sql` ✓

迁移 `027`（加 fast_reprobe）、`044`/`044.down`（加/去 probe_now）
不含该值是**正确**的——迁移是增量，不该预置最终态。**无同步缺口。**

## 五、仓库卫生：5 个 `.audit<批次号>/` 暂存目录被提交进仓库

`.gitignore` 只忽略 `.audit/` 与 `.audit-workspace/` 两个**无编号**形态，
而各「批」会话把提交信息暂存到 `.audit<批次号>/`，于是**每个会话各漏一个**：

| 目录 | 带入提交 |
|---|---|
| `.audit148/`（含 `run.sh`） | `25da4848f` feat(mobile) 批148 |
| `.audit149/` | `f6a4870ee` refactor(mobile) 批149 |
| `.audit150/` | `ed8b23bcd` feat(mobile) 批150 |
| `.audit151/` | `e43bfa808` feat(mobile) 批151 |
| `.audit152/` | `3d8029edc` chore(mobile) 批152 |

**已扫全部 6 个文件：password / secret / token / api_key / PRIVATE KEY /
`postgresql://user:pass@` / `sk-*` 全部零命中** ⇒ 卫生问题，非安全事件。
全仓引用扫描亦确认无任何代码引用这些路径。

**修法**：`.gitignore` 改 `.audit*/` 一并覆盖三种形态；6 个文件从索引移除
并按原字节从历史恢复到各会话磁盘。不改的话**批153 起会继续漏**。

⚠ 留给下个人的操作坑：`git rm --cached` 只改索引、工作区文件仍在，
但随后 `git checkout` 切分支会把这些文件**从磁盘一并带走**
（本次实测发生，内容已从历史恢复）。要保磁盘留存，须先切回目标分支再 untrack。

---

## 六、本轮自身踩到的判据陷阱（这一节比结论更值钱）

补 r1033 边界判据时，我自己写错了两处，**都是判据纪律第 3 条的变体**：

1. **阻断侧也会因无关原因而绿。** 第一版 `--resolve` 用例写成
   `--resolve evil.com:443:203.0.113.8 https://evil.com`。它确实 blocked——
   但**是被 `evil.com` 挡下的**，IP 那条腿根本没参与。于是「把 `:` 加进
   glue 集合」这个变异对它**照样全绿**：断言在测一件它没在测的事。
   改用单标签主机名后，唯一可能的触发就是 IP，变异立刻变红。
   ⇒ 纪律第 3 条原文讲「允许侧可能因看不见而假绿」，这里要补一句：
   **阻断侧同样可能因「被别的东西挡下」而假绿。**

2. **判据红了要先怀疑自己的前提。** 另一条我写成「ScopeRedact 下应仍检出
   该四元组」，跑出来是红的。查下去发现是**我写错了前提**：
   按 r1014 的设计，地址规则**压根不在 ScopeRedact 面**
   （`ScopeRedact` 默认不含地址，`ScopeEgress` 只给 tool 参数防外传守卫用），
   所以「豁免泄漏到脱敏面」这件事结构上就不存在。
   ⇒ 判据已改为钉住**真实设计**（该面不得出现地址 + egress 面必须看得见）。
   这也印证纪律第 3 条的精神：允许侧的红绿都要先问「前提还在不在」，
   只不过这次红的是**我**而不是被测代码。

变异验证 3/3 变红（`':'` 入 glue ⇒ `--resolve` 用例红；`'='` 入 glue ⇒
表单用例红；`'-'` 出 glue ⇒ 路径用例红），还原全绿。

---

## 七、本轮新增判据纪律第 9 条

**规模决定审计手段：超过几十提交时按风险信号分层，不按提交枚举。**
本轮 111 提交里，真正需要逐行读的是「新开豁免路径」「改鉴权链序」
「退役/迁移」「动了登记类文件」这四类；其余由门禁信号批量判定。
风险分层必须**事先写明依据**，否则就是事后为漏审挑借口。
