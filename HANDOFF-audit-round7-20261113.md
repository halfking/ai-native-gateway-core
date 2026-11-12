# 2026-11-13 注入检测与脱敏恢复域审计轮 handoff

## 结论与范围

本轮＝第五轮（`422e2c90e`）之后的**「下一轮提示词」三项任务执行轮**：
1. ✅ 凭据监控摘要端点优化方案（四条路径分析）
2. ✅ 注入检测与脱敏/恢复两域逐条审计（P2 已修：media 端点守卫）
3. ✅ 运营面处置建议（6 个凭据翻转 + F1 口径分叉修复）

完整主档见：
- `docs/audit/2026-11-13-monitor-summary-optimization-paths.md`（凭据监控优化方案）
- `docs/audit/2026-11-13-脱敏恢复域逐条审计轮-主档.md`（脱敏/恢复域审计，第七轮产出）
- `docs/audit/2026-11-13-credential-status-operational-guide.md`（凭据状态运营指南）
- `docs/audit/2026-11-13-next-round-summary.md`（本轮工作总结）

工作区 `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`。
开工树 `ff1f17a6c`（= origin/main = github/main，7 笔 docs/版本戳）。

## 改动与根因

### 1. ★★★★ 凭据监控摘要端点优化方案（本轮交付 #1，待裁决）

**背景**：第五轮审计从 211 真实日志挖出 `/api/credentials/monitor-summary` 在 245/154 上持续 500（`duration_ms=15007`），真凶已定位为 `total_requests` 的两臂 COUNT。

**本轮交付**：`docs/audit/2026-11-13-monitor-summary-optimization-paths.md`（16,564 字节）
- **四条路径的代价与口径影响分析**：
  | 路径 | 口径影响 | 工程量 | 推荐场景 |
  |---|---|---|---|
  | ① 近窗计数 | ★★★★ 高（数字含义变化） | ★ 低 | 运营确认「只看近期趋势」+ 窗口 ≤ 7 天 |
  | ② 复用 MV | ★★★ 中高（24h 窗口） | ★ 低 | 运营确认「24h 窗口满足需求」+ MV 口径等价 |
  | ③ 异步物化 | ★ **零**（准实时） | ★★★★ 高 | **必须全历史累计** + 可接受准实时 |
  | ④ 接受 500 | ★ 零 | ★ 零 | 该页面使用频率极低 |

- **252 库实测数据**（2026-11-12）：
  - 母表 `session_turns` = 1,411,419 行 / 7 分区
  - 单凭据计数 **12 s 超时** ⇒ 74 凭据 × 各一次 ⇒ 必越 15 s
  - **索引已命中**（`Bitmap Index Scan`），但匹配行数十几万 ⇒ `Bitmap Heap Scan` 回表 ⇒ 慢的根因是「要数完」，不是「找不到」
  - 换 `GROUP BY` 聚合形态实测 **23.4 s** ⇒ **任何同口径改法都过不了 15 s**

- **推荐方案**：
  - 若「数字含义可以变」⇒ 路径 ① 或 ②（工程量最小）
  - 若「数字含义不能变」⇒ 路径 ③（**异步物化，口径零变化是唯一不触碰裁决面的路**）

- **实施前必要实测**：
  - 路径 ① 的窗口与耗时关系（24h / 3 天 / 7 天各取 3 个样本凭据）
  - 路径 ② 的 MV 口径等价性验证（`credential_model_call_history.total_calls` vs `session_turns` 行数）

- **状态**：**待裁决**（需运营/产品明确：`total_requests` 要回答什么问题？全历史 or 近期趋势？）

---

### 2. ★★★★ 脱敏/恢复域逐条审计（本轮交付 #2，P2 已修）

**背景**：第五/六轮登记项「注入检测与脱敏/恢复两域逐条审计」的兑现。

**本轮交付**：`docs/audit/2026-11-13-脱敏恢复域逐条审计轮-主档.md`（第七轮产出，已完成）

**核心发现（P2 已修）**：`/v1/images/generations`（images_generations.go）与 `/v1/videos/generations`（videos_generations.go）**完全绕过 SmartSaniGuard**：
- **输入侧**：`sanitizeEnvelope` 的 switch 没有 media 分支（default 找 messages 找不到 ⇒ 原样直通），两个 handler 也不挂中间件 ⇒ **prompt 明文出上游**
- **响应侧**：media 响应是非流式 JSON（{created, data[]}），既非 native Messages/Responses 形态也无 choices ⇒ 即使过拦截链也只落在 maskUnrecognizedBody，从不还原；handler 直接写 respBody，根本不进链 ⇒ **占位符出得去、原文回不来**
- **为什么既有量具全绿**：5 序列化器还原矩阵/反射门只管 IR 参数还原轴；脱敏钉测全部经 ChatHandler 入口构造 ⇒ 守卫在缺陷处没有牙。骨架端点（2026-10-08 Phase C）创建时守卫链尚未建成，此后无「端点×挂载点」对拍门 ⇒ 缺口存活 3 个月。

