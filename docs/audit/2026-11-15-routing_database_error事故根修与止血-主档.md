# 2026-11-15 routing_database_error 事故根修与止血 — 主档

> owner 报障：「请检查并分析 211 上网关的错误：failure upstream · routing_database_erro，请及时修正，然后进行部署与测试验证。」
> 提交：`eea844247`（分类器三形态补齐 + park 总预算收口 + admin 测试签名追平，基于合并 origin/main 的 `bfae35442`）
> 状态：止血完成（00:01:34 切回 b 槽）；正式部署与活体验证见 §五。

## 一、事故时间线（全部 211 实测取证）

| 时间(+08) | 事件 | 证据 |
|---|---|---|
| 11-14 22:54 | `d5eb218a2`（入口并发旋钮）提交 | git log |
| 11-14 23:09 | `8066b57c0` 修复 lifecycle「SQL别名越界」（P0 上线即崩：`mc.released_at` 越出 CTE 作用域 ⇒ 42P01） | git log + 提交自陈 |
| 11-14 23:12 | `39b4d8e5a`（含修复）成为 HEAD；b 槽在其上运行，**6h 零候选错误** | b 槽日志 grep 0 命中 |
| 11-14 23:42:29 | 并行会话按「部署债交付」（7fdef810a 自陈 build_seq 3054）把 **a 槽部署成 d5eb218a**（旧两档、**不含** 23:09 的 P0 修复）并杀 b 槽 | a 槽启动 6 秒后首条 42P01 |
| 23:42:29–23:43:01 | 候选查询 42P01 连败 ⇒ 候选熔断 open | `failed to get candidates: missing FROM-clause entry for table "mc" (SQLSTATE 42P01)` |
| 23:43–00:01 | 熔断 open ⇒ 请求 parked ⇒ 客户端 300s 超时挂断 ⇒ `context canceled` 全部落入 `routing_database_error` 兜底 ⇒ 500 | duration 246–300s 的 http_request + `context canceled` ERROR 对拍 |
| 11-15 00:01:34 | **止血**：拉起 b 槽 + nginx upstream 切 8783（官方 helper nginx-upstream-port.py + nginx -t + reload） | upstream 主行 `server 127.0.0.1:8783` |
| 00:04 起 | 每 100% 成功、0 routing_database_error | request_logs 分钟聚合 |

事故规模：**83 笔失败 / 7 个模型 / 21 分钟**（23:42:29→00:03:41），chat 面近乎全瘫（同窗仅 4 笔成功）。放大器＝重试风暴：客户端（ZCode）300s 超时重试，风暴护栏空 session 窗口计数一度 69 笔/5min。

## 二、根因定案（三层链，一次部署事故暴露两个真实代码缺陷）

### 2.1 触发层：回退式部署（r1052 教训复演，非新代码缺陷）
「部署债交付」选了 `d5eb218a2` 这个**比在役 b 槽旧两档**的树部署，把 23:09 刚修的 P0 覆盖回坏形态。部署前未对拍 origin/main 水位——**AGENTS.md 2026-11-14 多模态轮「部署事故自纠」横幅的原文教训**：部署前必对拍 origin/main 水位；冻结树/合并 HEAD 部署纪律。

### 2.2 归因层：classifyRoutingError 兜底吞噬三个真实形态（本轮修复一）
`domains/streaming/routing_errors.go` 的兜底类 `routing_database_error`（=仪表盘「failure upstream · routing_database_error」的分组键）吞掉了：
- `context canceled`（客户端挂断）——**不是**上游 DB 故障；
- `candidates query circuit open (retry after …)`（保护性熔断）——可重试瞬态，却回 500；
- `missing FROM-clause entry`/`SQLSTATE 42P01`（网关侧 SQL 缺陷）——不是 DB 基础设施故障。

后果：owner 在仪表盘看到的「upstream 数据库错误」实际是「客户端挂断 + 熔断停车」，真根因（SQL 别名）反而要翻容器日志才能看到。

### 2.3 放大器层：park 预算「注释说总额、实现逐次给满」（本轮修复二）
`provider/client.go` parking 循环注释：「parkCap 内循环等待/重试；预算耗尽快速失败」。实现：`remaining := parkCap` **每次迭代重新给满** ⇒ 总等待无上限。冷却翻倍梯子（30s→60s→…→10min cap）爬到 4-5 分钟后，请求被一路 park 到冷却点或客户端超时——**生产实测 300s 挂起、以 context canceled 收场**。若总预算真正生效（默认 10s），客户端在 10 秒内就会拿到可重试的 503，而不是干等 5 分钟。

## 三、修复内容（eea844247）

1. **classifyRoutingError 三形态补齐**（routing_errors.go）：
   - `context canceled`（errors.Is + 字串双判）→ `routing_client_canceled`/499，日志降 WARN（事故中 ERROR 刷屏误导归因）；三调用点（chat handler / messages / responses）改为先分类后记日志（`logCandidatesFailure`）。
   - `circuit open` → `routing_circuit_open`/503。
   - `missing FROM-clause entry`/`SQLSTATE 42P01` → `routing_schema_error`。
   - bare `context deadline exceeded` 补进 connection/timeout 臂（原先也漏进兜底）。
