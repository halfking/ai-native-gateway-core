# 48h 审计 + 分类测试整合中心 · 总览报告（R56 入口）

> 生成时间：2026-09-22 23:51 CST  
> HEAD：`4484585a6 feat(docs,ir,paramreg,errorsx): 2026-09-21 审计落地剩余 31 份未跟踪产物`  
> 范围：48h 滚动窗口 + 全量审计域的整合测试中心

---

## 1. 这一轮做了什么

按用户"完善需求、制定详细执行方案、然后进行审计执行"的要求，分**两步走**：

### 1.1 整合测试中心（本次交付）

| 模块 | 文件 | 用途 |
|---|---|---|
| README | `tests/48h-audit/README.md` | 索引 + 与 `docs/audit/playbook/` 的关系图 |
| 主计划 | `tests/48h-audit/00-PLAN.md` | v2 版主代理提示词，整合审计 + 4 类测试 |
| 模板 | `tests/48h-audit/TEMPLATE-domain.md` | 域目录 + 测试文件 + 报告三套模板 |
| 脚本 | `tests/48h-audit/scripts/{run-all,aggregate-reports,new-domain}.sh` | 一键跑 / 聚合 / 新建域 |
| 17 个域目录 | `tests/48h-audit/DXX-*/` | 按 17 域 playbook 镜像结构 |
| 2 个示例域 | D01 + D02 | 含真实测试代码 + 真实审计发现 |
| 15 个占位域 | D03-D17 | plan.md + reports/latest.md 占位，待 worker 子代理填充 |
| 聚合索引 | `tests/48h-audit/reports/INDEX.md` | 全域状态一览（自动生成） |

### 1.2 域示例的实测产出

| 域 | 测试文件 | 类别 | 结果 |
|---|---|---|---|
| D01 IR 生命周期 | `business/ir_field_roundtrip_test.go` | business | 4/4 PASS |
| D01 IR 生命周期 | `data/ir_field_golden_test.go` | data | 5/5 PASS |
| D01 IR 生命周期 | `safety/ir_malformed_test.go` | safety | 18/18 PASS |
| D01 IR 生命周期 | `stress/serialize_p99_test.go` | stress | 1/1 PASS（5000 reqs @ conc=50，p99=580µs） |
| D02 协议适配 | `business/multi_role_roundtrip_test.go` | business | 9/9 PASS |
| D02 协议适配 | `safety/tool_choice_injection_test.go` | safety | 8/8 PASS |
| D02 协议适配 | （占位） | data / stress | 待 worker 填充 |

### 1.3 真实审计发现（已固化到 reports）

- **D01**：4 协议 IR roundtrip 守恒；Gemini body 不携带 model（URL-path 设计）已显式标注
- **D02**：发现 **OpenAI 协议 parse 时把 system role 提升为 IR.System 一等字段并从 Messages 数组移除**（这是有意设计，非 bug，已在测试中固化）；tool_choice 含 SQL 注入 / path traversal / XSS / NULL byte 形态不 panic、无敏感字段泄漏

---

## 2. 架构对照（与既有 docs/audit/playbook/）

```
docs/audit/playbook/         tests/48h-audit/
├── conventions.md            （沿用）
├── README.md                 （沿用）
├── orchestrator-prompt.md    → 00-PLAN.md（v2 增强）
├── domains/DXX-name.md       → DXX-name/plan.md（测试具象化）
├── runs/RNN-*/               → reports/latest.md + reports/history/
└── （无测试）                 → business/data/stress/safety/*_test.go
```

**对比 v1 增强点**：

1. 每个审计域都强制要求至少 1 个 business + 1 个 data + 1 个 stress 或 safety 测试
2. 测试代码与审计计划在同一目录，便于上下文切换
3. 报告在同目录累积（latest.md + history/<RNN>-<日期>.md）
4. 聚合脚本一键生成跨域索引

---

## 3. 当前未完成的事（明确登记）

| 项 | 状态 | 预计填补方 |
|---|---|---|
| D03-D17 测试代码 | 占位 | worker 子代理（按 TEMPLATE-domain.md） |
| D02 data / stress 测试 | 占位 | worker 子代理 |
| Codegraph 重建（如果工具可用） | 未做 | 主代理在第 2 步 |
| 真实 48h 改动面的逐域审计 | 本轮仅完成 D01/D02 | 主代理第 3-4 步 |
| 修复 + 钉桩（钉到 tests/48h-audit/） | 无新 P0-P3 | 主代理第 5 步 |
| 三门（build / vet / test） | build OK / vet OK / D01+D02 测试 OK | ✓ 已通过 |
| 提交与推送 | **未提交**（等你确认） | 主代理第 7 步 |

---

## 4. 怎么用本目录继续工作

### 4.1 跑测试

```bash
# 全量 17 域（占位域无 .go 文件，runner 自动跳过）
bash tests/48h-audit/scripts/run-all.sh

# 只跑 D01 + D02
bash tests/48h-audit/scripts/run-all.sh --domains=D01,D02

# 只跑 stress 类
bash tests/48h-audit/scripts/run-all.sh --categories=stress
```

### 4.2 看全量状态

```bash
bash tests/48h-audit/scripts/aggregate-reports.sh > tests/48h-audit/reports/INDEX.md
cat tests/48h-audit/reports/INDEX.md
```

### 4.3 新建域（D18+）

```bash
bash tests/48h-audit/scripts/new-domain.sh D18 --name=foo-bar --chinese="新审计域"
```

### 4.4 派 worker 子代理填占位域

```bash
# 例：派 worker 子代理填 D03
# 子代理收到提示词（拷自 DXX-name/plan.md 末尾）：
"你是 worker 子代理（带写权限到 tests/48h-audit/D03-three-tier-cache/）。
 知识库入口：docs/audit/playbook/domains/D03-three-tier-cache.md
 模板：tests/48h-audit/TEMPLATE-domain.md
 必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
 输出 ≤5KB。"
```

### 4.5 启动新会话做下一轮

```bash
# 拷贝主计划提示词到新会话
cat tests/48h-audit/00-PLAN.md
# 或拷贝本文件
cat tests/48h-audit/MASTER_REPORT.md
```

---

## 5. 已知遗留（不阻断发版）

- D03-D17 占位：占位 plan.md 没有具体测试代码，但**架构与模板完整**，worker 子代理可按模板填充
- D02 data + stress 测试缺失，但 D02 已通过 business + safety 暴露主要设计观察
- 聚合脚本的 sed 模式仅在 macOS / Linux GNU sed 下验证，BSD sed 需要 `-E` 标志（已加）

---

## 6. 下一步建议（主代理在 R56+ 接手时）

1. 拉远端 → 0 冲突确认
2. 用 codegraph 重建图谱（如工具可用）
3. 派 6-8 个 worker 子代理并行填 D03-D08（高优先级域）
4. 派 4-5 个 worker 子代理并行填 D09-D17
5. 主代理亲读复核 P0-P3，按 0-P3 顺序修复
6. 钉桩到 `tests/48h-audit/DXX-*/{business,data,stress,safety}/`
7. 跑 `bash tests/48h-audit/scripts/run-all.sh` 三门通过
8. 写 `docs/audit/2026-09-22-r56-48h-audit-round.md` 主文档
9. 提交并推送（commit message 沿用仓库惯例）

---

**版本**: R56 入口 · 2026-09-22 23:51 CST  
**关联**: `docs/audit/playbook/` (v1) + `tests/48h-audit/` (v2 整合)  
**下一轮提示词**: 见 `tests/48h-audit/00-PLAN.md` 末尾