**实施**：
1. `security/sanitize/input_protocols.go`：sanitizeEnvelope 增 `/v1/images|/videos/generations` 分支（prompt 复用 completions 同款口径；字符串/数组两形态；非文本 token-id 直通）
2. `security/sanitize/media_restore.go`（新）：`SanitizeRestoreInterceptor.RestoreMediaBody` —— 映射取请求 Context 的完整活字典（中间件 Wrap 尾部放入，已含 Redis 会话装载与本次新增）；复用 `restoreNativeNestedValue` 通用树行走：已知占位符还原 / 未知标记打码 / 残余保留标记 fail-closed（ErrMediaRestoreBlocked）
3. `domains/streaming/images_generations.go` + `videos_generations.go`：`SetSanitizeGuard` 成对装配；鉴权后绑定租户（`WithAuthenticatedTenant`，与 chat 同契约）；2xx 响应过 RestoreMediaBody，还原通道报错 ⇒ 502 `sanitize_restore_blocked`
4. `cmd/gateway/goal_control.go`：installSmartSaniGuard 扩参接线两 handler；**还原拦截器构建失败时撤回输入中间件（SetSanitizeGuard(nil,nil)）** —— 「只脱敏不还原」的半装配比不装配更糟；`SanitizeGuardWired()` 探针只在两半都非 nil 时返回非 nil

**门与验证**：
- 钉测 8 条：envelope 2（prompt 入洗+元数据不动 / 无 prompt 直通）+ restore 5（直通/还原/打码/截断 fail-closed/键名标记 fail-closed）+ 端到端 2（上游收占位符+客户端收原文 / 伪造占位符被打码）
- **变异验证**：禁用 envelope media 分支 ⇒ sanitize + streaming 两包当场红；还原 ⇒ 绿 ⇒ 门有牙
- `go test ./security/sanitize/` ok 10.1s；`./domains/streaming/` ok 86.2s；`./cmd/gateway/` 装配序钉 ok

**通用式 §704**：**守卫链覆盖面必须以「数据面端点清单×挂载点」机器对拍为准；轴内绿 ≠ 端点面绿**。

**登记不修**：
- 上游错误体全量入 `_request_logs` + slog body_preview 无脱敏（取证面改动归 owner 裁决）
- 反射矩阵盲点：无 json tag 字段、响应方向不在闸内
- 批5（f014129bd）后 responses 入站缺「占位符→回显→还原」端到端钉测（建议下轮补）
- ASR `/v1/audio/*` multipart 面 prompt 未入洗（transport 不同，另案）
- armor middleware.go / v2 pipeline 死面（无生产调用方）

**注入检测域 F-41 维持登记**：
- detector/checker 仍 nil 接线（cmd/gateway/main.go:7415）；`user_content` 生产链仍无人写（唯一写入方在默认关闭的 /v2 演示面）；governance_prompt_injection stage 从不注册
- armor：observe-only + MockJudge(0.0) 兜底（ARMOR_JUDGE_ENDPOINT/MODEL/API_KEY 三 env 全设才启用真 judge）+ 阈值 0.7 硬编码 TODO ⇒ 生产现状=零判别力观测面；**ARMOR_JUDGE_* 未入任何配置文档**（新增登记）
- 测试全部直接构造 hook/plugin、手动塞 Metadata ⇒ nil 接线回归对全部现有测试不可见
- **裁决**：维持 v3（全面审计v3/03 移交清单）的修复方案与登记，不在本轮实施（需要 *sql.DB 桥接 + user_content 写面设计，属功能切片非缺陷修补）

---

### 3. ★★★★ 运营面处置建议（本轮交付 #3，待执行）

**背景**：第五轮审计从 211 真实日志挖出 ASR/TTS 端点 503（`/v1/audio/transcriptions` 35 次），根因是遗留 `credentials.status='disabled'` 列把健康凭据挡在路由外。

**本轮交付**：`docs/audit/2026-11-13-credential-status-operational-guide.md`（13,663 字节）