2. **park 总预算收口**（provider/candidates_circuit.go）：park 循环提取为 `awaitCircuitProbe` 方法，`parkDeadline` 收口总预算；0 预算=纯快速失败语义保持。
3. **admin 测试 NewHandler 签名追平**：并行 phase2（4c50dc4e2）改签名未同步 8 个测试文件 ⇒ `go vet ./admin/` 编译红（「推送前必须编译验证」教训现行案例）；统一补 `nil` registry。

## 四、门与验证

| 项 | 结果 |
|---|---|
| TestClassifyRoutingError（16 用例，含 4 个生产逐字形态） | ok |
| TestAwaitCircuitProbe ×4（总预算/客户端挂断透传 ctx.Err()/0 预算/闭合直通） | ok |
| 变异 M1 摘 canceled 臂 | 红（回退到生产原形态 routing_database_error）→还原绿 |
| 变异 M2 摘 circuit open 臂 | 红 →还原绿 |
| 变异 M3 退回逐次给满预算 | 红（**等满 30s ctx 超时**，复刻生产挂起机制）→还原绿（1s 快速失败） |
| 变异 M4 摘 42P01 臂 | 红 →还原绿 |
| provider 全量 / streaming 全量 / admin 全量 | ok / ok 104.6s / ok 121.7s |
| build / vet / gofmt（触碰文件） | 干净 |
| 止血活体（b 槽 39b4d8e5a）：临时 key id=261 → glm-5.3-flash | **200 + 真实回答 PONG** |
| 止血后分钟聚合 | 00:04/00:05 两分钟 32/32 全成功、0 routing_database_error |

## 五、部署与终态验证

- 冻结树：`git archive eea844247` → /tmp/deploy-frozen-211-eea844247；`LLM_GATEWAY_ROOT` 钉冻结树；`DD_BUILD_SHA=eea84424` 显式声明（冻结树无 .git）；bash5 入口 `scripts/deploy-211.sh`。
- 首轮被部署门拒绝＝**本机 docker 兜底 OOM 假红**（`compile: signal: killed`；同一棵树基线跑 streaming ok 154.6s/候选跑被杀，随内存压力漂移；触发器＝PATH go 1.27.3 < go.mod 要求 1.27.4 使原生路径失效——正是 `39b4d8e5a` 修的 DD_TEST_GO_BIN 场景）。二轮带 `DD_TEST_GO_BIN=~/sdk/go/bin/go`（1.27.4 原生+热缓存）通过。
- 终态：蓝绿 `2.5.9-eea844247-20261115-211`（build_seq 3055）槽 a @8781 接管、readyz OK、公网版本身份 OK、旧槽 b 停留可回滚。
- **活体验证（并行会话验收漏掉的那条）**：临时 key（id=261）→ 公网 `/v1/chat/completions` glm-5.3-flash **3/3 全部 200 + 真实回答 PONG**；key 验后 revoked（带 prefix 守卫）。
- **DB 终态**：部署后（00:35 起）`routing_database_error` 增量 **0**；00:36 起 **65/65 全部成功**。

### 5.1 部署后追修：nginx :443 半修缓冲缺口（既有缺陷，验证腿暴露）

公网验收首笔 502 ⇒ nginx error log `upstream sent too big header`：非流式响应的 `X-Gateway-Failover-Notice` 头（2026-09-07 `15fa9c432` 引入的 failover 通道，base64 JSON，随 failover 链增长，本轮实测一次成功请求的头块已达 ~2.7KB）超过默认 `proxy_buffer_size`。**半修形态**：`:8000` mesh 块的 `location /` 历史上已配 `proxy_buffer_size 64k; proxy_buffers 8 64k`（ns-gateway-211.conf:1141-1143），主公网 `:443` 块的 `location /`（:1527-1537）没有——同缺陷只修一半。处置＝宿主侧对齐补 64k（python 单点替换 + 备份 `conf.bak-rde-20261115` + nginx -t + reload），补后 3/3 200。**登记**：该 conf 是宿主侧运维文件不在仓内（部署只改 upstream 行）；failover 头无长度上限（超长链仍可越 64k，理论上应网关侧截断——独立裁决面）。

## 六、遗留与登记

1. **部署纪律**（流程面，归 owner）：「部署债交付」不得选低于在役水位的树；部署前对拍 origin/main（本轮已再次实证）。建议把「在役版本高于候选树 ⇒ 阻断」做进部署引擎门。★ 另：部署验收只跑 readyz+公网入口 200 挡不住本轮这类 P0（readyz 不测候选 SQL）——**验收必须含真实模型请求**（本轮并行会话的验收正是漏了这条才放走坏部署）。
2. **风暴护栏空 session 聚合**（登记观察）：`wrapAnomalyDetector` 对无 session 请求聚到一个 `""` 窗口，事故重试期把无 session 的正常请求（含验证 curl）误拦为 storm；独立裁决面。
3. **X-Gateway-Failover-Notice 无长度上限**：failover 链超长时仍可越 64k（本轮 3 次链 ~2.7KB；理论上应网关侧截断/去重）；nginx conf 为宿主侧文件不在仓内，64k 补丁的持久化依赖宿主备份纪律。
4. **499 语义**：routing_client_canceled 用 nginx 惯例 499 记录；仪表盘如按 status_code 过滤需知晓此非标准码。
5. 历史已登记的 gofmt 基线（provider 三个非触碰文件）不在本轮范围。
