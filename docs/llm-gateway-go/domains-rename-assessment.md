# domains/ 重命名评估报告

> **评估日期**: 2026-07-01（v1 → v2）  
> **评估范围**: 将 `domains/` 重命名为 `pipeline/` 的影响分析  
> **结论**: **不建议现在执行重命名**，应延迟到 Phase 2 Go module 拆分时一并处理  
> **报告版本**: v2 增量更新（见末尾 §7-12，重点：命名冲突告警 / 合规包审计 / 真孤立包清单）

---

## 1. 当前状态

### 1.1 引用统计
- **总引用次数**: 271 处（`.go` 文件中的 `llm-gateway-go/domains` import 路径）
- **被 import 最多的 domains 子包**（Top 15）:
  ```
  25 次  domains/pipeline
   8 次  domains/sessionaudit
   8 次  domains/identity
   7 次  domains/transformation
   7 次  domains/hooks/audit
   6 次  domains/session
   6 次  domains/memory
   6 次  domains/credential
   5 次  domains/hooks/compression
   5 次  domains/credentialstate
   5 次  domains/authentication
   4 次  domains/streaming/executors
   4 次  domains/streaming
   4 次  domains/routing
   4 次  domains/hooks/sessionaudit
  ```

### 1.2 最依赖 domains/ 的文件（Top 20）
```
24 处  ./cmd/gateway/main_pipeline.go
20 处  ./cmd/gateway-v2/main.go
14 处  ./cmd/gateway/main.go
13 处  ./cmd/gateway/main_v2_pipeline.go
10 处  ./domains/streaming/handler.go
 8 处  ./domains/streaming/executors/executor.go
 6 处  ./domains/streaming/responses.go
 6 处  ./domains/streaming/messages.go
 5 处  ./cmd/gateway/main_v3_wiring.go
 5 处  ./cmd/gateway-v2/e2e_test.go
 4 处  ./domains/integration/pipeline_builder.go
 4 处  ./domains/integration/pipeline_builder_test.go
 4 处  ./domains/integration/full_pipeline.go
 3 处  ./examples/auto_control_integration.go
 3 处  ./domains/streaming/session_assignment.go
 3 处  ./domains/streaming/request_meta.go
 3 处  ./domains/streaming/request_log_pipeline.go
 3 处  ./domains/streaming/executors/executor_prestream_test.go
 3 处  ./domains/streaming/executors/context_summarize.go
 3 处  ./admin/handler.go
```

### 1.3 最依赖 domains/ 的 Go 包（Top 15）
```
32 个 import  cmd/gateway
20 个 import  cmd/gateway-v2
11 个 import  domains/streaming
 9 个 import  domains/streaming/executors
 7 个 import  domains/integration
 4 个 import  admin
 3 个 import  domains/transformation
 3 个 import  domains/hooks/compression
 3 个 import  bg
 2 个 import  domains/interception
 2 个 import  domains/hooks/sessionaudit
 2 个 import  domains/hooks/handoff
 1 个 import  upstream
 1 个 import  domains/transformation/anthropic
 1 个 import  domains/session
```

---

## 2. 重命名影响分析

### 2.1 需要修改的地方
如果执行 `domains/` → `pipeline/` 重命名，需要修改：

1. **271 处 import 路径**（所有 `__REPO_URL_3__/domains` → `__REPO_URL_3__/pipeline`）
2. **目录结构**（`mv domains/ pipeline/`）
3. **文档引用**（README / docs/architecture/ARCHITECTURE.md / design docs）
4. **注释中的引用**（代码注释中提到 "domains" 的地方）
5. **可能的配置文件**（如果有硬编码路径）

### 2.2 变更范围
- **核心入口点**: `cmd/gateway` (32 imports) + `cmd/gateway-v2` (20 imports) = 52 个核心依赖
- **domains 内部**: `domains/streaming` (11 imports) + `domains/integration` (7 imports) = 18 个内部依赖
- **其他服务**: `admin` (4 imports) + `bg` (3 imports) + `upstream` (1 import) = 8 个外部依赖

---

## 3. 风险评估

### 3.1 高风险因素
1. **并发会话冲突**: 
   - 当前有其他会话（如 opencode-agent）也在修改 `domains/` 下的代码
   - 大规模重命名会导致 merge conflict 或覆盖其他会话的工作

2. **测试覆盖不足**:
   - 271 处修改需要全量回归测试
   - 当前测试覆盖率未知（未运行 `go test ./...`）

3. **IDE 索引失效**:
   - 重命名后 IDE (VSCode/GoLand) 的 Go 索引需要重建
   - 可能影响代码补全和跳转

