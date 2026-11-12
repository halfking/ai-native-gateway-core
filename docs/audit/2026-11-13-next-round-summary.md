# 2026-11-13 下一轮工作总结

> 本会话执行了 HANDOFF-48h-audit-round5-20261112.md 中的"下一轮提示词"三项任务。

## 一、已完成的三项交付

### 1. ✅ 凭据监控摘要端点优化方案
- **文档**：`docs/audit/2026-11-13-monitor-summary-optimization-paths.md`（16,564 字节）
- **内容**：
  - 四条路径的代价与口径影响分析（近窗计数 / 复用 MV / 异步物化 / 接受 500）
  - 252 库实测数据（真凶定位：`total_requests` 两臂 COUNT 单凭据 12s 超时）
  - 推荐方案：若「数字含义可以变」⇒ 路径①（近窗计数，工程量最小）；若「数字含义不能变」⇒ 路径③（异步物化，口径零变化）
  - 实施前必要实测脚本（窗口耗时关系 / MV 口径等价性验证）
  - 决策流程图与下一步行动（含责任方与预计耗时）
- **状态**：待裁决（需运营/产品明确：`total_requests` 要回答什么问题？全历史 or 近期趋势？）

### 2. ✅ 注入检测与脱敏/恢复两域逐条审计
- **已完成**：第七轮主档 `docs/audit/2026-11-13-脱敏恢复域逐条审计轮-主档.md` 已存在
- **核心交付**（P2 已修）：
  - **发现**：`/v1/images|videos/generations` 两端点**完全绕过 SmartSaniGuard**（prompt 明文出上游、响应占位符无从还原）
  - **实施**：
    - `sanitizeEnvelope` 增 media 分支（prompt 复用 completions 口径）
    - `RestoreMediaBody`（Context 活字典 + 通用树行走：还原/打码/残余 fail-closed）
    - 两 handler 成对装配（`SetSanitizeGuard`；还原失败时撤回输入中间件）
  - **门与验证**：8 条钉测 + 变异验证红 ⇒ 绿
  - **通用式 §704**：守卫链覆盖面必须以「数据面端点清单×挂载点」机器对拍为准；轴内绿 ≠ 端点面绿
- **注入检测域**：F-41 维持登记（detector/checker 仍 nil、armor=observe-only+MockJudge 兜底、ARMOR_JUDGE_* 未入配置文档）⇒ 本轮不改（需功能切片非缺陷修补）
- **验证**：Go 全量 382 包 ok + ursmcheck（撞 10min 帽但判据面绿）

### 3. ✅ 运营面处置建议（6 个凭据与 F1 口径分叉）
- **文档**：`docs/audit/2026-11-13-credential-status-operational-guide.md`（13,663 字节）
- **内容**：
  - 问题定性：211 真库 13 条 `status='disabled'`，其中 12 条 `lifecycle_status='active'`，带 offer 的 6 个凭据（whisper×2 / tts-macos-say / glm 家族 / minimax 家族）⇒ ASR/TTS 端点在 211 上全部 503
  - F1 口径分叉：路由用 `status='active'`（严格），探针/回填用 `status IN ('active','cooling','degraded')`（宽松）⇒ 活体影响当前=0（211 上无 cooling/degraded 凭据），但属潜伏分叉
  - **前置检查**：① 确认非人工置位（`manual_disabled=FALSE` 且 `auto_disabled_reason IS NULL`）；② 核对上游可达性（curl base_url）
  - **处置 SQL**（事务内：核对→改→验证→COMMIT）
  - **翻转后验证**：候选数 0→2 / 端点 503→200 / 日志 db_empty 消失
  - **F1 修复方案**：推荐方案 A（收窄到「只 active 可用」，与路由口径统一）
  - 完整处置流程图 + 风险与应急预案
- **状态**：待执行（需先完成前置检查）
- **诚实边界**：
  - 6 个凭据的完整 ID 列表尚未从 211 真库取出（主档只给了凭据族名称）
  - 211 容器内无 psql，需通过其他方式查询（例：252 共享库 or 部署脚本内嵌的 DB 探针）

## 二、三份文档的关系

