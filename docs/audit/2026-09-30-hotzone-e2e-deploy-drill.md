# hotzone E2E 部署级演练(local 全栈,2026-09-30)

- 对应挂账: docs/audit/2026-09-30-hotzone-p3p4-critical-audit.md §五.2 / §六(部署演练是热区唯一实质遗留)
- 验收依据: docs/storage/2026-09-24-hotzone-dual-mode-plan.md §6 验收清单 + 同日 P3P4 审计的 F4 对账口径
- 演练环境: local 全栈(active 8782 + llm-gateway-pg + Redis 16379/DB5 + mock upstream 19080-82)
- 方法论: 每项验收以「命令输出/日志行/指标 JSON/文件清单」落证,不接受"窗口无报错"式声明;
  对照实验(③④)与正常路径使用同一二进制、同一流量脚本,只切换目标变量。

---

## 一、结论

P3/P4/P5 的部署级半边全部实证通过:**①** `data/hotzone/` 在 full+热区开启下随真实流量出现,
requests/ 三件套镜像可 gunzip 且与 `request_logs_bodies_hot` 内容一致(F4 豁免在生产数据上复现),
cache/ 有 L1.5 文件且「Set 写入」与「L3 回源回填」两条路径都观察到;**②** `/metrics/storage` 三键
(hotzone_enabled / mirror.by_mode.full.total / l1_5_by_mode.full.hits)实读非零;**③**
`LLM_GATEWAY_HOTZONE_ENABLED=false` 重建容器后走历史装配、目录零增长、行为与 main 一致;**④**
PG 停机期间镜像持续写入(errors=0)、恢复后无结构性损坏。方案 §6 第 2/3/5/6 条的部署级半边就此闭合。

同时**新发现两个部署层缺口**(管线 env 白名单不含 STORAGE_MODE/HOTZONE_*,.env.local 一行命令式
写法会令部署中止)与三个如实记录的观察项(F3 同路径双粒度覆盖写、PG 硬停窗口 chat 请求遥测条目丢失、
mirror.hotzone_on 恒 false),见 §三。

## 二、部署与流量编排(可复现命令链)

### 2.1 二进制与部署

| 项 | 值 |
|---|---|
| 演练前 active | `2.5.9-vapeur3-2344`(git_sha=work-vapeur3,今日并行会话部署,已含 P3-P5 代码) |
| 部署入口 | `__DEV_HOME__/workspace/official-deploy/services/llm-gateway-go`(部署真入口克隆) |
| 克隆 HEAD | `d0f333687`(`git pull --rebase --autostash` 后,含并行会话的 80a9a87ab docs 提交) |
| f44d1e045 | `git merge-base --is-ancestor f44d1e045 d0f333687` → 是 |
| 部署结果 | `HEALTH_TIMEOUT=300 bash scripts/deploy-local.sh deploy --no-frontend` → `VERIFY_PASS=1`,release `2.5.8.2345`,version `2.5.8-d0f33368-20260930-2345`,build_seq 2345 |
| 蓝绿 | 候选 8781 探针通过 → 切回 8782;凭据解密冒烟 providers=587 creds=7 failed=0 |

首次部署尝试在 env 写入步中止(见 §三 D2);`pull --rebase` 首次因两个并行会话遗留的未暂存
测试文件被拒,按纪律用 `--autostash` 完成,autostash 正确回放(两文件仍为 M 状态,stash 数不变)。

### 2.2 热区激活(关键前置:STORAGE_MODE 必须显式)

`initStorageMode` dispatcher(storage_mode_init.go:178)在 `NormalizeMode()` 为空(未设
`LLM_GATEWAY_STORAGE_MODE`)时走 default 分支=历史装配、热区不激活。演练前那颗**已含 P3-P5
代码**的 vapeur3 二进制长期 `hotzone_enabled=false`,根因即此——不是代码缺陷,是部署缺口(§三 D1)。

激活方式:按 deploy-local.sh:933 的原样 docker run 参数重建 active 容器,追加
`-e LLM_GATEWAY_STORAGE_MODE=full`。装配日志四连:

```
INFO storage hotzone trimmer 已装配 mode=full dir=./data/hotzone retention_hours=7 max_size_gb=1 trim_interval=30m
INFO storage full 模式热区已装配（hotZoneOnly） hotzone_dir=./data/hotzone cache_dir=data/hotzone/cache
     session_bodies_dir=data/hotzone/session_bodies l1_5_ttl=7h0m0s sqlite_factory=false redis_env_untouched=true
INFO storage hotzone: telemetry request body mirror wired (fire-and-forget, fail-open)
INFO storage hotzone: session v2 body mirror wired (fire-and-forget, fail-open)
```

