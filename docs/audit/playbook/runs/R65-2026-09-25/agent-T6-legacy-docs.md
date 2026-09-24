# T6 R64遗留复核+文档一致性 子代理报告（窗口：2026-09-24T05:00..2026-09-25T05:00, HEAD 9635b9b17）

> R65 轮只读子代理原文留档。主代理复核结论见轮文档。

## 一、R64 遗留逐项判定表

| # | 项 | 判定 | 证据（file:line 亲读） |
|---|---|---|---|
| 1 | §一.1 mock-probe 生产接入拍板 | **仍遗留**（第三轮+1，未拍板） | cmd/gateway/ grep "mockprobe" 零命中；装配仍仅 cmd/gateway-v2/main.go:60,1152,1193 |
| 2 | §一.2 P4 selector 接线 | **仍遗留** | NewTierSelector 零生产构造点；internal/endpointselect dispatch 零调用；serialize 多模态 gate 未落码（serialize_ollama.go:199-206）。库函数面健康（irToCatalogProtocol、权重 higher=higher 三方一致） |
| 3 | §一.3 V800 boot ensure | **仍遗留** + 状态漂移（已知）：**下一可用迁移 746 → 747**（9635b9b17 占用 746） | db ensure 链零 800 消费者；800 唯一升级通道=sequence |
| 4 | §一.4 auto 专项 P2/P3 | **规划内待推进**（非回归） | P2=TierSelector 接线；P3=V3 影子为运行期观察项；gate 口径已限 heuristic（analyzer.go:374,384,421,476） |
| 5 | §一.5 大专项候选三项 | **全部仍遗留** | ① sanitizer read-modify-write+fail-open 原样（smart_sani_guard.go:229-240、:336-349）；② nodestatecache 零生产 import；③ bg keyword LIKE 未转义（feedback_analyzer.go:260，行号自 252 漂移） |
| 6 | §一.6 installer 凭据收尾 | **仍遗留** | refresh.token 唯一写点 auto_activate.go:174-186；licensing/token_refresh.go:170 读的是另一路径（~/.kx-gateway/refresh_token），写入文件确实零读取方 |
| 7 | §一.7 PR4 删 python mock | **仍遗留** | scripts/mocks/llm-mock-upstream 全在 |
| 8 | §一.8 R61 §六 1/2/3/4/5 | **5 项全部仍遗留**（R64 判定属实，两处登记细节漂移） | 1 读面归一 ~7 处属实；2 parse 面 requestID 4/4 属实（extensions_restore.go:107 锚点失效——该文件 grep "unknown" 零命中）；3 ClaimDueCallbacks 从不 Commit（store.go:825-869）；4 UA 两份真重复（client_fingerprint.go:23 与 executors/executor.go:1039）；5 btrim vs TrimSpace 漂移（743:64 等 vs protocol_normalize.go:71） |
| 9 | §一.9 例行部署 154/245 | 不判定（运维面） | — |

§二两教训机制核验：i18n 门禁 exit 1（i18n-audit.mjs:146-176）与 testbench 门禁 exit 2（metrics.go:292-297、auto-testbench.sh:41-43）均仍在位。

## 二、文档一致性发现

| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| 1 | **P3** | **746 未入 sequence 通道**（dbinit+boot ensure 双臂齐但 sequence 缺席，与 744/745 三臂先例不同）；自测钉桩列表未含 746 | apply-db-revision-sequence.sh:578-591；test:79 | 补登【已修】 |
| 2 | **P3** | 9635b9b17 commit「SSOT+installer 五点同步+boot ensure 全链对齐」——五点确已含 746（声称成立），但未披露 sequence 通道缺席 | docs/db-changelog.md:430 | 随 #1 补记【轮文档记】 |
| 3 | **P3** | handoff「下一可用迁移： 746」漂移→747 | handoff:6 vs 746 文件在库 | R65 handoff 更新【已更新】 |
| 4 | **P3** | handoff §一.5③ 行号漂移：feedback_analyzer.go「:252」实为 :260 | handoff:28 vs :260 | 更新引用【已更新】 |
| 5 | P4/登记勘误 | R61 §六.2「extensions_restore.go:107 仍打 unknown」与现状不符（零命中）；parse 面 4 处主发现不受影响 | R61 轮文档:94 | 剔除该子锚点【轮文档记】 |
| 6 | 记录（低） | mock-probe README §0「单一开关 MOCK_PROBE_ENABLED」为简写统称，实际 env=LLM_GATEWAY_MOCK_PROBE_ENABLED；文档内部自洽非漂移 | README:27、01-requirements.md:31 | 可不动 |

## 三、核实为健康的面

1. **hotzone 方案（7489c8e92）文档-代码互洽**：纯文档提交，H1-H5 零落地，但方案文档自述"待实施"且验收清单未勾选——不构成漂移（勿在下轮误判）。
2. **README 8 语言 installer 章节真实一致**：结构计量完全一致；es/fr/ar 的 fallback 链系译文用词非漏改；章节事实与 installer 代码核对一致。
3. **mock-probe 文档 vs 代码一致**：装配位置、四端点、旁路、2×2 通道、env 四键全部吻合。
4. **审计轮文档验证记录抽查 4 条全实**（sequence 门禁含 745/800、i18n exit 1、testbench exit 2、240 例口径）。
5. **.env.example Mock Probe 4 读取点精确对账通过**。
6. **R64 §二修复抽查在位**（F4/F12/F7/F6）；746 建号无撞号。

## 四、未覆盖项与原因

1. §一.9 部署（运维执行面）。
2. R64 §四实跑类验证（只读不可重跑，采信静态证据）。
3. auto P3 V3 影子（运行期灰度观察）。
4. R61 §六.4 UA 第三份封闭集枚举（原文即"待确认"）。
5. R63 §三 14 项中未列入本次指令的项（窗口内 diff 未触碰，维持）。
