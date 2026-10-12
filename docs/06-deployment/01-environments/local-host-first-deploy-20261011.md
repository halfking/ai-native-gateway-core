# 本地 host-first 部署方案（deploy-local.sh，2026-10-11）

> **定位：** `scripts/deploy-local.sh` 的资源发现与依赖自举方案——主机优先发现
> PostgreSQL/Redis（缓存与数据分置），主机与 docker 都没有时自动源码安装
> Redis + PostgreSQL 17 + Citus columnar（pgcolumnar），以及
> `llm.itestu.cn` 域名经 NetBird 指向本机实例的接入方案。
> **事实快照：** 2026-10-11，本机（Apple M6 mac mini，macOS 27，docker 缺席）。

## 1. 目标与结论

| 目标 | 结论 | 证据 |
|---|---|---|
| 从主机到 docker 发现 PG/Redis，主机优先 | 已实现，四层发现链 | `tests/deploy_host_first_contract_test.sh` 11 项断言全绿（verify.sh 门禁） |
| 缓存与数据分置 | Redis=缓存/会话，PG=数据；host 实例被认领后 DSN/ADDR 自动落位 | 本机部署日志 `pg-discover: host → 127.0.0.1:54318` / `redis-discover: host → 127.0.0.1:6379` |
| 都没有时自动安装 redis + pg17+citus(columnar) | 已实现（源码构建，含 pgcolumnar 列存 AM 激活） | `DL_AUTOINSTALL_TEST=1 tests/deploy_host_autoinstall_test.sh` 端到端实跑（真实编译 PG17.11 + pgcolumnar v1.0-alpha4 + Redis 7.4.2） |
| llm.itestu.cn → 本机网关 | NetBird DNS zone + A 记录已建 | `dig llm.itestu.cn @100.106.255.254` → 100.106.169.63 |
| deploy-local.sh 全链路部署验证 | 迁移通道修通后端到端跑通 | 见 §6 验证矩阵 |

## 2. 资源发现链（host-first）

`dl_detect_resources`（scripts/deploy-local-lib.sh）按固定顺序认领，先到先得：

```
Tier 1  caller 显式配置（LLM_GATEWAY_DATABASE_URL / LLM_GATEWAY_REDIS_ADDR）
Tier 2  主机原生服务（本机回环健康检查，被认领即注入 PATH）
Tier 3  docker 容器（仅当 host 槽位仍为空时才可认领，不得覆写 host）
Tier 4  自动源码安装（host 与 docker 都没有时，ensure_resources 内联触发）
```

健康检查语义（「端口在听」≠「可用」）：

- **PG**：`psql -d postgres SELECT 1`（探维护库而非应用库——应用库还没建的
  健康集群照样认领）→ 退 `pg_isready` → 退 TCP 握手（advisory，fail-closed
  交给部署后续的 `dl_wait_pg_isready`）。
- **Redis**：`redis-cli PING → PONG`。
- 主机端口候选：`LLM_GATEWAY_PG_PORT`/`PGPORT` → 5432 → 54318（本机
  kaixuan pg18 实例）。

主机认领成功后 `dl_export_host_bin_path` 把
`$KAIXUAN_ROOT/{pg18,redis,deps}/bin` 前置进 PATH——部署体后续的
`psql_query` / `dl_wait_pg_isready` / migrate 辅助全部用裸命令名，没有这步
host 模式会在「psql is required」死掉（这正是 2026-10-11 修掉的缺口）。

## 3. 缓存与数据分置

| 角色 | 落位（本机实测） | 说明 |
|---|---|---|
| 缓存/会话 | host Redis `127.0.0.1:6379`（`~/kaixuan/redis`） | `LLM_GATEWAY_REDIS_ADDR` 由 Tier 2 认领时导出 |
| 数据 | host PG `127.0.0.1:54318`（`~/kaixuan/pg18`，PG18.6 + pgcolumnar） | `LLM_GATEWAY_DATABASE_URL` 由 Tier 2 认领时物化（`postgresql://user@127.0.0.1:54318/llm_gateway?sslmode=disable`） |

应用库 `llm_gateway` 位于 kaixuan pg18 主实例；列存可用（`pgcolumnar`
1.0-alpha4，`columnar` table AM 已注册）。

