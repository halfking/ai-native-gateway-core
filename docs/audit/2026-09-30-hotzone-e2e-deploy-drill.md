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
写法会令部署中止)与数个如实记录的观察项(瞬态 no-candidate 预热窗、F3 同路径双粒度覆盖写、
telemetry out 半边的租户分裂呈现、mirror.hotzone_on 恒 false、PG 硬停窗口 PG 侧行未回放)。
**同日复审轮(§八)修正了其中两处首轮误判(O3 撤回、O5 前半句错误,根因是只枚举单租户目录),
并把 D1(透传+部署级验证)、D2(字面量)、O4(接线)三项修复闭环**;详见 §三 与 §八。

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

> 2026-09-30 同日复审轮对本表逐点实勘,O2/O3/O5 有实质修正(原文划线保留于 §八),D1 已修复并部署级验证。声明①(FileBodiesStore 惰性建目录)与声明④(租户派生)均代码/数据实证通过。

| # | 类型 | 发现 | 证据与处置 |
|---|---|---|---|
| D1 | **部署缺口(已修复+部署级验证)** | deploy-local.sh 的 `dl_write_env` 是固定白名单,此前不含 `LLM_GATEWAY_STORAGE_MODE` 与任何 `LLM_GATEWAY_HOTZONE_*`——**走管线部署的 full 环境热区永远不激活**,必须在容器创建时手工注入 env。dispatcher 对空 mode 走 default 分支=历史装配(storage_mode_init.go:186-191,代码实勘)。**245 实锤(只读检查)**:gateway 进程(release 2324-a8a34023,早于全部 hotzone 提交)env 无 STORAGE_MODE/HOTZONE_*,即使升级二进制后不补 env 热区仍不会激活 | 修复:dl_write_env 增 6 键透传(未设写空行,gateway 视为未设置,行为与历史一致);函数级验证(6 键齐发+注入值透传)+ 修复后走管线部署实装验证(§八);演练期间并行会话的一次管线部署(2349)当场复现了本缺口(手工注入的 STORAGE_MODE 丢失,热区回退不激活) |
| D2 | **部署坑(已修)** | official-deploy 克隆 `.env.local:37` 为 `export LLM_GATEWAY_ADMIN_API_KEY="$(openssl rand -base64 32)"`——逐字解析器(python dl_load_project_env)不执行命令替换,把含空格的命令文本当值,`dl_emit_env_line` 拒写 → **deploy 中止并清理候选**(`error: LLM_GATEWAY_ADMIN_API_KEY contains whitespace`)。mock-system-test 记忆里"部署需从 run env 注入 admin creds"即此坑的历史痕迹 | 已修:该行改为 run env 现役字面值(.env.local 未被 git 跟踪,本地修复不入库);修复后部署不再需要 export workaround |
| O1 | 环境观察 | 本地 mock 路由存在「容器启动后首个解析窗口 candidates_count=0」瞬态(自愈):r1(启动后 ~2min)0/6 503,~2min 后同模型 200;r4(启动后 ~30s)0/6,10s 后 `candidates_resolved candidates_count=7`;r6 同现。与热区开关无关(两侧均有),与 PG 无关(PG up 时也现)。疑似 URSM/候选装配预热窗口,建议另行专项 | 网关日志 `candidates_resolved ... candidates_count=0` → 数十秒后 `count=7` 原样在案 |
| O2 | **对账口径(复审轮修正)** | 同一请求的三件套镜像**分裂在两个租户目录、两种粒度**:telemetry 侧(persistRequestLog 顶部)租户取 `ApplicationCode\|\|TenantID`(mirrorRequestBodies,client.go:1211-1217),落 `requests/{application_code}/…`(本演练 API key 46 的 application=ide → requests/ide/),内容=整请求信封;v2 会话写侧取会话租户,落 `requests/default/…`,内容=turn 粒度消息数组,且 out 方向以 `final_full:{session}` 命名。同路径存在两写方先后覆盖(file 内容取决于最后写者)。**对账消费方按单一租户目录浏览会看到缺件** | 实测:r2 的 b3e8ca6d 在 default/ 为 turn 数组;midstream 请求 021da488 在 ide/ 为信封 req+out;对照见 §八 全映射实验 |
| O3 | ~~观察项~~ **复审轮撤回** | 原声明"telemetry 侧从不产出 hex-id 的 .out(40 个 out 全部 final_full)"**是我只枚举了 requests/default/ 单目录的取样错误**——ide/system 租户下存在 74 个 hex .out(信封粒度,telemetry 侧产出)。撤回该条,以 O2 的租户分裂口径替代 | `ls requests/ide|system/*/ \| grep '\.out\.'` 实测 |
| O4 | 观察项(已修复) | `mirror.hotzone_on` 在热区全开时恒 false:`SetMirrorHotZoneOn` 是 setter-without-caller(P5 审计 F2 同款预存缺口的镜像版),属 H4 开关未接线 | 已修:与 hotzone_enabled 同一 defer 出口盖章(`mirrorActive = requestMirror != nil`),三个测试钉死(Assembly true / kill-switch false / REQUEST_MIRROR off 时双开关分立) |
| O5 | ~~观察项~~ **复审轮实质修正** | 原声明"停机窗口完成的 chat 请求既无镜像也无 PG 行(遥测条目丢失)"**前半句错误**:021da488(PG 中途停机、停机窗口内完成)的 telemetry 镜像 **req+out 已落盘**(requests/ide/,信封粒度)——我复查时只搜了 default/ 目录。修正后的事实:**镜像通道对 chat 流量在 PG 硬停期间正常工作(「PG 不可用时镜像仍写入」部署级对 chat 成立,强于 §四 ④ 原稿只由 probe 流量承载的证明);PG 侧 request_logs_hot/bodies_hot 行缺失且恢复 1h+ 后未回放**——「镜像可能多于 PG」的单向容错实测成立,「恢复后补写」不成立(回放缺失根因待查:fallback 队列丢弃/重试上限) | ide/021da488*.req/.out 文件 gunzip 实证;PG `LIKE '%pgdown-midstream%'`=0 行 |