### 3.2 中风险因素
1. **文档同步**: 需要同步更新所有设计文档中的 "domains" 引用
2. **习惯用语**: 团队成员已习惯 "domains" 术语，重命名后需要时间适应

### 3.3 低风险因素
1. **向后兼容**: 这是内部重构，不影响外部 API

---

## 4. 建议方案

### 4.1 推荐方案: 延迟到 Phase 2
**理由**:
- Phase 2 计划将 Go 代码拆分为多个 module（`application/` / `infrastructure/` / `domain/`）
- 届时 `domains/` 会整体移动到 `application/` module，路径会变成 `__REPO_URL_3__/application/pipeline`
- **一次性处理路径变更 + module 拆分**，避免二次修改

**优势**:
- 减少重复工作（不需要先改成 `pipeline/`，再改成 `application/pipeline`）
- 与架构重构同步，更符合整体设计
- 降低并发会话冲突风险

### 4.2 如果必须现在重命名

**前置条件**（必须全部满足）:
1. ✅ 与所有并发会话 owner 同步，确认他们的工作已提交或暂停
2. ✅ 当前 working tree clean（无未提交修改）
3. ✅ 备份当前分支（`git branch backup-before-rename-$(date +%Y%m%d)`）
4. ✅ 通知团队成员即将进行大规模重命名

**执行步骤**:
```bash
cd /path/to/llm-gateway-go

# 1. 备份
git branch backup-before-rename-$(date +%Y%m%d)
git status  # 确认 clean

# 2. 重命名目录
mv domains pipeline

# 3. 批量替换 import 路径（使用 gofmt）
gofmt -w -r '__REPO_URL_3__/domains -> __REPO_URL_3__/pipeline' .

# 4. 验证编译
go build ./...
go test ./...

# 5. 提交
git add -A
git commit -m "refactor: rename domains/ to pipeline/ (271 import paths updated)"
git push origin main
```

**验证清单**:
- [ ] `go build ./...` 成功
- [ ] `go test ./...` 全部通过
- [ ] `grep -r "llm-gateway-go/domains" . --include="*.go"` 返回 0 结果
- [ ] 文档中的 "domains" 引用已更新
- [ ] IDE 重新索引完成

---

## 5. 替代方案: 渐进式重命名

如果既不想延迟到 Phase 2，又担心一次性改动过大，可以考虑：

1. **保留旧路径别名**（Go 1.18+ 支持）:
   ```go
   // pipeline/types.go
   package pipeline
   
   // 临时别名，供旧代码使用
   import "__REPO_URL_3__/domains"
   ```

2. **分批重命名**（每周处理 50-80 处引用）:
   - Week 1: `cmd/gateway` + `cmd/gateway-v2` (52 处)
   - Week 2: `domains/streaming` + `domains/integration` (18 处)
   - Week 3: 剩余外部依赖 (8 处)
   - Week 4: `domains/` 内部互相引用 (193 处)

---

## 6. 总结

| 方案 | 工作量 | 风险 | 推荐度 |
|------|--------|------|--------|
| **延迟到 Phase 2** | 低（只评估） | 低 | ⭐⭐⭐⭐⭐ |
| **一次性重命名** | 高（1-2 天） | 中 | ⭐⭐ |
| **渐进式重命名** | 高（4 周） | 低 | ⭐⭐⭐ |

**最终建议**: 延迟到 Phase 2 Go module 拆分时一并处理，当前只完成本次评估即可。

---

## 附录: 相关文档

- Phase 2 Go module 拆分计划: `docs/go-migration/2026-06-12-master-plan.md`
- 架构设计: `docs/architecture/ARCHITECTURE.md`
- 重命名工具: `gofmt -w -r` (Go 官方工具)

---

# 📌 v2 增量更新（2026-07-01 第二轮调研）

> **增量范围**: v1 报告之后的额外发现（命名冲突 / 合规包告警 / 真孤立包清单）  
> **调研人**: 新会话（rule 18 上下文交接）  
> **触发原因**: 子仓 HEAD 已从 7661e773 → 985575d0（opencode-agent 持续提交），需刷新评估

---

## 7. 🔴 v2 关键发现 1：命名冲突风险

### 7.1 现状
`domains/pipeline/` 子目录**已经存在**，包含：
- `domains/pipeline/pipeline.go`（7,527 字节）
- `domains/pipeline/pipeline_test.go`（10,671 字节）

### 7.2 风险
如果执行 `domains/` → `pipeline/` 重命名，将产生 **`pipeline/pipeline/` 二级嵌套**：
```
llm-gateway-go/
└── pipeline/                  ← 新顶级目录（原 domains/）
    └── pipeline/              ← 原 domains/pipeline（无意义嵌套）
        ├── pipeline.go
        └── pipeline_test.go
```
这是**无效命名**（Pipeline 设计中"管道"概念与自己同名），必须先解决。