`ApplyHotZoneDefaults` 生效:Dir=./data/hotzone、RetentionHours=7、MaxSizeGB=1(§6 第 2 条的
默认值半边)。

### 2.3 真实流量

- mock upstream:`scripts/mocks/llm-mock-upstream/server-v2.py` × 3(19080/19081/19082,healthy);
  凭据 70-72 + 绑定 3001244-46 + API key 46(`sk-e2e-test-*`)为本地唯一可解密路由链
  (`credential_keys` 仅此三行,其余 585 个 provider 均为 252 schema 拷贝的不可解密凭据)。
- 流量脚本 `/tmp/hotzone-e2e-drill/send_traffic.py`:每轮 3 会话(hz-drill-sess-1/2/3,
  `X-Session-Id` 复用)× 2 请求(stream + plain),标记 `hz-drill` 便于 DB 对账。
- 各轮结果:r2(热区开,首轮正式)6/6 200;r3(同一容器 docker restart 后,同会话)6/6;
  r5(kill-switch)6/6;r7(PG 停机前基线)6/6;r9(PG 恢复后)6/6。r1/r4/r8 的 503 见 §三 O1。

## 三、发现表(部署层新知,批判式)

| # | 类型 | 发现 | 证据与处置 |
|---|---|---|---|
| D1 | **部署缺口** | deploy-local.sh 的 `dl_write_env` 是固定白名单,不含 `LLM_GATEWAY_STORAGE_MODE` 与任何 `LLM_GATEWAY_HOTZONE_*`——**走管线部署的 full 环境无法激活热区**,必须在容器创建时手工注入 env。本演练即以「原样 docker run 参数 + 追加 -e」重建容器完成;该缺口同样影响 245(若其 env 注入链同构,待 owner 确认) | 演练前 vapeur3 二进制(含 P3-P5)运行 9+ 小时 hotzone_enabled=false 即本缺口的实证;待办:dl_write_env 增透传或 deployment-guide 增手工注入说明 |
| D2 | **部署坑** | official-deploy 克隆 `.env.local:37` 为 `export LLM_GATEWAY_ADMIN_API_KEY="$(openssl rand -base64 32)"`——逐字解析器(python dl_load_project_env)不执行命令替换,把含空格的命令文本当值,`dl_emit_env_line` 拒写 → **deploy 中止并清理候选**(`error: LLM_GATEWAY_ADMIN_API_KEY contains whitespace`)。mock-system-test 记忆里"部署需从 run env 注入 admin creds"即此坑的历史痕迹 | workaround:部署前 `export LLM_GATEWAY_ADMIN_API_KEY=$(grep '^LLM_GATEWAY_ADMIN_API_KEY=' ~/kaixuan/llm-gateway-go/run/llm-gateway-local-8782.env \| cut -d= -f2-)`(caller env 优先);根治应把该行改为字面量 |
| O1 | 环境观察 | 本地 mock 路由存在「容器启动后首个解析窗口 candidates_count=0」瞬态(自愈):r1(启动后 ~2min)0/6 503,~2min 后同模型 200;r4(启动后 ~30s)0/6,10s 后 `candidates_resolved candidates_count=7`;r6 同现。与热区开关无关(两侧均有),与 PG 无关(PG up 时也现)。疑似 URSM/候选装配预热窗口,建议另行专项 | 网关日志 `candidates_resolved ... candidates_count=0` → 数十秒后 `count=7` 原样在案 |
| O2 | **F3 口径修订** | 同一 `(tenant,requestID,direction)` 文件路径存在**两个粒度不同源的写方**:telemetry 侧镜像整请求信封(如 probe-direct 文件 = `{"model":...,"messages":[...]}`),v2 会话写侧镜像 turn 粒度消息数组(`[{"role":"user",...}]`),后写覆盖先写。P3P4 审计 F3 的「gzip 覆盖、内容一致、幂等无害」**不准确**:内容不一致(粒度不同),幂等性依赖"最后写者获胜"且两源同为有效载荷 | 实测:b3e8ca6d(plain chat)镜像文件内容 = turn 数组,而其 PG request_body = 整信封;probe-direct 文件 = 信封。对账脚本须按文件内容判源,不能假设单一粒度 |
| O3 | 观察项 | telemetry 侧镜像在本次窗口**从不产出 hex-id 的 .out 文件**(40 个 .out 全部为 `final_full:gw_*`),而 PG `outbound_body` 有值——streaming 与 plain 皆然。出向证据由 v2 final_full 镜像承载,telemetry 侧 out 方向疑似在 entry.OutboundBody 装配前就已投递(mirror 在 persistRequestLog 顶部,先于 bodies upsert)。方向正确性无影响(PG 有、v2 侧有),但「三件套」的 telemetry 侧 out 半边是空的,记录待查 | `ls requests/… \| grep -c '\.out\.'` = 40,全部 final_full 前缀 |
| O4 | 观察项 | `mirror.hotzone_on` 在热区全开时仍为 false:`SetMirrorHotZoneOn` 是 setter-without-caller(P5 审计 F2 同款预存缺口的镜像版),属 H4 开关未接线 | monitoring/storage_metrics.go:201 无生产调用方;指标实读 hotzone_on=false |
| O5 | 观察项(PG 演练) | PG 硬停窗口内**完成**的 chat 请求(021da488,流式,PG 中途停机后完成 200):既无镜像文件、恢复后也无 `request_logs_hot`/`bodies_hot` 行——遥测条目丢失而非回放(raw-logs jsonl 为 RawDataLogger 审计 sink,不回放 request_logs)。候选归因:probe 重试风暴占满 worker / 策略拦截响应体未采集,未定论。**与热区正交**(镜像通道对 probe 条目全程正常),属既有遥测管线在硬故障下的已知语义,建议挂账 | 时间线:PG stop 14:58:02 → 请求完成 06:58:55Z → 恢复后 0 行;同期 probe 镜像 +20 文件、counter 持涨、errors=0 |