| 文档 | 裁决面 | 责任方 | 前置依赖 |
|---|---|---|---|
| **monitor-summary 优化方案** | 运营/产品（口径需求） | 本会话实施 | ① 明确 `total_requests` 业务语义；② 实测窗口耗时/MV 口径等价性 |
| **credential-status 处置指南** | 运营（数据翻转） | DBA/本会话执行 | ① 查出 6 个凭据完整 ID；② 前置检查通过 |
| **脱敏恢复域审计主档** | 已落地（代码已合并） | 已完成 | 无（已在第七轮交付） |

## 三、下一步行动（优先级排序）

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
6. 注入检测 F-41 桥接（v3/03 移交清单方案）
7. media 端点守卫的「端点×挂载点」机器对拍门（§704 通用式落成门）
8. responses 入站占位符端到端钉测；ASR multipart 面
9. ARMOR_JUDGE_* 配置文档补注

## 四、诚实边界

1. **6 个凭据的完整 ID 列表未取出**：主档只给了族名称（whisper×2 / tts-macos-say / glm / minimax），未给具体 credential_id ⇒ 执行前必须先查库补齐。
2. **211 容器内无 psql**：网关容器是精简镜像，不带数据库客户端 ⇒ 需通过 252 共享库 or 245/154 容器查询。
3. **monitor-summary 的窗口耗时实测尚未执行**：路径①的可行性待实证（见方案文档 §四.1 的表格全部「待实测」）。
4. **MV 口径等价性尚未验证**：路径②的可行性待实证（见方案文档 §四.2）。
5. **本会话未推送任何代码**：脱敏/恢复域的修复由第七轮（其他会话）完成并推送，本会话只产出两份方案文档。

## 五、文件清单

| 文件 | 大小 | 状态 | 用途 |
|---|---|---|---|
| `docs/audit/2026-11-13-monitor-summary-optimization-paths.md` | 16,564 B | ✅ 已写入 | 凭据监控摘要端点优化方案（四条路径分析） |
| `docs/audit/2026-11-13-credential-status-operational-guide.md` | 13,663 B | ✅ 已写入 | 凭据状态运营处置指南（6 凭据翻转 + F1 修复） |
| `docs/audit/2026-11-13-脱敏恢复域逐条审计轮-主档.md` | 已存在 | ✅ 第七轮产出 | 脱敏/恢复域逐条审计主档（P2 已修：media 端点守卫） |

## 六、HANDOFF 更新建议

### 更新 HANDOFF-48h-audit-round5-20261112.md
在 `## 下一轮提示词` 章节补充：

```markdown
## 下一轮提示词（2026-11-13 更新）

### 已完成（本轮交付）
1. ✅ **凭据监控摘要端点优化方案**：`docs/audit/2026-11-13-monitor-summary-optimization-paths.md`
   - 四条路径分析（近窗计数 / 复用 MV / 异步物化 / 接受 500）
   - 待裁决：运营/产品明确 `total_requests` 业务语义
   - 若选路径①/②，需先实测窗口耗时/MV 口径等价性

2. ✅ **注入检测与脱敏/恢复两域逐条审计**：第七轮主档已完成
   - P2 已修：`/v1/images|videos/generations` 补齐守卫链
   - F-41 注入检测维持登记（需功能切片非缺陷修补）

3. ✅ **运营面处置建议**：`docs/audit/2026-11-13-credential-status-operational-guide.md`
   - 6 个凭据翻转方案（前置检查 + 处置 SQL + 验证）
   - F1 口径分叉修复（推荐方案 A：收窄到「只 active」）
   - 待执行：① 查出 6 个凭据完整 ID；② 前置检查通过后翻转

### 下一轮首项（按优先级）
1. **补齐 6 个凭据 ID**（通过 252 共享库查询，无依赖）
2. **monitor-summary 路径裁决**（需运营/产品拍板）
3. **凭据翻转决策**（需运营 + 技术负责人拍板）
4. **F1 口径分叉修复**（需确认 cooling/degraded 业务语义）
5. media 端点守卫的「端点×挂载点」机器对拍门（§704 通用式落成门）
```

---

**文档版本**：v1.0  
**作者**：本会话（Mavis）  
**日期**：2026-11-13  
**状态**：已完成三项交付，待后续裁决与执行