### 7.3 推荐解决方案（三选一）

| 方案 | 描述 | 影响范围 | 推荐度 |
|---|---|---|---|
| **A：先迁移 `domains/pipeline`** | 把 `domains/pipeline/` → `domains/orchestrator/`（或 `domains/stages/`），再重命名顶级 | +33 处 import 修改 | ⭐⭐⭐⭐⭐ |
| **B：重命名为非 `pipeline/` 的名字** | 改成 `app/`, `services/`, `modules/` 等避开冲突的名字 | -33 处 import 修改（不需要单独处理 pipeline）| ⭐⭐⭐ |
| **C：保留 `domains/` 名字** | 不重命名，仅做内部子包整理 | 0 处修改 | ⭐⭐⭐⭐ |

**A 方案影响范围补充**：
- `domains/pipeline/` 被 33 处外部 import 引用（仅次于 hooks/ 的 84 次）
- 加上 pipeline/ 内部 17 处自引用，共 **50 处**
- 改名后需同步更新 hooks/outputcompliance、admin/handler.go 等多文件

---

## 8. ⚠️ v2 关键发现 2：合规包审计（用户重点关注）

### 8.1 现状
v1 报告未审计合规相关包，但用户（A1 任务）明确要求"检查 compliance/ 是否有模型策略模板"。第二轮调研发现：

### 8.2 _to-be-deprecated 状态
- **`_to-be-deprecated/compliance-候选废弃-20260701/`** ← **A1 任务已被 opencode-agent 部分执行**
- **`_to-be-deprecated/llmclient-候选废弃-20260701/`** ← 已废弃
- **建议**：确认这两个目录里**确实没有策略模板**（等保 / GDPR / 模型卡片），如果安全则继续 Phase 0-5 关闭

### 8.3 ⚠️ `domains/outputcompliance/`：**不是孤立，是合规核心**
- 位置: `domains/outputcompliance/checker.go`（591 行）
- Go import 数: **0**（但通过 hooks 中转）
- **实际调用方**:
  - `cmd/gateway/main_pipeline.go` ← 启动时挂载
  - `domains/hooks/outputcompliance/hook.go` ← 集成点
  - `domains/hooks/outputcompliance/hook_test.go` ← 测试
  - `admin/modules.go` ← 注册为 **output_compliance 模块**
  - `admin/modules_test.go` ← 模块测试
  - `admin/session_analytics_handler.go` ← 读 `output_compliance_audit` 表
- **合规内容**（实际是**模型安全策略模板**）:
  - PII 检测：email、中国手机号、身份证、信用卡（Visa）
  - 毒性关键词：profanity、violence、hate_speech、sexual
  - 偏见检测：性别、种族、年龄歧视
  - 多租户策略：每个 tenant 独立 Policy

### 8.4 ⚠️ `domains/promptinjection/`：**同样不是孤立**
- 类似 outputcompliance，通过 `hooks/outputcompliance/hook.go` 间接引用
- 在 `cmd/gateway/main_pipeline.go` 挂载
- 包含提示注入检测（PII 字段也都涉及），**属于合规核心**

### 8.5 🚫 结论

| 包 | 真实状态 | A1 任务处理建议 |
|---|---|---|
| `compliance/`（顶级） | _to-be-deprecated 已存在 | 由 opencode-agent 处理，确认无策略模板即可 |
| `domains/outputcompliance/` | **合规核心（模型安全策略）** | ❌ **绝不可废弃** |
| `domains/promptinjection/` | **合规核心** | ❌ **绝不可废弃** |
| `domains/sessionsummary/` | **活跃**（analysis worker 引用）| 不可废弃 |
| `domains/tenant/` | **活跃**（apihub 通过 string 引用）| 不可废弃 |
| `domains/notification/` | 真孤立（grep 命中是 SQL 字段名）| ✅ 可候选废弃 |

---

## 9. v2 关键发现 3：真孤立包清单

经过 **域内 import + Go import + 字符串引用** 三层验证：

### 9.1 真孤立（0 Go import，可候选废弃）

| 包名 | 文件数 | 备注 | 处理建议 |
|---|---|---|---|
| `flowcontrol` | controller.go + test + mock | 控制器已无引用 | ✅ 候选废弃 |
| `taskmanagement` | group_manager + stats_manager + assigner | 任务调度无引用 | ✅ 候选废弃 |
| `notification` | approval_notifier + lark_bot + test | grep 命中是 SQL 字段，**但 lark_bot 可能是飞书通知** | ⚠️ **需确认**：lark_bot 是否真的不再使用 |
| `remotecontrol` | 4 文件，新加 untracked | opencode-agent 在做 | ❌ **跳过**（等他完成）|