## 四、验收矩阵(逐条带证据)

### ① data/hotzone 三子树 + gunzip 对账(§6 第 2 条部署级)

| 项 | 要求 | 实证 | 判定 |
|---|---|---|---|
| ①-a | 三子树出现 | `cache/` ✓(3 个 session JSON)、`requests/{tenant}/{date}/` ✓(369 文件)、`session_bodies/` **目录未创建**——P3P4 审计 §五.4 已挂账 bodiesStore 无写入方,FileBodiesStore 惰性建目录,装配不改盘 | ✅(带 §五.4 既有挂账的如实偏差记录) |
| ①-b | cache/ 有 L1.5 文件 | 两条路径都观察到:Set 路径(首轮请求后 `gw_hz-drill-sess-*.json`,内容 SessionStateV2,LastTurnNo=2 与两轮请求吻合);回源回填路径(容器重建后 L1/L1.5 全空,r7/r9 流量经 L3 PG 命中回填,三文件再现) | ✅ |
| ①-c | requests/ 三件套镜像 | 热区树当时合计 369 文件(cache 3 + requests 366);分向统计(.req 214 / .resp 132 / .out 40)为另一时刻口径。out 全部 final_full 前缀**仅对 default/ 目录成立**——ide/system 租户有 74 个 hex .out(O3 撤回、O2 租户分裂口径替代)。镜像覆盖 chat 请求、probe-direct 流量两类 | ✅(口径注记见 §八) |
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
| 行为与 main 一致 | r5 6/6 200;成功请求照常写 `request_logs_hot`+`request_logs_bodies_hot`(r5 标记行 11 行中 success=9,客户端 6/6 之外的为网关内部重试产生的同标记行——历史路径对重试的落库行为不受热区影响);r4 的 503 为 O1 瞬态,且其 no_candidate 失败行也照常落 PG(历史路径对失败请求的落库行为不受热区影响) | ✅ |
| 指标对照 | hotzone_enabled=false、mirror.total=0、l1_5.full.hits=0(流量后仍全零) | ✅ |

### ④ PG 故障演练(§6 第 6 条,方案标"可选",本轮实做)

