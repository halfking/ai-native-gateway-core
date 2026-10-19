# 211 蓝绿化 + Docker 引擎主机无关化（2026-10-20 第二轮）

> 承接 `2026-10-20-211-502-deploy-order.md`（第一轮：守卫前移 + 可回滚退役）。
> 本轮把 211 从「单容器 + 自己实现的退役」升级成**与 245/252 同款的蓝绿切换**，
> 并把 Docker 部署引擎从 `deploy-211-docker.sh` 泛化/重命名为 `deploy-seamless-docker.sh`
> （与 `deploy-seamless.sh` 同族），使「薄外壳指定主机信息」即可复用于多台 Docker 主机。

## 一、为什么不能再是单容器形态

第一轮的修法是「守卫前移 + 可回滚退役（改名+停机）」，它把
「一次部署失败 = 服务下线」降级成「一次部署失败 = 部署没发生」，
但**单容器形态的结构性上限**还在：旧容器被拆掉之后，**没有任何实例在服务**，
于���这段时间内任何失败（镜像拉不动、就绪探不过、迁移炸了）都还是一次停机。

蓝绿形态天然免疫：旧槽全程在服务，候选槽失败只要丢掉候选即可。
154/245 的 `deploy-seamless.sh` 早就是这个形态，本轮把它移植到 Docker 侧。

## 二、引擎泛化：`deploy-211-docker.sh` → `deploy-seamless-docker.sh`

| 项 | 前 | 后 |
|---|---|---|
| 文件名 | `scripts/deploy-211-docker.sh` | `scripts/deploy-seamless-docker.sh`（`git mv`，保留历史） |
| 主机名 | 脚本内写死 `211`（`D211_*` 374 处） | 引擎内**零**主机名；主机信息由薄外壳用 `DD_*` 注入 |
| 变量前缀 | `D211_` / `d211_` | `DD_` / `dd_` |
| 日志前缀 | `[211]` | `[%s]`（= `$DD_TARGET`） |

### 主机信息注入面（薄外壳 → 引擎）

`DD_TARGET` `DD_HOST` `DD_SSH_TARGET` `DD_STATE_DIR` `DD_REMOTE_ROOT` `DD_NETWORK`
`DD_SLOT_A_PORT` `DD_SLOT_B_PORT` `DD_CONTAINER_PREFIX` `DD_IMAGE` `DD_GW_DATA_ROOT`
`DD_NGINX_CONF` `DD_NGINX_UPSTREAM` `DD_DOMAIN` `DD_PG_*` `DD_REDIS_*`
`DD_BASE_REGISTRY` `DD_GO_BASE_IMAGE` `DD_RUNTIME_BASE_IMAGE`

⇒ **新增一台 Docker 形态的主机 = 写一个薄外壳**（复制 `scripts/deploy-211.sh` 改主机信息），
**不需要复制引擎**。复制引擎 = 两份蓝绿语义各自演化，必然漂移。

### 薄外壳 `scripts/deploy-211.sh`

211 的主机信息（唯一职责）：

```
DD_TARGET=211   DD_SSH_TARGET=211   DD_HOST=192.168.31.211
DD_STATE_DIR=/opt/llm-gateway-go-211      ← 不可改：含 env/gateway.secret
DD_REMOTE_ROOT=/root/llm-gateway-go-211
DD_SLOT_A_PORT=8781  DD_SLOT_B_PORT=8782
DD_CONTAINER_PREFIX=llm-gateway-211
DD_NGINX_CONF=/etc/nginx/conf.d/ns-gateway-211.conf
DD_NGINX_UPSTREAM=ns_llm_gateway_upstream
DD_DOMAIN=llm.itestu.cn
DD_BASE_REGISTRY=registry-211.itestu.cn
```

> ⚠ `DD_STATE_DIR` 路径**不可改**：`env/gateway.secret` 决定 `SECRET_KEY`，
> 而 SECRET_KEY 与库内 `api_keys.key_hash` 的 HMAC 绑定 ⇒ 换路径 = 换密钥 = 全部 key 失效。

## 三、蓝绿状态机（Docker 形态）

```
槽位：a ↔ b，各绑一个固定发布端口（DD_SLOT_A_PORT / DD_SLOT_B_PORT）
容器名：<DD_CONTAINER_PREFIX>-<slot>
活跃槽记在 <data root>/run/active-slot

一次部署：
  ① 前置门（前端产物新鲜度）—— 任何破坏性动作之前，失败即「本次不部署」
  ② 构建/推送新镜像（不动任何在跑的容器）
  ③ 种子化**候选槽专属**目录（web/<slot>/dist、web-mobile/<slot>/dist、version/<slot>.json）
  ④ 起候选容器（候选端口），旧槽继续服务 ⇒ 零停机
  ⑤ 验收候选：readyz(DB+Redis) / version / 网页面(HTML+入口 JS MIME)
  ⑥ 切流：改 nginx upstream 主 server 行 → nginx -t → reload
     → 复验公网版本身份（防「文件已切、流量未切」的半切换态）
  ⑦ 切流成功 ⇒ 停旧槽容器（**停而不删**，人工 `docker start` 可回滚）
  任一步失败 ⇒ abort：删候选槽 + 切回 upstream + 旧槽若已停则拉回
```