## 4. 自动安装路径（Tier 4）

`install_host_services`（scripts/deploy-local.sh）——主机与 docker 都没有时：

| 组件 | 版本钉 | 安装位置 | 备注 |
|---|---|---|---|
| Redis | 7.4.2（`DL_REDIS_VERSION`） | `$KAIXUAN_ROOT/redis` | 源码构建，`MALLOC=libc`，daemonize，AOF+RDB |
| PostgreSQL | 17（最新小版本自动从 ftp.postgresql.org 解析，`DL_HOST_PG_VERSION` 可给全版本） | `$KAIXUAN_ROOT/pg`，datadir `$KAIXUAN_ROOT/postgres/data` | `configure --without-icu` 自动回退（无 ICU 头的源码机），`install-world-bin`（带 PGXS） |
| Citus columnar | pgcolumnar v1.0-alpha4（`DL_PGCOLUMNAR_VERSION`，commandprompt/pgcolumnar，citus columnar AM 的独立发行） | 安装进同一 pg 前缀 | PGXS 构建；`shared_preload_libraries='pg_stat_statements,pgcolumnar'`；应用库 `CREATE EXTENSION pgcolumnar` 并校验 `pg_am.amname='columnar'` |

约束与坑（都有代码注释 + 测试锁定）：

- pgcolumnar 必须在**首次 start 之前**装好——preload 引用缺失库会让
  postmaster 直接拒绝启动。
- 构建日志目录必须先建（redis 腿曾因日志重定向先于 mkdir 必挂）。
- macOS 新 SDK（.tbd 带 arm64e.x1 target）与旧链接器不兼容：`dl_resolve_sdkroot`
  逐个探测 SDK 可链接性，取第一个能用的。
- 退出开关：`DL_HOST_INSTALL=0` 跳过自动安装（CI 断言 docker 路径用）。

## 5. NetBird 域名接入（llm.itestu.cn）

控制面：`https://netbird.itestu.cn`（自托管，252）。管理工具：
`~/workspace/myvpn/scripts/netbird-config/{nb-login.py,nb-config.py}`。

| 项 | 值 |
|---|---|
| DNS zone | `llm-itestu-zone`（domain `llm.itestu.cn`，All 组分发） |
| A 记录 | `llm.itestu.cn → 100.106.169.63`（本机 peer `nb-macmini-29` 的 mesh IP），TTL 300 |
| 解析验证 | `dig llm.itestu.cn @100.106.255.254` 与 macOS `dscacheutil` 均返回 100.106.169.63 |
| mesh 入站 | 网关 `LLM_GATEWAY_LISTEN=:8782` 绑全接口；macOS 应用防火墙关闭；NetBird Default 策略（all/all）放行；LAN IP（192.168.31.29:8781）实测 200 佐证非回环入站可用 |

> 本机自连 mesh IP（curl/ping 100.106.169.63）在 userspace 栈下不通属预期
> 怪癖，不代表远端入站状态。**远端 peer 验证命令**（任一在线 peer 上执行）：
> `curl http://llm.itestu.cn:8782/healthz`。**2026-10-11 上午已经 252 实测通过**
>（见 §6），当初「无在线可登录 peer」的留待项作废。

> **HTTPS 终结**（`https://llm.itestu.cn`，浏览器侧）：远端 peer 走 mesh 入站
> 用 HTTP 即可；浏览器场景需 HTTPS 终结，独立方案见
> [local-https-termination-20261011.md](./local-https-termination-20261011.md)。

### 5.1 域名的两条入口与 252 侧链路（2026-10-11 排障沉淀）

`llm.itestu.cn` 有**两条并存入口**，故障定位时先分清走的是哪条：

| 入口 | 解析 | 链路 | 状态（2026-10-11） |
|---|---|---|---|
| mesh 内 | NetBird zone `llm-itestu-zone` → `100.106.169.63` | peer → 网关 `:8782`（HTTP）/ mac Caddy `:8443`（自签 HTTPS） | ✅ |
| 公网 | 公网 DNS（hichina）`llm.itestu.cn → 115.29.212.252` | 252 nginx stream `:443`（SNI `ssl_preread` 透传）→ `:9443`（letsencrypt TLS 终结，证书公网校验通过）→ upstream `100.106.169.63:8782`（走 wt0 mesh）→ 网关 | ✅ |