## 四、验收矩阵(逐条带证据)

### ① data/hotzone 三子树 + gunzip 对账(§6 第 2 条部署级)

| 项 | 要求 | 实证 | 判定 |
|---|---|---|---|
| ①-a | 三子树出现 | `cache/` ✓(3 个 session JSON)、`requests/{tenant}/{date}/` ✓(369 文件)、`session_bodies/` **目录未创建**——P3P4 审计 §五.4 已挂账 bodiesStore 无写入方,FileBodiesStore 惰性建目录,装配不改盘 | ✅(带 §五.4 既有挂账的如实偏差记录) |
| ①-b | cache/ 有 L1.5 文件 | 两条路径都观察到:Set 路径(首轮请求后 `gw_hz-drill-sess-*.json`,内容 SessionStateV2,LastTurnNo=2 与两轮请求吻合);回源回填路径(容器重建后 L1/L1.5 全空,r7/r9 流量经 L3 PG 命中回填,三文件再现) | ✅ |
| ①-c | requests/ 三件套镜像 | 统计:.req 214 / .resp 132 / .out 40;三向齐备但 out 全部来自 v2 final_full(O3)。镜像覆盖 chat 请求、probe-direct 流量两类 | ✅(out 半边带 O3 记录) |
| ①-d | gunzip 抽查与 `request_logs_bodies_hot` 对账 | b3e8ca6d…(plain):镜像 `.req` = `[{"role":"user","content":"hz-drill hotz-A1 r2 s1 plain world"}]`、`.resp` = echo 内容(含 mock identity),PG 行 request_body/response_body 为整信封,**语义内容一致**(粒度差见 O2)。0d2bff55…(stream):.req/.resp 同验 | ✅ |
| ①-e | F4 豁免(null/{} 行) | 生产数据复现:PG 行 `probe-direct-c49-mdeepseek-flash-a5-fail-…335` response_body/outbound_body 均 NULL,镜像目录该 requestID **仅** `.req.json.gz`(resp/out 无文件)——mirrorableBody 的 null/{} 跳过按设计工作 | ✅ |

### ② /metrics/storage 三键(§6 第 3 条「可通过指标区分命中层」部署级)

| 项 | 要求 | 实测(时间序) | 判定 |
|---|---|---|---|
| ②-a | hotzone_enabled=true | 装配后 true(基线 vapeur3=false);kill-switch 重建后 false(③) | ✅ |
| ②-b | mirror.by_mode.full.total>0 | 首轮流量后 472、errors=0;重启后新进程从 43 涨至 247(PG 停机窗口仍涨) | ✅ |
| ②-c | l1_5_by_mode.full.hits>0 | 同容器 docker restart(L1 清空、L1.5 文件保留)+ 同会话复用 → **hits=3、hit_rate=0.6**、`hotzone.hit_total_by_mode.full=3`(同源别名一致)。关键认知:L1 内存命中在单进程内遮蔽 L1.5,部署级命中必须跨重启观测 | ✅ |

### ③ `LLM_GATEWAY_HOTZONE_ENABLED=false` 重启对照(§6 第 5 条部署级)

| 项 | 实证 | 判定 |
|---|---|---|
| 装配路径 | 日志:`storage full: hotzone 关闭（config 通道），走历史装配（runtime=nil）`;trimmer/双镜像接线日志全部缺席 | ✅ |
| 目录不再新增 | 同镜像同 env-file 重建(kill-switch 唯一变量)后发 12 个请求(r4+r5),`data/hotzone` 目录不存在、文件数 0(对比热区开:12 请求后即有镜像与 cache 文件) | ✅ |
| 行为与 main 一致 | r5 6/6 200;成功请求照常写 `request_logs_hot`+`request_logs_bodies_hot`(6 行 success);r4 的 503 为 O1 瞬态,且其 no_candidate 失败行也照常落 PG(历史路径对失败请求的落库行为不受热区影响) | ✅ |
| 指标对照 | hotzone_enabled=false、mirror.total=0、l1_5.full.hits=0(流量后仍全零) | ✅ |

