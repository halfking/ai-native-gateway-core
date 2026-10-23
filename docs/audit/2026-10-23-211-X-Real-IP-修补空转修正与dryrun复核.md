# 211 X-Real-IP 修补空转修正 + 部署后复验轮收口（2026-10-23）

> 承接 `2026-10-20-211-bluegreen-docker-engine.md`（211 蓝绿 + 引擎主机无关化）
> 与 `2026-10-20-部署后复验轮`（245/154/211 三站部署后复验，产出 F1/F2/F3 三个发现）。
> 本轮**只做修正 + 门禁 + dry-run**，**不做任何部署**（属主指令：不要部署，只做 dryrun）。

## 一、本轮范围与最终形态

| 项 | 上一轮（F1） | 本轮结论 |
|---|---|---|
| F1 用户画像详情端点 500 | 已改代码（`d72489377`） | ★ **已在生产生效**（只读探针实测 245/154 均 200 + `degraded:true`），本轮**无需部署** |
| F2 211 引擎 X-Real-IP 修补空转 | 仅现象记录 | ★ **已修 + 配门 + 变异验证**（`90814ce7e`） |
| F3 245/154 重端点抖动 | 仅现象记录 | 本轮**未处理**（属主未定优先级） |

## 二、F2：211 的 X-Real-IP 修补**从来没生效过**，且每次都打一条**假**告警

### 2.1 真实拓扑（211）

```
211 nginx
  ├─ ns-gateway-211.conf   listen 100.106.67.198:8000  server_name llm.itestu.cn
  │                        proxy_set_header X-Real-IP $ns_real_ip;   ← mesh 腿
  └─ （另一个文件）通用 TLS 终结器  listen 443 ssl  server_name _   ← 不含该域
```

- mesh 腿（`:8000`）：上跳 252 进来，252 用 PROXY protocol 注入真实客户端 IP
  ⇒ 这里**必须**保留 `$ns_real_ip`（**不能**改）。
- 443 腿：通用终结器，`server_name` **不含**该域 ⇒ 引擎「按域 443 块」的匹配
  **永远命中不了**。

### 2.2 三个真缺陷

1. ★ **只在自己的 conf 里找 443 块** ⇒ 真实结论是「**本机不承担该域的 443**」，
   却报成 `noop-no-block` + WARN「**块形态变了?**」⇒ **每次部署都打的假告警**。
2. ★ **域名写死 `llm.itestu.cn`**（权威是 `DD_DOMAIN`）⇒ 换域名会静默改错对象；
   且与引擎文件头自称「不含任何具体主机名」自相矛盾。
3. ★ **`nginx -t` 失败时不还原文件** ⇒ 磁盘上留着**没验证过**的配置，
   之后任何一次 nginx reload 都会加载它（**潜伏半切换态**）。
   同仓审计 `2026-10-20-211-bluegreen-docker-engine.md` §4.1 已定过正确形态
   （备份改前内容 → 改文件 → `nginx -t` → 失败则**还原**），此处一直没落地。

### 2.3 修法

- 判定逻辑**挪出引擎** → `scripts/ops/nginx-llm-realip-patch.py`
  （独立 CLI、**机器可读结论分类** `RESULT=`、注释/字符串感知的掩码解析）。
- 引擎只做「调用 + 按分类处置」，**不再靠退出码做分支**
  （原实现正是在那里踩过 `if ! cmd; then 成功 else 失败` 的**反转坑**）。
- 结论**三分**：
  | 分类 | 含义 | 引擎处置 |
  |---|---|---|
  | `patched` / `already` / `would-patch` | 该域的 443 腿在本机 | 改 / 不改 / 预演 |
  | `skip-domain-not-on-443-block` | **本机不承担该域的 443** | **信息**（log，不是 warn） |
  | `skip-shared-leg` | 该 443 块是**共享终结器** | **不自动改**（会波及别的域） |
  | `noop-unknown-shape` | X-Real-IP 是**第三种取值** | **告警**（这才是该 warn 的场合） |
- 另修两处：★ 备份**每次覆盖**写「改前」内容（旧实现只在不存在时写 ⇒ 还原会拿到
  更早的版本）；★ **共享终结器不自动改**。

## 三、门禁与变异证据（全部零外部依赖）

| 门 | 位置 | 读数 |
|---|---|---|
| X-Real-IP **行为**门（夹具驱动 + **引擎级端到端演练**，用替身 nginx 验 `nginx -t` 失败会还原） | `tests/nginx_llm_realip_patch_test.sh` | **34 通过 / 0 失败** |
| 211 契约门（**从「字符串存在性」升级为「接线 + 不变量」**） | `tests/deploy_211_contract_test.sh` | **129 通过 / 0 失败** |
| teardown 顺序门（回归） | `tests/deploy_211_teardown_order_test.sh` | **54 通过 / 0 失败** |
| 变异台（**不改工作区**，复制到临时目录再变异） | `scripts/mut-nginx-realip-patch.sh` | **7 被抓住 / 0 没抓住 / 0 作废** |
| 降级门变异（补 CONTROL 后） | `~/kaixuan/lgw-verify-20261020/mutate-user-profile-degrade.sh` | **3 被抓住 / 0 没抓住 / 0 作废** |