**问题定性**（211 真库实测 2026-11-12）：
- **503 端点**：`/v1/audio/transcriptions` 35 次 503、`/v1/embeddings` 23 次 503（6h 窗）
- **诊断信号不可用**：日志只有 `[candidate_diag] db_empty {plan_count:0}` ⇒ 分不清「模型没登记」与「被状态闸滤空」
- **根因**：`model_offers` 里**有** `whisper-large-v3-turbo`（`canonical_id=3346055`），挂的凭据 `99009` 的 `credentials.status='disabled'`（2026-11-05 01:58 写入），而 `lifecycle_status='active'` / `availability_state='ready'` ⇒ 上游其实活着（192.168.31.13:8081 实测可达）⇒ 对照实验：生产口径候选 **0** / 放掉遗留 `c.status` 闸 ⇒ **2**
- **规模与影响**：211 真库 13 条 `status='disabled'`，其中 12 条 `lifecycle_status='active'`，带 offer 的 6 个凭据（whisper×2 / tts-macos-say / glm 家族 / minimax 家族）⇒ ASR/TTS 端点在 211 上全部 503

**F1 口径分叉**（已知、未修、活体影响当前=0）：
| 位置 | 口径 | 文件:行号 |
|---|---|---|
| **路由候选 SQL（严格）** | `c.status = 'active'` | `provider/client.go:1215` |
| **探针/回填/健康检查（宽松）** | `c.status IN ('active', 'cooling', 'degraded')` | `bg/node_probe.go:3527` + 4 处 |

- **影响分析**：`cooling` / `degraded` 的凭据会被路由单方面丢弃，而同一次探针/回填又把它当「可用」来测 ⇒ 口径不一致；活体影响当前=0（211 上无 cooling/degraded 凭据），但属潜伏分叉
- **建议修复方案 A**（推荐）：收窄到「只 active 可用」，与路由口径统一（改 5 处宽松口径）
- **方案 B**（备选）：放宽到「cooling/degraded 也可路由」，但需先确认其业务语义

**处置方案**：
1. **前置检查 ①**：确认「disabled 非人工置位」（`manual_disabled=FALSE` 且 `auto_disabled_reason IS NULL`）
2. **前置检查 ②**：核对上游可达性（curl base_url）
3. **处置 SQL**（事务内：核对→改→验证→COMMIT）：
   ```sql
   UPDATE credentials 
      SET status = 'active', updated_at = NOW()
    WHERE id IN (99009, 99010, 99011, <其余 3 个 id>)
      AND COALESCE(status, 'active') = 'disabled'
      AND COALESCE(lifecycle_status, 'active') = 'active'
      AND COALESCE(manual_disabled, FALSE) = FALSE;
   ```
4. **翻转后验证**：候选数 0→2 / 端点 503→200 / 日志 db_empty 消失
5. **24h 监控**：观察 503 是否复现、上游是否稳定

**完整处置流程图 + 风险与应急预案**（见运营指南文档）

**状态**：**待执行**（需先完成前置检查：① 查出 6 个凭据完整 ID；② 前置检查通过）

**诚实边界**：
- 6 个凭据的完整 ID 列表尚未从 211 真库取出（主档只给了族名称）⇒ 需通过 252 共享库查询
- 211 容器内无 psql，需通过其他方式查询（例：252 共享库 or 245/154 容器）

---

## 发布真值与验证

**本轮产出三份文档**（无代码改动，脱敏/恢复域修复由第七轮完成）：
| 文件 | 大小 | 状态 |
|---|---|---|
| `docs/audit/2026-11-13-monitor-summary-optimization-paths.md` | 16,564 B | ✅ 已写入 |
| `docs/audit/2026-11-13-credential-status-operational-guide.md` | 13,663 B | ✅ 已写入 |
| `docs/audit/2026-11-13-next-round-summary.md` | 9,302 B | ✅ 已写入 |
| `docs/audit/2026-11-13-脱敏恢复域逐条审计轮-主档.md` | 已存在 | ✅ 第七轮产出 |

**脱敏/恢复域验证**（第七轮）：
- `go build ./...` / `go vet ./...` rc=0；gofmt 触碰文件干净
- Go 全量 `go test ./... -count=1`：382 包 ok + ursmcheck 1 FAIL（607s 撞默认 10min 单包帽 = 超时伪影；帽后真红＝死键棘轮 0→1，已被 40b5d5a31 修复）
- ursmcheck 单包重跑（-timeout=2400s）：全包仍撞掏空门探针容量域（TestTsvHollowout 33m38s ⇒ 40min 预算内 panic；0 个 `--- FAIL`）；门判据面单跑收口 ok 129.4s；死键读数=0（=基线）

---

## 遗留与下一轮

### 立即可做（无依赖）
1. ✅ **脱敏/恢复域审计**：已完成（见第七轮主档）
2. 🔄 **补齐 6 个凭据 ID**：通过 252 共享库查询（211/245/154 共用）
   ```bash
   # 在 252 上执行（或通过 245/154 的容器）
   psql -U llm_gateway -d llm_gateway_db -c "
   SELECT c.id, p.catalog_code, p.display_name, COUNT(mo.id) AS offer_count
   FROM credentials c
   JOIN providers p ON c.provider_id = p.id
   LEFT JOIN model_offers mo ON mo.credential_id = c.id
   WHERE COALESCE(c.status, 'active') = 'disabled'
     AND COALESCE(c.lifecycle_status, 'active') = 'active'
   GROUP BY c.id, p.catalog_code, p.display_name
   HAVING COUNT(mo.id) > 0
   ORDER BY c.id;"
   ```

