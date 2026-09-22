# 域目录结构模板（拷给子代理）

> **用法**：每个新域的子代理收到本模板 + `docs/audit/playbook/domains/DXX-name.md` 末尾的子代理派发提示词。
> **目标产出**：在 `tests/48h-audit/DXX-name/` 下填齐 plan.md + 4 类测试 + 报告。

---

## 目录结构

```
DXX-name/
├── plan.md                        # 必填：审计要点 + 测试分类 + 验收门
├── business/                      # 业务测试：API 行为、契约
│   ├── README.md                  # 列出本类全部测试点
│   └── test_*.go                  # Go 测试文件
├── data/                          # 数据测试：IR 字段、序列化、缓存一致
│   ├── README.md
│   └── test_*.go
├── stress/                        # 压力测试：吞吐、并发、长会话
│   ├── README.md
│   └── test_*.go                  # + 可选 bench_*.go
├── safety/                        # 安全测试：锁、泄漏、异常路径
│   ├── README.md
│   └── test_*.go                  # + chaos_*.go 强制异常
├── scripts/                       # 驱动脚本
│   ├── run.sh                     # 本域一键跑
│   └── (可选) setup-mock.sh / cleanup.sh
└── reports/
    ├── latest.md                  # 本轮 48h 审计结论
    ├── INDEX.md                   # 本域历史报告索引
    └── history/
        └── <RNN>-<YYYY-MM-DD>.md  # 历史轮次（按 git 时间倒序）
```

---

## plan.md 模板

```markdown
# DXX — <域中文名>

> 域知识库：docs/audit/playbook/domains/DXX-*.md  
> 48h 改动面（截至 R<N>）：<commit 列表 / 文件清单>  
> 状态：草稿 / 进行中 / 已闭环

## 1. 审计要点（来自 playbook 域文档）

- 要点 1：...
- 要点 2：...
- 要点 3：...

## 2. 业务测试（business/）

- [ ] B-01：<测试名> —— <断言>
- [ ] B-02：...

## 3. 数据测试（data/）

- [ ] D-01：<字段序列化 golden>
- [ ] D-02：<缓存一致性>

## 4. 压力测试（stress/）

- [ ] S-01：<吞吐 ≥ X req/s>
- [ ] S-02：<P95 ≤ Y ms @ Z 并发>

## 5. 安全测试（safety/）

- [ ] SF-01：<-race 无数据竞争>
- [ ] SF-02：<取消/超时/EOF 无泄漏>

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race ./tests/48h-audit/DXX-*/... -timeout 60s
bash tests/48h-audit/DXX-*/scripts/run.sh
```

## 7. 与方案文档的对齐

- RFC: docs/.../RFC-XXX.md
- 上轮挂账: docs/audit/playbook/runs/R<N-1>/agent-DXX.md §遗留
```

---

## reports/latest.md 模板

```markdown
# R<N> · DXX <域名> · 48h 审计结论

> 时间：YYYY-MM-DD  
> 改动面：N commits / N files  
> 复核：主代理亲读全部 file:line

## 1. 发现与处置

| 级别 | 描述 | commit | 钉桩测试 |
|---|---|---|---|
| P0 | ... | sha | business/test_x.go |
| P1 | ... | ... | ... |

## 2. 核实为健康的关键面

- ✓ ...

## 3. 遗留登记

- [ ] ...

## 4. 测试与验证

```
$ bash scripts/run.sh
<输出>
```

## 5. 下一轮提示

- 关注点 1
- 关注点 2
```

---

## 测试文件规范（Go）

- 包名：`package dxx_test`（黑盒）或 `package dxx`（白盒，慎用）
- 文件名：`test_<aspect>.go` 或 `test_<issue>.go`
- 命名：`TestXxx_<Behavior>`
- 跑测：`go test -race -timeout 60s ./tests/48h-audit/DXX-name/...`
- 共享 util：`tests/testutil/`（如已有）

---

## scripts/run.sh 模板

```bash
#!/usr/bin/env bash
# DXX-name/scripts/run.sh
# 一键跑本域 4 类测试
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOMAIN="$(basename "$(dirname "$(dirname "$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || echo "${BASH_SOURCE[0]}")")")")"
echo "[run] domain=$DOMAIN"
cd "$DIR"
go test -race -timeout 60s ./tests/48h-audit/$DOMAIN/business/... || exit 1
go test -race -timeout 60s ./tests/48h-audit/$DOMAIN/data/...     || exit 1
go test -race -timeout 120s ./tests/48h-audit/$DOMAIN/stress/...   || exit 1
go test -race -timeout 60s ./tests/48h-audit/$DOMAIN/safety/...   || exit 1
echo "[run] all PASS"
```

---

**注意**：本模板不是死规矩。如果某个域只有 1-2 类适用（比如纯压力域），允许空目录 + 在 plan.md 写明"无 X 类（理由：...）"。