**踩坑一（已修）：252 上 mihomo fake-ip 劫持 mesh 域名。** 252 跑着 mihomo
（tun + `dns-hijack: any:53` + fake-ip），发往任何 DNS 的查询都被劫持发
fake-IP；`DOMAIN-SUFFIX,itestu.cn,DIRECT` 规则救不了它——mihomo 自己再解析
`llm.itestu.cn` 走公网 DNS 拿到 `115.29.212.252`（252 自己），回环死锁，
表现为 252 上按域名访问网关超时而按 IP 一切正常。**修法**（`/etc/mihomo/config.yaml`，
备份 `config.yaml.bak-llm-mesh-dns-*`，API 热重载 `PUT 127.0.0.1:9090/configs?force=true`）：

```yaml
dns:
  fake-ip-filter:
  - +.llm.itestu.cn                      # 只豁免 mesh 域名；ai./netbird. 等兄弟域保持 fake-ip 行为
  nameserver-policy:
    +.llm.itestu.cn: 100.106.190.139     # 指向 252 netbird 客户端自身的解析器（zone 权威应答）
```

> **nameserver-policy 不可省**：只加 fake-ip-filter 时 mihomo 会退回公网解析
> → 解到 252 自己，照死。**策略必须收窄到 `+.llm.itestu.cn`**：放宽到
> `+.itestu.cn` 会把兄弟域的解析也交给 netbird 转发链路，而 netbird 的上游
> 转发同样被 `any:53` 劫持（实测 `nb-macmini-29.itestu.cn` 解出 fake-IP），
> 反而弄坏现在工作正常的兄弟 vhost。

**踩坑二（已修）：252 nginx upstream 硬编码单一端口。** 蓝绿部署 active 端口
会在 8781/8782 间漂移（本机 2026-10-11 凌晨实测漂过一次，vhost 备份
`bak-port8781-fix-*` 即为手修痕迹）。已给 upstream 加 backup 兜底
（备份 `llm.itestu.cn.conf.bak-mesh-upstream-*`）：

```nginx
upstream llm_gateway_upstream {
    server 100.106.169.63:8782 max_fails=2 fail_timeout=10s;
    server 100.106.169.63:8781 max_fails=2 fail_timeout=10s backup;   # 漂移兜底
    keepalive 32;
}
```

**netbird 客户端（252）的正确形态（2026-10-11 下午核实并安装，更正本节早前误诊）。**
252 的 netbird 客户端**不是宿主机原生服务**，而是 podman 容器 `nb-252-host`
（镜像 `netbirdio/netbird:0.74.6`，与管理面同版本；host 网络，wt0/100.106.190.139
由它创建；状态盘 bind-mount 在宿主 `/opt/netbird/openpocket/nb-252-host-state/`，
peer 身份在其中的 `default.json`）。宿主机上没有 `/usr/local/bin/netbird` 只是
因为二进制从来就在镜像里——早前「exe 指向 (deleted)、无法重启」是跨 mount
namespace 误读，**作废**。现已补齐管理面：

- 宿主 CLI：从镜像提取同版本二进制装到 `/usr/local/bin/netbird`（0.74.6）；
- 管理封装：`netbird-252 status [-d]`（= `podman exec nb-252-host netbird …`，
  守护进程 socket 在容器内，宿主 CLI 直连不了）；
- 重启程序：`podman restart nb-252-host`——2026-10-11 实测身份/IP/连接全保留
  （IP 仍 100.106.190.139，Management/Signal Connected，Relays 5/5），wt0 闪断
  数秒后 mesh 域名 8782/8443 与公网 443 全部自愈，无需改 mihomo/nginx。
- 留档 setup key：`/opt/netbird/openpocket/setup-key*`（root only）。

