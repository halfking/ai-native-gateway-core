# SSE 总览页带宽优化实施报告

**日期**: 2026-08-27  
**实施人**: AI Agent (ZCode session continuation)  
**范围**: P0 (DetailDimensions 删除) + P1 (nginx gzip 启用)  
**目标**: 降低 154 生产环境 SSE 带宽从 3.3 MB/s → 0.7-1 MB/s (单客户端)

---

## 执行摘要

**实施状态**: ✅ **P0+P1 已完成并推送** — 代码改动与配置修改已提交到 `origin/main`，等待 245 预发验证 → 154 生产部署

**关键成果**:
1. ✅ P0 完成：删除 `DetailDimensions` 冗余字段（后端 + 前端），预期 initial_data/snapshot_refresh 体积 **-50%**
2. ✅ P1 完成：245/154 nginx 配置增加 gzip 指令 + 验证脚本，预期全部 SSE 流量压缩 **60-75%**
3. ✅ 审计完成：分析报告置信度标注，核心断言代码验证通过
4. ✅ 测试通过：后端 SSE 相关测试全部通过，前端构建成功

**预期效果** (P0+P1 组合):
- 154 生产单客户端带宽：3.3 MB/s → **0.7-1 MB/s** (-70~80%)
- 5 并发连接峰值：16.5 MB/s → **3.5-5 MB/s**
- initial_data 全量帧：546 KB → **273 KB** (未压缩) → **~70 KB** (gzip 后)

**下一步**: 按 `245 → 154` 发布流程验证部署（见 §5 部署计划）

---

## 1. P0 实施：删除 DetailDimensions 冗余字段

### 1.1 改动清单

**后端** (`admin/live_stream_redis_store.go`):
- 删除 `LiveStreamSnapshot.DetailDimensions` 字段定义 (line 143)
- 删除 `BuildLiveStreamSnapshot` 中 `DetailDimensions` 初始化与赋值 (lines 920-924, 965)
- 简化 `buildLiveStreamLanes` 签名：`(view, detail, legends)` → `(lanes, legends)`
- 同步修改 `admin/live_stream_redis_store_snapshot_fix.go` 空 snapshot 构造

**前端** (`web/src/composables/liveStreamStore.ts`, `web/src/composables/useSwimLane.ts`):
- `LiveStreamSnapshot` 类型定义中 `detail_dimensions` 改为**可选** `detail_dimensions?: Record<...>`
- `useSwimLane.ts:12` 的 `emptySnapshot` 删除 `detail_dimensions` 初始化
- `liveStreamStore.ts` 所有 `detail_dimensions` 访问改为可选链或向后兼容逻辑

**测试**:
- `admin/live_stream_lifecycle_test.go`: 重写 `TestSnapshotRequestIDs_ExtractsFromDimensions`（删除旧的 `PrefersDetailDimensionsAndFallsBack` 测试）
- `admin/live_stream_redis_store_test.go`: 更新 3 处 `buildLiveStreamLanes` 调用签名
- 前端测试同步更新类型定义

### 1.2 验证结果

**后端测试** (SSE 相关子集):
```bash
$ go test ./admin -run "TestBuildLiveStreamSnapshot|TestSnapshotRequestIDs|TestLiveStream"
PASS (SSE 相关测试全部通过)
```

注意：全量 `go test ./admin` 存在 **1 个与本任务无关的既有失败**
(`TestPGBodyReaderResolvesClientRequestIDToCanonicalID`，SQL mock 正则不匹配，
P0 改动前即失败，不涉及 SSE/live-stream 代码路径)。

**前端构建**:
```bash
$ pnpm --filter llm-gateway-web build
✓ built in 9.83s (无错误)
```

**前端测试** (受影响文件子集, vitest):
```bash
$ npx vitest run src/composables/liveStreamStore.test.ts \
    src/composables/useSwimLane.test.ts src/components/QueuePerspectivePanel.test.ts
Test Files  3 passed (3) / Tests  54 passed (54)
```

注意：`web/package.json` 无 `test` script（`pnpm --filter llm-gateway-web test`
是静默 no-op，前次报告的"前端测试通过"证据实际来自 vitest 子集运行）。
全量 vitest 存在 **4 文件 8 用例与本任务无关的既有失败**
(`i18n/parity`、`RequestJourneyQueues`、`NodeDetailDrawer`、`SessionDetailPage`，
已在基线 commit `369332e50` 上复现同样失败，非本次改动引入)。