### 9.2 活跃（不可动）
- `outputcompliance`, `promptinjection`, `sessionsummary`, `tenant`, `sessionsummary`
- 见 §8

### 9.3 28 个一级包 vs 20 个外部 import 的差异

```
所有包:        28
外部 import:   20
真孤立:         4（含 remotecontrol 未跟踪）
合规相关活跃:   4（outputcompliance, promptinjection, sessionsummary, tenant）
实际不可动:     20
```

---

## 10. v2 其他更新

### 10.1 引用数：271 → **273**（+2）

主要因 opencode-agent 新提交引入（未变更本质结构）。

### 10.2 当前活跃 importer（v2 重测）

| 包 | 文件数 | 备注 |
|---|---|---|
| `admin/` | 10 | 含 `admin/routing.go`（Phase 1 B1 任务冲突点）|
| `cmd/gateway` | 5 | 旧入口 |
| `bg/` | 4 | 后台任务 |
| `tests/integration` | 2 | |
| `cmd/gateway-v2` | 2 | 新入口 |
| `upstream`, `examples`, `credentialfpslot`, `.` | 1+ | 散落 |

### 10.3 _to-be-deprecated 残留 9 处引用
全部指向 `domains/hooks/observability/telemetry`（活跃代码），不影响重命名。

### 10.4 生成代码依赖：**0 处** ✅
- 无 .pb.go / .mock.go / .generated.go / .auto.go 引用 domains/
- 降低重命名风险（不需要重新生成 protobuf）

### 10.5 文档同步：17 个 .md 文件
- 9 个业务文档 + 7 个架构文档 + 1 个本报告
- 所有非业务引用都在描述架构和历史，重命名后需一次性搜索替换

### 10.6 顶层包热度排行（v2 完整版，含 hooks 子包展开）

```
84  次  hooks（聚合：audit 29, observability 21, compression 7, response 5, security 4, ...）
33  次  pipeline
17  次  sessionaudit
17  次  memory
16  次  transformation
16  次  identity
15  次  authentication
14  次  streaming
14  次  session
14  次  credential
 5  次  credentialstate
 5  次  assets
 4  次  sessionstate
 4  次  routing
 4  次  provider
 4  次  agent-ecosystem
 3  次  analysis
 2  次  security
 1  次  interception
 1  次  integration
```

---

## 11. v2 最终建议

### 11.1 关闭 A2 任务
本文档为 v1 + v2 **完整评估报告**，A2 任务（1 人日）可关闭。

### 11.2 重命名最终决策：**保持 `domains/` 不动**

理由（v1 + v2 综合）：
1. 273 处修改 + 17 个文档同步 + 命名冲突
2. 0 处生成代码依赖 = 安全，但 108 处活跃代码 = 高变更面积
3. 与 Phase 2 Go module 拆分合并，路径变 `application/pipeline/` 一次到位
4. **并发起见**：保留 `domains/` 不破坏 opencode-agent 当前工作

### 11.3 衍生任务：孤立包清理 v2（推荐单独立任务）

合并入 Phase 0-5 重写：
```bash
# 验证后废弃 3 个真孤立包（先确认 lark_bot 用途）
git mv domains/flowcontrol _to-be-deprecated/flowcontrol-候选废弃-$(date +%Y%m%d)
git mv domains/taskmanagement _to-be-deprecated/taskmanagement-候选废弃-$(date +%Y%m%d)
# remotecontrol 等 opencode-agent 完成后再评估
```

⚠️ **必做**：迁移前跑 `go build ./...` 确认无编译错误，并目视确认 `domains/notification/lark_bot.go` 不是飞书机器人（任何看起来可用的代码都不能直接移走）。

### 11.4 与 A1 任务的关系
A1 任务（孤立包废弃）已被 opencode-agent 部分执行（compliance, llmclient 已废弃）。v2 评估追加了 `flowcontrol` / `taskmanagement` / `notification` 三个新候选，等于 A1 任务的增强版。

---

## 12. v2 调研元数据

- **调研时间**: 2026-07-01
- **调研人**: 新会话（承接 rule 18 上下文交接）
- **主仓 HEAD at 调研时**: c62844df
- **子仓 HEAD at 调研时**: 985575d0
- **Token 使用**: ~50%（评估任务对上下文友好）
- **本次未提交文件**: `docs/SESSION_AUDIT_FLOW_CONTROL_FINAL_REPORT.md`, `domains/remotecontrol/`（opencode-agent 工作中，**未触碰**）