### 三个关键设计决定

1. **活跃槽以 nginx upstream 实际端口为事实**，状态文件只是账本。
   两者可能因手工改 nginx 而分叉；以事实为准，否则会把候选槽算成活跃槽 ⇒ 切流方向反了。
2. **种子按槽隔离** ⇒ 回退**不需要**还原种子 ⇒ 不会出现「旧二进制 + 新 SPA」的错配组合。
3. **切流后就地改 upstream 块的主 server 行**（不引入 include 迁移面），减少实机变更面。
   backup 行（`100.106.169.63:8782`，是**另一台机器**）不动。

## 四、本轮实测抓到的真缺陷

### 4.1 `nginx -t` 失败时配置已被改写 ⇒ 潜伏半切换态

原实现：`改文件 → nginx -t → 失败就只 warn 不 reload`。
失败形态：磁盘配置已指向候选端口，nginx 跑的仍是旧配置（流量暂不变），
但**此后任何一次 nginx reload**（人工 / systemd / crontab）都会把这份未验证的配置加载进去。

⇒ 修法：**先备份改前内容 → 改文件 → `nginx -t` → 失败则把文件还原**。
备份必须**每次**都写「改前内容」（旧实现是「只在不存在时写」⇒ 还原会拿到更早的版本）。

判据：`tests/deploy_211_teardown_order_test.sh` 场景⑤（行为层：真 nginx conf + 替身 nginx）。

### 4.2 `version.json` 改写落到了**没被挂载**的路径

槽位隔离改造后，改写仍指向平铺的 `$DD_GW_DATA_ROOT/version.json`（那个文件不再挂载），
且 python 只传 2 个参数却读 `sys.argv[3]` ⇒ IndexError 被 `|| warn` 吞掉
⇒ **失败形态是「静默不改写」**（线上 `/version` 继续显示别人的版本戳）。

⇒ 修法：改写目标改成本槽副本 `version/<slot>.json`，并把 target 作为第 3 个参数传进去。

### 4.3 `--redis-db N` 从未转发到远端

参数循环里 `shift` 完再用 `$*` 转发 ⇒ `$*` 恒为空 ⇒ 外壳文档里写着能传的
`--redis-db 9` 实际从未到达远端引擎。

⇒ 修法：显式收集 `DD_ARGS` 再转发。

### 4.4 `$var` 紧跟全角标点（bash 3.2 会把多字节首字节并进变量名）

`warn "nginx conf 不在场（$_conf），无法切流"` ⇒ 契约门扫描报 1 处 ⇒ 改 `${_conf}`。

### 4.5 逃生舱在 preflight 层是**死的**（真机部署直接撞上）

`dd_preflight_web_dist` 在「逃生舱放行」时返回 **1**，而调用方写的是
`dd_preflight_web_dist ... || exit 1` ⇒ 返回 1 = 失败 ⇒ **带了
`DD_ALLOW_STALE_WEB=1` 也照样退出** ⇒ 逃生舱形同虚设。

实测：2026-10-20 真机部署带着逃生舱仍被「web/dist 陈旧」拦死在**远端侧**。

⇒ 修法：语义改为「**放行 ⇒ 返回 0**」（告警已经 warn 过），
与 `deploy-local.sh` 的 `check_web_dist_freshness`（逃生舱 → warn + return 0）同一族口径。
判据：新增行为层「preflight 的返回码」三例（陈旧无舱 ⇒ die / 陈旧有舱 ⇒ 0 /
缺失但上一轮种子还在 ⇒ 逃生舱也不放行 ⇒ die）。

### 4.6 转发清单里混进了 **repo 相对派生的量**（会让部署直接 die）

`DD_WEB_DIST_SRC` / `DD_MOBILE_DIST_SRC` / `DD_CONFIGS_SRC` 在引擎顶部按
`$DD_REPO_ROOT/<x>` 算出来，而 `DD_REPO_ROOT` **两侧不同**（部署机 = 本机仓库，
远端 = `/root/llm-gateway-go-211`）。转发过去 = 远端拿着 macOS 路径去种子化 ⇒
前端产物「缺失」⇒ 撞「上一轮种子还在」判定 ⇒ **部署直接 die**；
configs 则是静默回落内置脱敏集（`sensitive_patterns.yaml` 没种进去）。

⇒ 修法：这三个量**不进转发清单**，让远端按自己的 `DD_REPO_ROOT` 重算。
判据：新增反向断言「转发清单不含 repo 相对派生的量」。

### 4.7 槽位隔离后「上一轮种子还在」的探测没跟着改

`dd_check_web_dist` 只认平铺路径 `<root>/<label>/dist/index.html`；
蓝绿按槽隔离后种子在 `<root>/<label>/<slot>/dist/index.html` ⇒ 探测恒为「不在」
⇒ 结论从「**硬拒绝**（逃生舱也不放行）」掉成「缺失且无种子（逃生舱可放行）」
⇒ 逃生舱会把**没有前端**的版本发上线（用户看到白页）。