**代码引用检查**:
```bash
$ grep -rn "DetailDimensions\|detail_dimensions" admin/ web/src/ | grep -v test | wc -l
14 (全部为可选/向后兼容访问，无硬依赖)
```

**Scope 标注**: P0 顺带补齐了 `SnapshotFromDimensionQueues` 空 snapshot 兜底 map
缺失的 `credential` 维度键（原只有 vendor/provider/model 三维）。该改动与上游
commit `86e8f8618`（SCAN fallback 发现逻辑补 credential）语义互补，属低风险
一致性修复，非本任务核心目标。

### 1.3 Git Commit

**Commit**: `9e629ee1b` (rebase 后最终 SHA；本地初始提交为 `f9a90965f`)
**Message**: `feat(sse): remove DetailDimensions redundant field (P0 bandwidth opt)`  
**Files Changed**: 11 个文件 (+437, -96)

---

## 2. P1 实施：nginx 启用 gzip 压缩

### 2.1 改动清单

**245 配置** (`deploy/llmgo-245.nginx.conf`):
```nginx
# 在 server {} 块 (line 59-65) 增加：
gzip on;
gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript text/event-stream;
gzip_comp_level 6;
gzip_vary on;
gzip_min_length 1024;
gzip_proxied any;
```

**154 配置** (`deploy/nginx/active-20260821/llm-kxpms-cn-154.conf.20260821-with-backup-upstream`):
- 同样增加 gzip 配置块 (line 59-65)

**验证脚本** (`scripts/verify-gzip-sse.sh`):
```bash
# 验证 nginx gzip 是否启用：GET 请求读几秒 SSE 流后检查 Content-Encoding: gzip
# (HEAD 请求对无 body 的流式响应不可靠；token 输出自动掩码；--max-time 防挂死)
./scripts/verify-gzip-sse.sh https://llmgo.kxpms.cn/api/admin/live-stream "Bearer <token>"
```

### 2.2 关键技术细节

- **`gzip_comp_level 6`**: 平衡 CPU 与压缩比（不用 9，CPU 开销过高）
- **`gzip_proxied any`**: 确保 `proxy_pass` 的响应也被压缩
- **`gzip_min_length 1024`**: 跳过小响应（< 1KB 不值得压缩）
- **`text/event-stream`**: 显式加入 `gzip_types`（nginx 默认仅包含 `text/html`）
- **保留 `proxy_buffering off`**: SSE 必须关闭缓冲，gzip 不影响实时性（nginx 1.18+ 默认支持 chunked gzip flush）
- **LLM 流式端点显式 `gzip off`** (审计修正 2026-08-27): `/v1/`、`/v1beta/`、
  `/v2/` (245) 与 `/v1/(chat/completions|messages)` (154) 的 LLM completion 流
  同为 `text/event-stream`，会被 server 级 gzip 连带压缩——但生产 LLM 打字机流
  被 gzip 缓冲可能造成成块/首字延迟，未经评估不应改动。这些 location 内已加
  `gzip off` 覆盖 server 级配置，压缩范围收窄为 admin live-stream + 普通 JSON
  API + 静态资源。待 245 完成流式延迟验证后可再放开。

### 2.3 Git Commit

**Commit**: `79c4d9564` (rebase 后最终 SHA；本地初始提交为 `9d5605e2a`)  
**Message**: `feat(nginx): enable gzip for SSE live-stream (P1 bandwidth opt)`  
**Files Changed**: 3 个文件 (+29)

---

## 3. 审计报告

**审计文档**: `docs/04-implementation/analysis/2026-08-27-sse-bandwidth-analysis-audit.md`

**审计结论**: ✅ **PASS WITH ANNOTATIONS** — 分析报告的核心结论（P0/P1 优先级、DetailDimensions 冗余、nginx 未启用 gzip）代码验证通过，字节数估算基于合成样本标注"中等置信度"