### ④ PG 故障演练(§6 第 6 条,方案标"可选",本轮实做)

| 项 | 实证 | 判定 |
|---|---|---|
| 热区持续写入 | PG stop 14:53:17(容器级硬停,非查询失败):镜像文件 94→100→121(窗口内 +20,为持续运行的 probe-direct 流量的 req+resp 对),`mirror.by_mode.full.total` 持涨至 158+,**errors=0**——镜像通道与 PG 的独立性在部署级成立(persistRequestLog 顶部投递,先于任何 PG 往返) | ✅ |
| 路由降级行为 | PG 硬停后新请求快速 503 `routing_connection_error`(预期:候选查询依赖 PG) | ✅(如设计) |
| 恢复后无结构性损坏 | `docker start` 后 7s healthy;public schema 639 表完好;恢复后 r9 6/6 200,镜像与 PG 行重新对齐(5 行 success join 命中,流式行 bodies 有既有 O3 时序);cache/ 三文件经回填再现 | ✅ |
| 数据丢失观察 | 见 O5:停机窗口完成的 chat 请求遥测条目丢失(无镜像无 PG 行)——既有管线语义,挂账非热区缺陷 | ⚠️ 挂账 O5 |

## 五、对方案 §6 验收清单的勾项

| §6 条目 | 单测级(此前) | 部署级(本轮) |
|---|---|---|
| 1. lite 9 项套件全绿 | ✅(P1P2/P3P4 轮) | 不适用(本轮 full) |
| 2. full:`data/hotzone/` 结构与 lite 同构,7h/1GB 默认值生效 | ✅ | ✅ 本轮 ①(装配日志+磁盘实测;session_bodies 无写入方偏差如实记) |
| 3. 读路径顺序实测 + 指标区分命中层 | ✅ | ✅ 本轮 ②(l1_5_by_mode.full.hits=3 跨重启实测;O2/O3 对账口径补全) |
| 4. hotzone_max_size_gb 热重载生效 + trimmer 先删最旧 | ✅(P2 轮单测) | 未做(30m trim 间隔 > 演练时长;单测已钉死,维持) |
| 5. 关闭开关后行为与 main 一致 | ✅(单测对照) | ✅ 本轮 ③ |
| 6. PG 故障演练:热区持续写入,恢复后无结构性损坏 | ✅(P4 轮单测) | ✅ 本轮 ④(镜像独立性部署级实证;O5 挂账) |

**热区方案的部署级验收至此全部闭环**(§6 第 4 条的部署级重放除外,理由如上)。

## 六、遗留与交接

1. **D1(建议尽快)**:`LLM_GATEWAY_STORAGE_MODE`/`LLM_GATEWAY_HOTZONE_*` 进 dl_write_env 白名单,
   否则 245/154 同构部署上热区永远不激活(需 owner 拍板 env 策略)。
2. **D2(一行修复)**:official-deploy 克隆 .env.local:37 的 openssl 命令式写法改字面量。
3. O2/O3:对账脚本(若建)按「文件内容判源 + 镜像可能多于 PG 单向容错 + null/{} 豁免」三规则;
   telemetry 侧 out 半边是否补齐待查。
4. O4:`SetMirrorHotZoneOn` 接线(H4 开关),或从 Snapshot 删键。
5. O5:PG 硬停窗口遥测条目丢失归因(worker 饱和 / 策略拦截),与热区无关,建议另立轮次。
6. 本轮结束后环境终态:active 8782 = main HEAD(d0f33368)二进制 + `LLM_GATEWAY_STORAGE_MODE=full`
   (热区默认开)——注意该 env 为演练手工注入,下次走管线部署会丢失,热区退回不激活态(D1)。

## 七、证据文件清单(/tmp/hotzone-e2e-drill/)

- `baseline-metrics-before.json` / `final-metrics.json`(演练前/后 /metrics/storage 全量)
- `traffic-*.jsonl`(r2/r3/r4/r5/r7/r8/r9 逐请求状态、request_id、延迟;pgdown-single.json)
- `b3e8ca6d….{req,resp}.json`、`ed49957f….req.json`、`021da488…` 缺文件对照、
  `probe-direct-c49-…335` F4 豁免对照(gunzip 前后文件)
- mock 启动日志 `mock-logs/mock-19*.log`