| 项 | 实证 | 判定 |
|---|---|---|
| 热区持续写入 | PG stop 14:53:17(容器级硬停,非查询失败):镜像文件 94→100→121(窗口内 +20,为持续运行的 probe-direct 流量的 req+resp 对),`mirror.by_mode.full.total` 持涨至 158+,**errors=0**。**复审轮升级证据**:停机窗口内完成的 chat 请求 021da488(PG 中途停机、完成后 PG 仍不可达)其 telemetry 镜像 req+out 正常落盘(requests/ide/,信封粒度)——「PG 不可用时镜像仍写入」部署级对 chat 流量成立,非仅 probe | ✅ |
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

## 六、遗留与交接(2026-09-30 复审轮更新)

1. ~~D1~~ **已修复并部署级验证(§八 R-6,build 2353)**。245 下一步升级到含热区的版本后,只需
   在其部署调用 env 中设 `LLM_GATEWAY_STORAGE_MODE=full` 即可激活热区(deployment-guide
   配置矩阵;不设=空行=与历史行为一致,不会强开)。
2. ~~D2~~ **已修**(克隆 .env.local 改字面量;2353 起的管线部署实测不再中止)。
3. O1(启动窗口瞬态 no-candidate):挂账,建议专项(URSM/候选装配预热)。本地观察窗口
   最长可达数分钟,且 mock 进程死亡后 binding 退避需三表恢复法手工拉回。
4. ~~O2:对账脚本(若建)~~ **已建(2026-10-01)**:`scripts/hotzone-request-mirror-reconcile.sh`。
   按本节三规则实现(按租户目录判源 + 镜像可能多于 PG 单向容错 + null/{} 豁免),
   并加两条判据纪律:① 三态 PASS/EMPTY/FAIL——空目录若报 PASS 会让「镜像根本没装配」
   伪装成「对账通过」,门无法区分「没查」与「查过且一致」时结论没有信息量;
   ② 汇总只计真正体检过的文件,与 files_seen 分开,避免「扫到500/体检200」被读成
   「500个都验过了」。已用真实 fixture 验证(损坏→FAIL/空→EMPTY/双写方去重/各类过滤)。
   用法见脚本头注。
5. O4:已修复接线(storage_mode_init.go 同 defer 出口),三测试钉死 + 2353/2354 两个生产
   构建 live 验证 hotzone_on=true。
6. O5:PG 停机窗口 PG 侧行未回放(镜像侧正常)——回放缺失根因(fallback 队列/重试上限)挂账。
7. F3 写放大(P3P4 审计)与 F5 admin ingest 第三落库点(owner 决策)维持挂账。
8. **R-9(新)**:本地 docker 模式热区文件落容器可写层,每次部署=热区重置;如需跨部署保留,
   把 data/ 加入 bind-mount 白名单(新需求待拍板)。
9. 环境终态:active 8782 = 2.5.8.2354 二进制 + 手工重建容器注入 STORAGE_MODE=full(热区开)。
   下一轮管线部署若其 shell 未设该 env,热区将按设计回退不激活(空行语义)。

## 七、证据文件清单(/tmp/hotzone-e2e-drill/)

- `baseline-metrics-before.json` / `final-metrics.json`(演练前/后 /metrics/storage 全量)
- `traffic-*.jsonl`(r2/r3/r4/r5/r7/r8/r9 逐请求状态、request_id、延迟;pgdown-single.json)
- `b3e8ca6d….{req,resp}.json`、`ed49957f….req.json`、`ide-out`/`sys-out`(hex .out 信封实证)、
  `probe-direct-c49-…335` F4 豁免对照(gunzip 前后文件)
- `dl_write_env-test.env` / `dl_write_env-test2.env`(D1 修复函数级验证产物)
- mock 启动日志 `mock-logs/mock-19*.log`

## 八、同日复审轮(对本轮总结声明逐点实勘 + 修复)

复审方法:与 P3P4 审计 §三.5 相同——对首轮总结中"声明过"的关键点逐一重新取证,不接受声明本身。