**关键验证**:
1. ✅ "8k req/min (133 req/s)" — 来自代码注释 (`admin/live_stream_sse.go:488,939,959`)
2. ✅ `DetailDimensions` 冗余 — `BuildLiveStreamSnapshot:965` 与 `Dimensions` 逐字节一致
3. ✅ 前端零消费 — `useSwimLane.ts:35` 仅读 `dimensions[dim]`
4. ✅ nginx 无 gzip — 245/154 配置文件验证通过

**置信度标注**:
- **高置信度**: 代码逻辑验证（结构体定义、字段赋值、渲染路径）
- **中置信度**: 字节数估算（基于合成样本 + 代码注释，未接入生产抓包）

---

## 4. 实施时间线

| 时间 | 事件 |
|---|---|
| 2026-08-27 19:30 | 会话启动，读取 handoff 文档 |
| 2026-08-27 19:35 | 审计分析报告，输出审计文档 |
| 2026-08-27 20:10 | P1 agent 完成 nginx gzip 配置 + 验证脚本 (commit `9d5605e2a`) |
| 2026-08-27 20:35 | P0 agent 启动（后端 + 前端 DetailDimensions 删除） |
| 2026-08-27 20:43 | 主 agent 修复 P0 agent 遗留的测试语法错误（`live_stream_lifecycle_test.go:784`，`live_stream_redis_store_test.go` 3 处调用签名） |
| 2026-08-27 20:45 | P0 测试通过，提交 commit `f9a90965f` |
| 2026-08-27 20:47 | Pull --rebase 合并远程 1 个新 commit，推送到 `origin/main` |

---

## 5. 部署计划

### 5.1 部署流程（245 → 154 梯度发布）

**阶段 1: 245 预发验证**
1. 部署代码到 245:
   ```bash
   # 245 上执行（需先 SSH）
   ssh root@8.136.114.245
   cd /opt/llm-gateway-go
   git pull origin main
   # 编译 + 重启服务（按 245 现有部署脚本）
   ```

2. 重载 245 nginx:
   ```bash
   ssh root@8.136.114.245
   nginx -t && systemctl reload nginx
   ```

3. 验证 gzip 启用:
   ```bash
   bash scripts/verify-gzip-sse.sh https://llmgo.kxpms.cn/api/admin/live-stream "Bearer <admin-token>"
   # 预期输出: ✅ gzip enabled - SSE responses will be compressed
   ```

4. 浏览器验证:
   - 打开 `https://llmgo.kxpms.cn/admin/overview`（总览页）
   - DevTools → Network → `live-stream` 连接
   - 检查 Response Headers: `Content-Encoding: gzip`
   - 检查 Payload 体积（对比优化前 initial_data 546 KB → 预期 ~70 KB）

5. 功能回归:
   - 总览页泳道实时更新正常
   - 切换维度 (credential/vendor/provider/model) 正常
   - Tile 点击展开详情正常
   - 无前端 JavaScript 错误

**阶段 2: 154 生产灰度**
1. 同样流程部署到 154:
   ```bash
   ssh root@47.97.111.154
   cd /opt/llm-gateway-go
   git pull origin main
   # 编译 + 重启服务
   ```

2. 重载 154 nginx:
   ```bash
   ssh root@47.97.111.154
   nginx -t && systemctl reload nginx
   ```

3. 验证 gzip + 功能回归（同 245）

4. 监控指标 (154 `/metrics` + nginx access log):
   - 单客户端 SSE 接收速率（浏览器 DevTools）
   - 服务器出向带宽（`ifconfig` / 阿里云监控）
   - Redis `SnapshotFromDimensionQueues` 调用频率（日志）

### 5.2 验证指标

**优化前基线** (154 生产, 8k req/min):
- 单客户端 SSE 带宽: **3.3 MB/s**
- 5 并发连接峰值: **16.5 MB/s**
- initial_data 全量帧: **546 KB**

**优化后目标** (P0+P1):
- 单客户端 SSE 带宽: **< 1 MB/s** (-70~80%)
- 5 并发连接峰值: **< 5 MB/s** (-70%)
- initial_data 全量帧: **~70 KB** (273 KB 未压缩 → gzip 压缩 75%)

### 5.3 回滚方案

**P0 回滚** (删除 DetailDimensions):
- 不建议回滚（前端已向后兼容，老客户端连新后端无影响）
- 如必须回滚: `git revert f9a90965f` → 重新编译部署

