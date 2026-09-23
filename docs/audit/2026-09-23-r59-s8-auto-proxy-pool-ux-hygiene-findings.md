# R59 S8 组审计发现（D11 auto + D12 代理 + D13 免费池 + D15 UX + D17 卫生）

- 审计员：S8（R59 48h 审计轮）· 基点 main @ 9f7b0ea3f · 只读审计（可跑测试）
- 必跑：`go build ./...` PASS；`go test` proxy / discovery / domains/hooks/goal / domains/sessionsummary / domains/routing / internal/clienttype / settings / telemetry 全 PASS。
- 证据方法：commit diff 实读 + HEAD 现场复核 + 包级 import grep + `golang.org/x/tools/cmd/deadcode` 全仓扫描（仅作候选线索，不直接定罪）。

## 发现表

| # | 严重度 | 域 | 发现 | 证据位置 | 状态 |
|---|---|---|---|---|---|
| F1 | **P1** | D17 | **552770c96 dl_shared_root 递归自引用守卫被后续 commit 意外回滚**。81e4ab932（T2 测试轮，提交说明自述 scripts/deploy-local-lib.sh "取远端消冲突（与本轮无关）"）在冲突消解时取了远端版本，把 `dl_shared_root`/`dl_shared_pg_dir` 的绝对路径白名单守门整体删掉，HEAD 恢复为无守门 one-liner。552770c96 修的真实故障链路（`KAIXUAN_ROOT='${KAIXUAN_ROOT:-…}'` 递归自引用 → docker `--mount source=` 非 literal → "invalid mount path"）随即复发。且 `tests/deploy_local_contract_test.sh` 从未覆盖该守门，回滚零红灯——这正是 R59 纪律 2（修复必须测试背书）的反例。 | scripts/deploy-local-lib.sh:61-62（HEAD 无守门）；diff 552770c96..HEAD | ❌ 需恢复守门 + 补契约断言 |
| F2 | **P2** | D12 | **HK 规避 "hkg" 变体缺口**。42038e1dc 后 HK 规则为 Keywords{香港} + Tokens{HK,HONGKONG} + Phrases{HONG KONG}（proxy/parser.go:557）。`HKG-01`/`hkg` 风格机场码命名分词出 `HKG`（≠`HK` 整词）、joined 串不含 `HONG KONG` → guessLocation 返回 ""，而 `IsRegionBanned("")` 语义为放行（types.go:219 fail-open）→ 禁区 overlay 对"识别不出地区"的节点整体免疫。审查项点名的 "Hong-Kong-Free-1"/"hongkong" 均已闭合（TestGuessLocationHKNames PASS），唯 hkg 漏。建议 HK Tokens 增 `HKG`（并评估 PVG/SHA 等机场码是否纳入规则表的通用策略）。 | proxy/parser.go:557,582；proxy/types.go:219-233；proxy/parser_test.go:80-100（无 hkg 用例） | ❌ 一行规则可修 |
| F3 | **P2** | D15 | **credentialquota 客户端类型白名单枚举漂移**。ce85e767a 给 `internal/clienttype.Normalize` 增补 minimax-code/deepseek-code，但 `domains/credentialquota/normalize.go:10-15` 手工镜像的 `allowedClientTypes` 未同步——这两类客户端在凭据配额/FpSlot 策略维度被归一为 "unknown"，无法按客户端类型下发 MaxConcurrent/MaxFPSlots 策略。注释自称 "mirrors the clienttype closed set"，手工镜像即 SSOT 违约的机制性证据。修法：白名单改由 clienttype 包导出集合消费，删本地副本。 | domains/credentialquota/normalize.go:10-15；internal/clienttype/clienttype.go:14-31 | ❌ 枚举 SSOT 收口 |
| F4 | P3 | D12 | 订阅刷新（1h）与全节点探活（5min）interval 为 NewManager 硬编码，无 settings 键、不可热调（swapLoop 的 30s/阈值 2 经 SelectionPolicy 可配）。现状盘点非缺陷，但与 proxy.* 其余键的热调能力不一致，建议纳入 spec_proxy Specs。 | proxy/manager.go:113-114,1128,1148 | 盘点注记 |
| F5 | P3 | D13 | `scripts/govern-junk-canonical/main.go:371-377` 的 AliasUpserts 无 `WHERE status<>'disabled'` 守卫——离线运维脚本非生产路径，但与 2fa1942ad 的"四写点对齐"叙事不一致（实际是五处，脚本第六处漏网）。建议顺手对齐。 | scripts/govern-junk-canonical/main.go:371-377 | ❌ 低危补齐 |
| F6 | P3 | D15 | UA/客户端识别副本盘点结论（回应"几份副本"）：**UA 关键词匹配真重复 2 份**（streaming/client_fingerprint.go `extractClientType` + executors/executor.go `extractClientType`，循环依赖借口下的人工同步契约）；**客户端类型封闭集枚举 3 份**（clienttype.Normalize / credentialquota.allowedClientTypes / sessionmeta/extractor_rules.go:202——R52 后两处同步、F3 处未同步）；telemetry 双层（system-prompt pattern + body_client_marker 参数级）与 format_detector.go `matchByUserAgent`（格式探测 hints，用途不同）不算违例。新增一个客户端要改 6-7 处，F3 即漏网实锤。 | 见 F3 + domains/streaming/client_fingerprint.go、executors/executor.go:1034-1085、sessionmeta/extractor_rules.go:202 | 结构性债，SSOT 收口建议 |