⇒ 修法：两种布局都认（平铺 + 按槽通配）。
判据：新增行为层「缺失但上一轮种子还在（按槽布局）⇒ 硬拒绝」。

### 4.8 蓝绿函数块排在 `--status` 分派**之后** ⇒ `--status` 直接报 command not found

bash 的函数是**运行时**解析：`show_status` 调 `dd_active_slot`，
而 `dd_active_slot` 定义在 `show_status` **之后** ⇒ `--status` 报
`dd_active_slot: command not found`（真机验证第一条命令就撞上）。

⇒ 修法：把整块蓝绿函数前移到 `--status / --probe-only / --stop` 分派之前。
**教训**：新增「被早分派路径调用」的函数时，要确认它定义在**所有**分派之前，
而不只是「在用到它的 deploy 路径之前」。

### 4.9 切流后**立刻**探一次公网 ⇒ 把一次成功的部署判成失败（nginx reload 是异步的）

真机实测：切流日志显示「已 reload」，紧接着的公网 `/version` 复验却读到**旧槽**
的版本戳 ⇒ 判「半切换态」⇒ 触发 abort（abort 本身是对的，但判据太急）。

真因：`nginx -s reload` 只是给 master 发信号，**新 worker 起来之前，旧 worker
仍会用旧 upstream 应答新连接**。配置其实已经改对（`nginx -T` 里是新端口），
只是生效有延迟。

⇒ 修法：复验改成**轮询**（默认 10 次 × 1s，`DD_SWITCH_VERIFY_TRIES` 可调），
拿到新 sha 才算过；始终拿不到才判失败。
⇒ 顺带把复验里的 `printf | grep -q` 换成 bash 原生模式匹配
（`grep -q` 命中即退 ⇒ 上游 printf 收 SIGPIPE ⇒ pipefail 把「明明匹配」判成
「没匹配」—— 本仓已记录的竞态，这里再踩一次）。
判据：新增行为层「reload 窗口内拿到旧版本时，轮询复验最终通过」+
「公网始终是旧版本 ⇒ 判失败（轮询不会放过真半切换态）」。

> ⭐ 这条同时证明了**复验是有用的**：没有它，这次部署会「成功」，
> 而线上其实还在跑旧版本（上游文件已改、流量未切）。



## 五、判据

| 门 | 层次 | 条数 | 覆盖 |
|---|---|---|---|
| `tests/deploy_211_teardown_order_test.sh` | 行为 + 语义 + 结构 | 48 | 守卫在前 / abort 切回 upstream / abort 拉回旧槽 / nginx -t 失败不 reload 且还原配置 / backup 行不动 / 种子按槽隔离 / 切流·验收·停旧槽的顺序 / 失败点走 abort / 旧槽停而不删 / 活跃槽以 upstream 为事实 |
| `tests/deploy_211_contract_test.sh` | 契约 | 118 | 沿用上一轮（Redis 倒排、PG 三维判据、密钥卫生、基础镜像仓、版本戳改写、种子按槽隔离…），并把「引擎不含写死主机名」「转发清单」改成泛化后的口径 |

### 变异验证（10 条，全被抓住）

```
abort 不切回 upstream            → 行为层红（upstream 停在 8782）
nginx -t 失败不还原配置          → 行为层红（配置停在 8782）
切流跳过 nginx -t                → 行为层红（-t 失败仍 reload）
种子改回平铺路径                 → 契约层红
活跃槽不读 upstream 实际端口      → 结构层红
旧槽改成硬删                     → 结构层红
失败点改回裸 die                 → 结构层红
守卫挪到候选槽起来之后           → 结构层红（守卫行号 > 候选行号）
切流挪到网页面验收之前           → 结构层红
停旧槽挪到切流之前               → 结构层红
```

判据纪律：读数必须**区分「真实断言红」与「量具自己炸」** ——
harness 首版把 transform 当普通参数传进 `python3 -`（没人 exec 它），
10 条变异全部「没打中」；这是**量具坏了**，不是被测代码没问题。
修正 harness 后 10/10 抓住。

## 六、真机验证（211，测试环境）

见本文件末节「真机验证记录」。

## 七、遗留 / 需知

- **首次蓝绿迁移**是一次性形态变更：211 上原先是单容器 `llm-gateway-211`（占 8781）。
  首轮蓝绿部署的候选槽是 b（8782），切流成功后把历史单机容器
  `llm-gateway-211` **改名并停止**为 `llm-gateway-211-legacy`（保留可回滚），而不是删掉。
- 245 / 252 是生产，本轮**没有**对其做任何部署动作；引擎泛化对它们无影响
  （它们走 `deploy-seamless.sh`，不是 Docker 引擎）。
- `scripts/deploy-211-docker.sh` **不留**兼容壳（引擎已改名；留壳等于留两份入口语义）。
  引用面已同步：`deploy/deploy.sh` 菜单、两道门禁的 `SCRIPT=`、
  `scripts/sync-gateway-db-to-211.sh` 的提示文案。