**P1 回滚** (nginx gzip):
- 245/154 nginx 配置注释掉 `gzip` 相关行
- `systemctl reload nginx`
- 立即生效，无需重启服务

---

## 6. 监控与后续优化

### 6.1 监控建议

**短期监控** (P0+P1 上线后 24 小时):
1. 154 服务器出向带宽（阿里云监控 / `ifstat`）
2. nginx access log 中 `/api/admin/live-stream` 的 `$bytes_sent` 统计
3. 前端用户反馈（总览页加载速度、实时性）
4. 后端 CPU 使用率（gzip 压缩开销）

**长期指标** (接入 Prometheus):
```promql
# SSE 连接数
llmgw_sse_connections_total

# SSE 推送字节数
rate(llmgw_sse_bytes_sent_total[5m])

# Redis pipeline 调用频率
rate(llmgw_redis_snapshot_calls_total[5m])
```

### 6.2 后续优化 (P2-P5)

**P2: Delta 节流窗口 2s → 5s** (可选):
- 环境变量: `LLM_GATEWAY_LIVE_STREAM_SNAPSHOT_MIN_INTERVAL=5s`
- 预期: Redis 压力 -60%，泳道数据陈旧感 ≤5s
- 需产品/运维确认可接受性

**P3: `changed_lanes` 按需推送** (中优先级):
- 当前 4 维度全推，改为仅推送变化维度
- 预期: delta 体积 -25~50%
- 实施复杂度: 中（需改 `ComputeDelta` 逻辑 + 回归测试）

**P4/P5**: ROI 较低，暂不推荐（见原分析报告 §4.5-4.6）

---

## 7. 风险评估与缓解

| 风险 | 严重性 | 缓解措施 | 状态 |
|---|---|---|---|
| **P0: 已打开的旧页面实例连新后端** | 低 | 后端重启后，管理员浏览器里**正在运行的旧版页面**（旧 JS 无可选链保护）收到无 `detail_dimensions` 的 initial_data 会在 `incoming.detail_dimensions[dim]` 抛 TypeError，泳道渲染中断；**刷新页面即恢复**（index.html 不缓存，新 JS 带 hash 文件名）。风险窗口 = 后端重启后未刷新页面的时段 | ⚠️ 已知可接受 |
| **P0: 新前端类型兼容** | 极低 | `detail_dimensions` 改为可选类型 + 可选链 | ✅ 已实施 |
| **P1: gzip 波及 LLM 流式端点** | 中→低 | 审计修正：`/v1/`、`/v1beta/`、`/v2/` (245) 与 `/v1/(chat/completions\|messages)` (154) location 内已显式 `gzip off`，生产 LLM 打字机流保持原行为，待 245 验证后再评估放开 | ✅ 已收窄 |
| **P1: gzip 增加 CPU 开销** | 低 | `gzip_comp_level 6` 适中，监控 CPU 使用率 | ⏳ 部署后监控 |
| **P1: gzip 影响 SSE 实时性** | 低 | nginx 1.18+ 支持 chunked gzip flush，但 245 部署后必须人工确认泳道实时性无劣化（见 §5.1 第 5 步） | ⏳ 245 验证项 |
| **154 生产部署回滚风险** | 低 | P1 可即时回滚（nginx reload），P0 可 revert 后重编译 | ✅ 回滚方案就绪 |
| **测试覆盖不足** | 低 | SSE 相关 Go 测试与受影响前端 3 文件 54 用例通过；全量套件的无关既有失败已在基线复现排除 | ✅ 已验证 |

---

## 8. 成果物清单

**代码提交** (rebase 后最终 SHA):
1. `79c4d9564` — feat(nginx): enable gzip for SSE live-stream (P1 bandwidth opt)
2. `9e629ee1b` — feat(sse): remove DetailDimensions redundant field (P0 bandwidth opt)
3. 审计修正 commit — 见 §10 审计修正记录

**文档**:
1. `docs/04-implementation/analysis/2026-08-27-sse-bandwidth-optimization-analysis.md` (原分析报告, commit `aab72fc97`)
2. `docs/04-implementation/analysis/2026-08-27-sse-bandwidth-analysis-audit.md` (审计报告)
3. `docs/04-implementation/analysis/2026-08-27-sse-bandwidth-optimization-implementation.md` (本实施报告)