**代理订阅更新（2026-10-11 下午，按 mac 本地配置对齐）。** 252 mihomo 原用
efanyun 订阅的**陈旧内联节点快照**（机场已整批轮换服务端，114 节点全死，
google/github 全挂）。已改为与 mac（network-switch）相同的 `proxy-providers`
方案：provider `efanyun`（同订阅 URL，`proxy: DIRECT` 直拉，interval 6h，种子
文件取自 mac 缓存 `~/Library/Application Support/network-switch/core/providers/efanyun.yaml`）；
组 rewiring：`Auto`=url-test over provider、`Entry-Proxy/Auto-US/LLM-Traffic/
Grok-Traffic/x-traffic` 收敛为纯组引用、`Cursor-Direct`=fallback(provider,
filter `(?i)US|美国`)。实测 101/101 节点存活（最佳 32ms），google/github 200，
llm 三链路与兄弟域名 fake-ip 行为零回归。手术前的完整备份在
`/etc/mihomo/backups/config.yaml.bak-pre-efanyun-*`。

> **教训（YAML 文本手术）**：该配置里组列表项首键不统一（多数 `- name:`，
> 但 `Auto`/`Cursor-Direct` 首键是 `- interval:`）——按 `^- name:` 切块会把
> 后者整块并入前组，替换时连带删除；必须按 `^- `（列 0 列表项）切块、组名
> 同时匹配 `- name:` 头与缩进 `name:` 键。本轮两个中间态事故（组段头丢失、
> rules 尾巴被砍）均在本地复现定位后修正，线上未受影响（备份恢复）。

### 5.2 白页事故（2026-10-11 下午，2538→2540）

`https://llm.itestu.cn/?login=1` 白页，根因**两层**：

1. **PC 前端构建被静默跳过**：2538 部署时 checkout 里 `web/node_modules`
   缺失，`build_frontend` 走「warn + 保留现有 web/dist」分支，而现有
   `web/dist` 根本不存在 → 发布包 `web/` 只staging了一个 version.json。
   网关进程 cwd=checkout 根，`defaultStaticDir()` 启动时探测
   `web/dist`（缺）→ 回落 `web/`（**源码目录**）→ 把未构建的 Vite 模板
   （`__LLMGW_DEPLOY_SEQ__` 占位符、`/src/main.ts`）当 SPA 首页发出 →
   浏览器无可执行 JS，白页。**修法**：`corepack pnpm install --frozen-lockfile`
   后重新 deploy（构建产物按请求读盘+重启重探测，2539 起恢复）。
   ⚠️ 部署前置条件：`web/` 与 `web-mobile/` 都要先装好依赖；deploy-local
   对「node_modules 缺失→staging 空 web/」目前只 warn 不拒绝（deploy-245
   是 refuse 语义），建议后续补齐 refuse 门。
2. **SPA 深链被全局鉴权截胡（紧邻缺陷，已修码）**：控制台 SPA 从
   `/admin/*` 旧路由迁到顶层路由（`/dashboard`、`/login`、`/routing-v2/*`…）
   后，`middleware/auth_mw.go` 的 BypassRule 名单没跟着更新——任何深链/
   刷新（无 API key）在路由前 401 `missing_key` 裸 JSON，SPA 回退永远
   走不到。**修法**（r2540）：`AuthMiddleware` 增加 SPA 导航放行——
   `Accept: text/html` 的浏览器导航、且不在 `/v1`、`/mock`、`/plugins`
   前缀内 → 放行给 mux（静态处理器仅服务 web/dist 白名单扩展 + index
   回退，无新增暴露面；`/api/*`、`/admin/*` 本就 bypass 由 wrapAdmin
   自守卫）。回归测试三件套 + 变异验证（middleware/auth_mw_test.go）。

浏览器侧验收（IAB，127.0.0.1:8782）：登录 admin 成功、控制台外壳完整
渲染（#2540）、深链 `/dashboard` 整页加载正常。首次启动激活向导会持续
把路由弹回 `/bootstrap`——这是产品自身的激活门禁（License 决策属用户侧），
非缺陷。另有一条非阻断提示「Stop-write switch is not explicitly
configured」（数据状态无法确认新鲜度），待管理员在设置里显式配置。

## 6. 验证矩阵