## 验证通过项（✅）

- ✅ **D12 HK 规避根修（42038e1dc）主体**：phrase 切词重连匹配（Hong.Kong/hong_kong/多空格统一命中）、`proxy.default_banned_regions`（默认 HK、Dangerous、HotReload、≤5s 缓存）overlay 与订阅层/节点层三列表并集；选择路径（manager.go:385）与 admin 地区分布（manager.go:1998）两处口径一致；空 region 放行语义未变。R51 F2 闭合（除 F2 的 hkg 缺口）。
- ✅ **D12 代理链路盘点**：订阅 refreshLoop 1h / healthCheckLoop 5min / swapLoop 默认 30s·阈值 2 → ForceSwap 排除失败节点（excludeNodeID）；探活 per-node 互斥 + Stop ctx 取消；R52 节点缓存 + overlay 锁外读；R46 F7 订阅优先级排序（读侧 clamp [0,99]）；地域亲和 any/prefer_same/require_same 接线（load_balancer.go SelectNodeWithLocation）。
- ✅ **D13 免费池闭环**：freediscovery（模板→ResolveAPIKey→/models 扫描→FreeOf→ToS→discovery_results）→ admin 审查导入（import_service → free_resource_catalog，avoid 自动 disabled）→ autocombo VirtualFactory 查 catalog（RLS + SET LOCAL 同连接）过滤标准 `provider.Candidate`（candidateMatchesCatalog）+ 配额 preflight → 走 executors Router 标准供应商池消费。scan→register→pool→consume 闭环成立。
- ✅ **D13 alias 复活守卫（2fa1942ad F6/F7）**：discovery.go 三处 upsert（826/991/1024）+ alias_sync.go（270）+ taxonomy_sync.go（215）全部 `WHERE model_aliases.status <> 'disabled'` 在位；canonical 侧 status CASE 守卫在位。见 F5 的脚本侧唯一漏网。
- ✅ **D11 auto route**：L1/L2/L3 `partitionBySelectionLayer` 生产接线于 `planByTier`（router.go:1081-1111，lottoLen 限定第一跳加权彩票池，L3 兜底段不进彩票）；L1=优先+ok配额+并发余量、L3=饱和优先节点，与 dispatch ApplySoftPenalty 双读同一饱和契约。
- ✅ **D11 minheap 生产面判断证实**：`SelectTopN`/`SelectTopKWeighted`/`WeightedRouter` 生产零持有——全仓唯一消费者是 `tests/local/gateway/main.go`（本地压测 harness）+ 包内测试；executors 生产路由不 import domains/routing 的 WeightedRouter。**e2c582e74 把 minheap 接进 WeightedRouter 属于给测试基建做性能优化，生产价值为零的判断成立**（这也是清理候选 C5 的依据）。
- ✅ **D11 auto_route_selections_hot ensure 守卫（1bc6d7e12）**：db/db.go 目录短路守卫在位（44 列+4 索引+3 约束+视图全在位 = 零 DDL；任一缺失回落原路径；探针失败降级 warn+full ensure）。
- ✅ **goal.audit 三键（a429d64d0 B12）**：goal.audit_max_rounds(3,1-5)/audit_verify_enabled(true)/audit_verify_model(auto) 三键 spec 登记齐全，默认值与 audit_hook.go 三个读取点（272/275/487）逐一对齐。
- ✅ **X-Gw-Goal-Mode managed 入口（4f88f1861）三点**：①mode_hook.go:741-742 managed 头 → detectExplicit reason=header:managed（GoalRun 持久化仍 fail-closed 要求 body goal 对象）；②goal_control.go:212 `LLM_GATEWAY_GOAL_CLIENT_DRIVEN` 默认翻转 true（租户键 goal.client_signal_enabled 仍可显式关）；③message_source_digest.go:100 / message_source_v2.go:90 双查询 `NOT LIKE 'goal-%'` 排除影子轮 + shadow_turn_exclusion_test.go 回归钉桩。
- ✅ **D17 deploy 加固 2/3**：c165e0bce 路径归一化（deploy-local.sh:321-322 `tr -s '/'` + 尾斜杠压平）与 env 去重（awk 首序末胜）在位；5ce9bf87c `/bin/rm` 直调 + `>/dev/null` 防 mavis-trash stdout 污染 + osascript 挂死规避（deploy-local.sh:614-728）在位。
- ✅ **已删组件无残留**：chunk_buffer / error_detector_ring 在 Go/SQL/TS 全仓（除 docs）零引用；R55-F1b python env 解析器消费路径在 81e4ab932 的同文件回滚中幸存（只删了注释块，`done < <(_dl_safe_env_source …)` 保留）。
- ✅ **D15 web 轻量抽查**：B7 看板（1cd15c6a7）BoardPieGrid.vue 数据键 virtual_ips→client_ips 与 8 个 locale 文件同步、键位一致性钉桩测试；758bf92c2 仅动 menu-config.json；588e3e965 协议归一在 Go 写边界。近期 web 改动控件复用度良好，无新发现。
- ✅ **必跑**：`go build ./...` PASS；proxy/discovery/goal/sessionsummary/routing/clienttype/settings/telemetry 包测试全 PASS。