### 待裁决（阻塞实施）
3. ⏸️ **monitor-summary 路径选择**：需运营/产品拍板
   - 问题：`total_requests` 要回答什么问题？全历史累计 or 近期趋势？
   - 输入：四条路径的代价与口径影响分析（已写入方案文档）
   - 输出：路径编号 ① / ② / ③ / ④
   - 后续：若选路径①/②，需先实测（见方案文档 §四）；若选路径③，需立项设计

4. ⏸️ **凭据翻转决策**：需运营 + 技术负责人拍板
   - 问题：6 个凭据是否翻转 `status='disabled'` → `='active'`？
   - 输入：前置检查结果（① 非人工置位 ② 上游可达性）
   - 输出：书面确认（含理由）
   - 后续：若确认翻转 ⇒ DBA/本会话执行 SQL + 翻转后验证

5. ⏸️ **F1 口径分叉修复**：需与运营确认 `cooling` / `degraded` 的业务语义
   - 问题：`cooling` / `degraded` 的凭据是否应该参与路由？
   - 输入：当前活体影响=0（无 cooling/degraded 凭据），但将来可能出现
   - 输出：① 方案 A（收窄到「只 active」）or 方案 B（放宽到「cooling/degraded 也可路由」）
   - 后续：代码改 5 处 + 测试 + 部署

### 已登记遗留（下轮候选）
6. 注入检测 F-41 桥接（v3/03 移交清单方案）—— owner 排期
7. media 端点守卫的「端点×挂载点」机器对拍门（§704 通用式落成门）
8. responses 入站占位符端到端钉测；ASR multipart 面
9. ARMOR_JUDGE_* 配置文档补注
10. 63 个老 stash 的逐条清算（另案）

---

## 下一轮提示词

```
对 llm-gateway-go 继续执行本轮未完成的三项裁决与实施：

1) ★★★【待裁决 #1】补齐 6 个凭据的完整 ID 列表（通过 252 共享库查询，无依赖），然后执行前置检查：
   ① 确认「disabled 非人工置位」（manual_disabled=FALSE 且 auto_disabled_reason IS NULL）
   ② 核对上游可达性（curl base_url）
   若前置检查通过 ⇒ 由运营 + 技术负责人书面确认是否翻转（见运营指南文档 §三）

2) ★★★【待裁决 #2】monitor-summary 路径选择：需运营/产品拍板「total_requests 要回答什么问题？
   全历史累计 or 近期趋势？」⇒ 输出路径编号 ① / ② / ③ / ④（见优化方案文档 §三）
   若选路径①/②，需先在 252 库实测窗口耗时/MV 口径等价性（见方案文档 §四，可直接执行的 bash 命令）
   若选路径③，需立项设计（后台 job / 汇总表 / 失败重试 / 监控）

3) ★★★【待裁决 #3】F1 口径分叉修复：需与运营确认「cooling / degraded 的凭据是否应该参与路由？」
   ⇒ 输出方案 A（收窄到「只 active」，推荐）or 方案 B（放宽到「cooling/degraded 也可路由」）
   ⇒ 代码改 5 处（bg/node_probe.go:3527 + 4 处）+ 测试 + 部署

纪律（本轮新增，必须照做）：
· ★★★ **三项裁决都需要「书面确认」或「明确输出」**，不要凭猜测实施 —— 口径变更 / 数据翻转 / 
  流量分配都是运营裁决面，技术只负责给出「代价与影响分析」。
· ★★ **实施时必须带「真库对照」**：252 上 EXPLAIN ANALYZE + 端点 HTTP 计时（211 与 245/154 两侧都要）；
  凭据翻转后必须验证「候选数 0→2 / 端点 503→200 / 日志 db_empty 消失」三件事。
· ★★ **6 个凭据的完整 ID 必须从真库取**，不要用主档里的族名称（whisper×2 / tts-macos-say / glm / 
  minimax）当作完整列表 —— 处置 SQL 需要具体的 credential_id。
· ★ **monitor-summary 若选路径③（异步物化）⇒ 需单独立项设计**：后台 job（定时触发 + 并发控制 + 
  失败重试）/ 汇总表（schema + retention）/ 监控（job 健康度 + 汇总表新鲜度）/ 冷启动 fallback。
```

---

**文档版本**：v1.0  
**作者**：本会话（Mavis）  
**日期**：2026-11-13  
**开工树**：ff1f17a6c  
**状态**：三项交付已完成，待后续裁决与执行