| # | 判定 | 实勘内容 | 处置 |
|---|---|---|---|
| R-1 | **重大修正(O3 撤回、O5 前半句错误)** | 首轮 O3/O5 都源于**只枚举了 requests/default/ 单目录**:requests/ 下实有 default/ide/system 三个租户树。ide/ 下存在 74 个 hex-id .out(信封粒度),其中 021da488 正是 PG 停机窗口完成的 midstream 请求——telemetry 镜像在停机期间正常落盘(req+out),「PG 不可用时镜像仍写入」对 chat 成立;PG 侧行缺失但镜像在,「镜像可能多于 PG」单向容错实测成立 | O3 撤回;O2/O5 改写;④ 表证据升级 |
| R-2 | **根因定位(租户派生分裂)** | telemetry 镜像租户=`ApplicationCode\|\|TenantID`(client.go:1211),v2 镜像租户=会话租户;API key 46 的 application=ide → 同一请求三件套分裂在 ide/(telemetry 信封)与 default/(v2 turn/final_full)。API key 46→application_id 4→code "ide" 数据实证 | O2 改写;对账规则更新(§六.4) |
| R-3 | 声明通过 | FileBodiesStore 惰性建目录(bodies_store.go:59-68 构造期无 MkdirAll)支撑 session_bodies 缺席解释 | 无需修 |
| R-4 | 声明通过(数字修正) | r5 标记行干净查询=11 行中 success 9(客户端 6/6;多出为网关内部重试同标记行),首轮写"6 行"不准 | ③ 表修正 |
| R-5 | 声明通过 | D2 的 .env.local 未被 git 跟踪(check-ignore 实证),本地修复不入库安全 | 已修 |
| R-6 | **D1 修复+部署级验证(实测)** | 修复路径:dl_write_env 增 6 键透传 → bash 函数级直调(未设=空行 6 键齐、注入值逐字透传)→ 部署入口克隆 pull 收口(`diff` 与主树逐字节 IDENTICAL)→ **走管线部署实装(build 2353,git_sha 2d750fb4)**:生成的 run env 含 `LLM_GATEWAY_STORAGE_MODE=full`,容器 env 同,`hotzone_enabled=true` 且 **`mirror.hotzone_on=true`(O4 修复首个 live 证据)**;全程未 export 任何密钥,**D2 修复同步生效(无 ADMIN_API_KEY 拒写中止)**。随后并行会话的一次管线部署(2354,其 shell 未设 STORAGE_MODE)按新语义写入空行、热区不激活——「只透传已设值」的对照面当场验证 | D1/D2 关闭;见 R-9/R-10 的运行时层补验 |
| R-7 | 声明通过(表述收紧) | 首轮"vapeur3 二进制已含 P3-P5 代码"是依据其 /metrics 暴露 P5 per-mode 键的**推断**(vapeur3 分支不可考);D1 结论不依赖该推断(dispatcher 空 mode 行为是代码实勘) | D1 行措辞已收紧 |
| R-8 | 环境事实 | 并行会话在演练窗口内先后三次管线部署(2349/2353/2354),手工注入的 env 每次丢失——修复前是缺陷(D1),修复后是设计(不设=空行=历史行为);需要热区的部署必须在其调用 shell 显式 export | 已在 R-6 一并处理 |
| R-9 | **新发现(运维认知)** | 热区文件落在**容器可写层**:deploy-local 的 docker run 只 bind-mount attachments/logs/raw-logs/backups 四个状态目录,**不含 data/**,容器重建(docker rm -f + run)即热区清零。对缓存/镜像属纯语义无损(fail-open,L1.5 冷启动回源 PG,实测回填正常),但意味着「本地每次部署=热区重置」;镜像计数器亦同进程归零。如需跨部署保留,后续可把 data/ 加入 bind-mount 白名单(新需求,未做) | 2353 窗口产生的镜像文件在 2354 容器中不存在;cache/ 在重建后由回填重建(gw_hz-drill-sess-1.json 再现) |
| R-10 | **全映射闭环(单请求六点对账)** | 请求 f6d847fe…(plain chat,success):PG bodies_hot(req 103/out 103/resp 329)+ **requests/ide/ 三件套齐(req+resp+out,信封粒度,telemetry 侧)** + requests/default/(req+resp,turn 粒度,v2 侧)+ final_full:gw_hz-drill-sess-1.out(v2 出向)+ hotzone/cache/gw_hz-drill-sess-1.json(L1.5)。ide/.req 与 .out gunzip 实证=完整请求信封。附带实证:预热窗口内失败的 no_candidate 请求(525ca3fc)同样有 req 半边镜像+PG 行,resp/out 按 F4 null 豁免缺席 | R-2 租户分裂口径的单请求全维度钉死 |