## 勘误登记

- 轮次分派表写 "D13…0fc829916"——0fc829916 实为 Wave1-A2 IR tool_use.input 包裹（归 S2）；48h 窗口内 discovery 相关改动是 2fa1942ad（alias 守卫）+ 5f160c37a（hot 表 ts 映射）。本报告按实际 commit 归位。

## 清理候选清单（标注建议，归 S2/后续裁决；本轮只供候选不做深挖）

| # | 候选 | 规模 | 依据 | 建议 |
|---|---|---|---|---|
| C1 | `domains/session/preprocess/`（整包） | 19 文件；redis_store.go 单文件 32 个不可达函数 | 全仓（除包自身测试）零 import 语句；deadcode 列 32+26 条 | 确认无外部脚本依赖后整包删除或移 attic |
| C2 | `adapter/unified/`（整包） | 5 文件、38 个不可达函数 | 全仓零 import（anthropic/openai adapter + registry 全家） | 同上；与现役 ir/provider 适配链重复定位 |
| C3 | `internal/fsstore/`（整包） | 4 文件、22 个不可达函数 | 全仓零 import | 确认 requestarchive 存储路径替代关系后删除 |
| C4 | `domains/nodestatecache/`（整包） | bitmap.go 22 + nodestatecache.go 21 + resources.go 17 条不可达 | 全仓零 import | 与 D09 节点状态现役实现重叠，裁决后删除 |
| C5 | `domains/routing` WeightedRouter 集群（weighted_router.go + minheap_topk.go + latency_tracker.go + unhealthy.go + 相关测试） | ~5 文件 | 生产零持有；唯一消费者 tests/local/gateway 压测 main；e2c582e74 minheap 优化落在测试基建 | 若保留压测 harness 则整体迁 tests/local 并钉注"非生产"；否则随 harness 一起退役 |

> 方法注记：deadcode 全仓扫描 3064 条不可达函数（以 cmd/* main 为根，不识别反射/SQL 派发），噪声大；C1-C5 是其中经"包级 import 零引用"独立核实过的子集，置信度高，但删除前仍应逐包确认无 build-tag/外部仓库消费。