| 层 | 命令 | 结果（2026-10-11） |
|---|---|---|
| 契约（零依赖） | `bash tests/deploy_host_first_contract_test.sh`（verify.sh 已接） | 11 断言全绿 |
| 自动安装 E2E（opt-in，真实构建） | `DL_AUTOINSTALL_TEST=1 bash tests/deploy_host_autoinstall_test.sh` | redis PING / PG17.x / pgcolumnar 扩展 / columnar AM / 列存 DDL+DML 1000 行全过 |
| 迁移通道 | `bash scripts/apply-db-revision-sequence.sh`（host pg18） | rc=0 全量收敛（本轮补登 11 件前置迁移 + 680b 视图族重建 + 738/740/815/816/817/824 动态形态兼容） |
| 全链路部署 | `bash scripts/deploy-local.sh deploy`（LLM_GATEWAY_ROOT=~/kaixuan/llm-gateway-go-v2） | **rc=0**：release `2.5.8.2522`，cutover 8782 active；`/healthz` ready:true、`/readyz` database+redis 双 connected（latency µs 级）、admin 登录冒烟通过（凭据扫描 WARN=no_credentials_to_scan，非阻断）、记录 `VERIFY_DATABASE=host VERIFY_REDIS=host` |
| LAN 入站 | `curl http://192.168.31.29:8782/healthz` | 200（版本一致） |
| DNS | `dig llm.itestu.cn @100.106.255.254` | 100.106.169.63 |
| 远端 peer 回连（mesh） | `curl http://llm.itestu.cn:8782/healthz`（252 上执行，2026-10-11 上午） | **200**（修复 mihomo fake-ip 劫持后）；`https://llm.itestu.cn:8443`（mac Caddy 自签，-k）**200** |
| 公网入口 | `curl --resolve llm.itestu.cn:443:115.29.212.252 https://llm.itestu.cn/healthz`（2026-10-11 上午） | **200，证书公网校验通过（免 -k）**；链路 252 stream 透传 → 9443 终结 → mesh 8782 |
| netbird 客户端可恢复性 | `podman restart nb-252-host` 后复查三链路（2026-10-11 下午） | **身份/IP 保留**（100.106.190.139），wt0 闪断数秒后 mesh 域名 8782/8443 与公网 443 全部自愈 200 |
| 代理出口（252 mihomo） | `curl -m 15 -o /dev/null -w %{http_code} https://www.google.com`（2026-10-11 下午） | 改 efanyun provider 后 **200**（此前 114 死节点全 000）；Auto 组测速 101/101 存活 |

### 本轮迁移通道修复清单（host-first 库从「Go ensure 链从未启动」状态收敛）

登记的前置迁移（全部幂等，登记位次保证链尾为最新函数体）：
532、392、631、407、536、540、510、513、516、542（及 491 为前一轮已登）。

结构性修复：

- **680b**（新增）：canonical 月视图三层包装链按活动 catalog 重建（hot∩parent
  交集，排除后追加列），解决「610 已扩展早退分支把陈旧列集冻结在视图层」的
  宿疾——734 起整族视图重建的前置条件。
- **738/740/815/816/817/824**：动态交集形态兼容（lateral 追加逐列探测剔除、
  740 的 bottom/mid 绝对列数改结构断言、E'' 替换串不得用 \1 回引——那是八进制
  转义不是 backreference）。

## 7. 运维速查

```bash
# 部署（在 llm-gateway-go 仓）
PATH="$HOME/sdk/go/bin:$PATH" \
GOPROXY=https://goproxy.cn,direct GOMODCACHE="$HOME/sdk/gomodcache" GOCACHE="$HOME/sdk/gocache" \
LLM_GATEWAY_ROOT="$HOME/kaixuan/llm-gateway-go-v2" \
bash scripts/deploy-local.sh deploy          # active 端口 8782

bash scripts/deploy-local.sh status          # 状态
bash scripts/deploy-local.sh logs            # 跟日志
bash scripts/deploy-local.sh stop            # 停 8781/8782/8783
curl -fsS http://127.0.0.1:8782/healthz      # 健康
curl -fsS http://127.0.0.1:8782/readyz       # DB+Redis 双 ping 严格门
```

本机部署根：`~/kaixuan/llm-gateway-go-v2`（bin/run/logs/attachments）；
密钥在仓库 `.env.local`（gitignored）；共享服务在 `~/kaixuan`（pg18/redis）。