★ **原契约门是恒绿门**：它只查「引擎源码里有没有 `X-Real-IP` / `$remote_addr` 字样」
⇒ 把整段逻辑删成空壳照样绿 ⇒ 而 F2 的根因恰恰是「这段逻辑从来没生效过」。
⇒ 本轮把它改成钉**接线**（引擎怎么用补丁器）+ **不变量**
（不写死域名 / 跨文件搜 / 两种「没改」结论要分开 / `nginx -t` 失败要还原）。

### 3.1 变异台的 CONTROL（★ 本轮的一条真教训）

★ 变异台**第一件事**必须跑未变异基线（CONTROL）：本轮实测本地 PG 容器
`m7-acc-pg-20261019` **停了 28 小时** ⇒ DSN 口令为空 ⇒ 三条变异**全部「被抓住」**
⇒ ★ **那三条红全红在量具上**（连接失败），不是红在被测行为上。
⇒ 「3/3 全被抓住」**在没有基线背书时不是证据**。已给两台变异台都补上 CONTROL。

## 四、dry-run 验证读数（**未做任何部署 / 切流 / 写生产库**）

```
$ bash scripts/deploy-211.sh --dry-run
{"target":"211","slot_a_port":"8781","slot_b_port":"8783","active_slot":null,"candidate_slot":null}
$ bash scripts/deploy-245.sh --dry-run
{"target":"245","active_port":"8781","candidate_port":"8782","active_port_kind":"contract_nominal"}
$ bash scripts/deploy-154.sh --dry-run
{"target":"154","active_port":"8781","candidate_port":"8782","active_port_kind":"contract_nominal"}
$ bash scripts/deploy-seamless.sh status 245|154      # 只读
```

★ **顺带修正的口径**：245/154 的 dry-run JSON 里 `active_port` 是**契约标称值**，
**不是「此刻跑在哪个端口」**。实测 245（2026-10-23）：nginx upstream =
`127.0.0.1:8782`、活跃单元 `llmgo-245-canary@8782`、而契约标称 `active=8781`
⇒ 照标称值探活会**探错端口**。⇒ 已在两个 JSON 里**只加不改**地补
`"active_port_kind":"contract_nominal"`（部署时以目标机实测为准，
`deploy-seamless.sh` 用 `DETECTED_ACTIVE_PORT`）。

## 五、线上**只读**复核（不发任何写请求）

| 机 | `/api/admin/session-analytics/users/{owner}` | `degraded` | 备注 |
|---|---|---|---|
| 245 | **200** | `true`（reason 非空 32 字） | 19 个键齐全 ⇒ 结构体形状未漂移 |
| 154 | **200** | `true`（reason 非空 32 字） | 同上 |
| 211 | — | — | ★ **ssh 不通**（`192.168.31.211:22` timeout），未能复核 |

⇒ ★ **F1 已在生产生效**：245/154 的发布版（`2815-bd0a27f7` / `2817-a55a4571`）
**都包含** `d72489377`（`git merge-base --is-ancestor` 核实）⇒ 是**其它会话的
重部署**把它带上线的 ⇒ 本轮**不需要**为 F1 部署。

## 六、诚实边界（未做 / 未验证）

1. ★ **211 完全未复核**：ssh 自 2026-10-23 起不可达 ⇒ ① 211 的 nginx 真实拓扑
   只能沿用 2026-10-20 的观测；② 211 的该端点现在返回什么**未验证**
   （上一轮记为「211 现在返回 200」，但那批证据已被 `/tmp` 清理冲掉）；
   ③ F2 的修复**只在夹具上验证过**，**未在 211 实机跑过**。
2. ★ **迁移 358 仍未应用到生产库**（本轮没碰）：它的回填源是已退役的 v1
   `request_logs` ⇒ 生产上必 42P01 整事务回滚。本轮做的是**代码层降级**
   （页面不报错 + 自报家门），**数据层缺口仍在**（页面显示的是空载荷）。
3. ★ **F3（245/154 重端点 8–15s 抖动 200↔500）未处理**。
4. **管理包 8 个真库门在本机恒红**（裸 PG 无业务 schema）⇒ 用 pristine
   worktree 对照确认是**既有失败**，与本轮无关。

## 七、提交与推送

- `90814ce7e`（8 个文件）⇒ 已推 `origin/main`，推送后与
  `git ls-remote origin refs/heads/main` **对拍一致**。
- 只含本轮自己的文件；**并发会话的在途改动（`web/src/*`、`middleware/origin_mw*`、
  `internal/probekind/`、`AGENTS.md` 等）未纳入、未改动**。