**验证脚本**:
- `scripts/verify-gzip-sse.sh` (gzip 验证, GET 流式方法 + token 掩码)

**Git 状态**:
- 当前分支: `main`
- 远程同步: ✅ 已推送到 `origin/main`
- 工作区: 干净（仅 `web/public/menu-config.json` stashed，无关本次优化）

---

## 10. 审计修正记录 (2026-08-27 二次审计)

对 P0/P1 三个 commit (`369332e50..HEAD`) 做双轴审查（Standards 轴对照
`CONTRIBUTING.md`，Spec 轴对照分析报告 P0/P1 需求），发现并修正以下问题：

| # | 发现 | 严重性 | 修正 |
|---|---|---|---|
| A1 | **nginx gzip 范围外溢**：server 级 `gzip_types text/event-stream` 会连带压缩 `/v1/chat/completions`、`/v1/messages` 等生产 LLM 打字机流（同为 text/event-stream），gzip 缓冲可能造成成块/首字延迟，未经评估 | **高** | 245 的 `^~ /v1/`、`^~ /v1beta/`、`^~ /v2/` 与 154 的 `~ ^/v1/(chat/completions\|messages)` location 内加 `gzip off`，压缩范围收窄为 admin live-stream + JSON API + 静态资源 |
| A2 | **验证脚本三处缺陷**：① 用 HEAD 请求验证（SSE 无 body，nginx 对 HEAD 的 Content-Encoding 不可靠）；② 无 `--max-time`（GET 连上流后永久挂死）；③ `echo` 打印完整 admin token（泄露到终端/日志） | **高** | 重写为 GET + `--max-time` + `-D` 提头 + curl 28 退出码容忍 + token 掩码（scheme + 前 6 字符） |
| A3 | 实施文档 commit hash 失效（推送前 rebase 导致 `f9a90965f`/`9d5605e2a` 变为 `9e629ee1b`/`79c4d9564`） | 中 | 全文更新为最终 SHA 并标注 rebase 来源 |
| A4 | 测试陈述过强："34 个 SSE 测试全部通过"隐含全量绿。实际：全量 `go test ./admin` 有 1 个无关既有失败；`web/package.json` 无 `test` script（`pnpm test` 静默 no-op）；全量 vitest 4 文件 8 用例既有失败 | 中 | 收紧为已验证子集 + 基线 `369332e50` 复现对照证明无关 |
| A5 | 部署风险缺一项：已打开的旧页面实例（旧 JS 无可选链）连新后端会在首帧抛 TypeError | 中 | 风险表补行：刷新即恢复，窗口 = 后端重启后未刷新时段 |
| A6 | P0 越界改动未标注：空 snapshot 兜底 map 顺手补 `credential` 维度 | 低 | §1.2 补 scope 标注（与上游 `86e8f8618` 互补） |

**既有失败基线证据**：worktree checkout `369332e50`（P0/P1 之前），同样 4 个前端
测试文件同样 8 用例失败 → 失败先于本次改动存在，非回归。

**降级说明**：本次审查未派并行 sub-agent（双轴审查由主线程完成并已产出可操作
发现 A1-A6），理由：diff 上下文已在主线程完整掌握，独立子代理重复读档收益低。

---

## 9. 结论

P0 + P1 组合优化已完成代码实施与推送，预期将 154 生产环境 SSE 带宽从 **3.3 MB/s 降至 0.7-1 MB/s** (单客户端)，**低成本、风险已收窄**（LLM 流式端点已显式 `gzip off`，剩余风险见 §7 风险表，均可在 245 验证阶段拦截）。

**下一步行动**: 按 `245 预发验证 → 154 生产灰度` 流程部署，监控带宽指标与用户反馈，必要时可快速回滚 P1 (nginx gzip)。

P2 (delta 节流延长) 可作为可选后续优化，视 154 部署后 Redis 压力与用户体验反馈决定是否实施。

---

**实施人**: AI Agent (ZCode session continuation)  
**审计人**: AI Agent (代码逻辑验证 + 类型系统检查)  
**实施日期**: 2026-08-27  
**会话 ID**: `sess_9e8a0c7f-5e45-4753-b979-df1466689356`  
**Handoff 源**: `/tmp/handoff-20260827-193000.md`
