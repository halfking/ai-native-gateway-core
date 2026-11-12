# 提示词优化模块 —— 测试验证记录

**日期**：2026-11-11
**范围**：`internal/prompt-optimizer/` + `cmd/prompt-optimizer/` + Docker 环境
**结论**：单元测试 7 包全绿 · 变异验证 14/14 臂被抓住 · Docker 端到端 31/31 断言通过

---

## 一、验证前发现的阻塞问题

初版代码是"写完即交付"，从未编译过。第一轮 `go build` 就塌了。

### 1.1 编译期（4 类）

| # | 问题 | 后果 | 处置 |
|---|------|------|------|
| C1 | 全部 import 路径写成 `github.com/your-org/llm-gateway` | 真实模块名是 `github.com/kaixuan/llm-gateway-go` ⇒ 8 个文件全部无法编译 | 批量改写 import |
| C2 | `cmd/prompt-optimizer/main.go` 用了 `gin-gonic/gin` | 该依赖**不在 go.mod 也不在 vendor/** ⇒ 本仓 vendor 模式下无法构建 | 重写为标准库 `http.ServeMux`（仓内 `cmd/quality-service` 同款范式）|
| C3 | `evaluation/evaluator.go` 的 `len(expected)*0.5` | int 与无类型浮点常量相乘被截断为 0 ⇒ **该指标恒返回 1.0** | 见 §二·D1 |
| C4 | `examples/basic.go` 缺 `strings` 导入，且是 `package main` | 无法编译；且不能被测试调用 | 改成普通库包 `package examples`，导出 `RunBasic/RunEvaluation/RunBatch` |

### 1.2 运行期与逻辑期（6 类）

| # | 问题 | 后果 | 处置 |
|---|------|------|------|
| D1 | 完整性指标恒真 | **"缺覆盖的全绿"** —— 指标永远满分，等于没有这个指标 | 改浮点运算 + 补钉测 |
| D2 | `optimizeWithEvaluation` 直接调 `o.evaluator.Evaluate` | evaluator 为 nil 时 **运行期 panic** | 加 nil 检查，明确报错 |
| D3 | `improvement = (opt-base)/base*100` | base=0 时得 NaN/Inf ⇒ **JSON 序列化直接失败**（整个响应 500）| 加除零保护 + 补 `absolute_delta` |
| D4 | `MetricScores` 永远是空 map | 接口承诺了但没实现 | 评估器补 `MetricBreakdown` |
| D5 | `getTestCases` 硬编码返回空切片 | **迭代优化策略是死代码**，从未优化过任何提示词 | 从 `StrategyContext.TestCases` 取值，由 `OptimizeOptions` 透传 |
| D6 | `analyzeIntent` 是空 TODO | 提示词正文完全没被利用，任务类型恒为空 | 实现关键词启发式（中英双语）+ 显式约束抽取 |

### 1.3 文档与实现不符（5 类）

初版 README 承诺但**从未落地**的文件：`ensemble.go` / `dynamic.go` /
`comparator.go` / `validator.go` / `examples/advanced.go`。
文档按"已实现"写 ⇒ 读者会去找不存在的文件。

**处置**：README 重写为按实际代码校正，并显式标注"未实现"。

### 1.4 Docker 环境（5 类）

初版 `docker-compose.prompt-optimizer.yml` + `Dockerfile.prompt-optimizer`
**`docker compose up` 必然失败**：

| # | 问题 | 后果 |
|---|------|------|
| E1 | `image: memora:latest` | Docker Hub **无此镜像** ⇒ pull 失败 |
| E2 | 引用 `./scripts/init-db.sql` | 文件不存在 |
| E3 | 引用 `./Dockerfile.test` | 文件不存在 ⇒ build 失败 |
| E4 | 引用 `./wait-for-it.sh` + `npm test` | 文件不存在，且这是 Go 仓 |
| E5 | Dockerfile 用 `golang:1.21-alpine` | 仓内 go.mod 是 **1.27.2** ⇒ 编译器过旧直接报错 |

**处置**：
- memora 改为 **nginx 健康桩**（沿用仓内 `docker-compose.dev-research.yml` 的成熟做法）；
- 桩额外模拟 memora 的 `/api/search` JSON 契约（否则 client 解析非 JSON 会报错）；
- 删掉三个不存在的文件引用；
- Go 版本对齐 1.27；
- 去掉与 vendor 模式冲突的 `go mod download`；
- 大体积历史二进制用 **Dockerfile 专属 ignore** 排除（不动共享 `.dockerignore`）。

---

## 二、变异验证：证明测试真的有牙

绿色测试证明不了"测试有牙"。脚本把每个已修复的缺陷改回去，确认测试**会转红**。

**判定规则**（三条缺一不可）：
- 变异后仍全绿 ⇒ 哑门 ⇒ **失败**；
- 变异后编译失败 ⇒ 红的原因不是断言 ⇒ **不算证据**；
- 变异后测试红 ⇒ 抓住 ⇒ 通过。

### 首轮结果：10/12 臂通过，2 臂暴露"测试没牙"

| 臂 | 变异内容 | 首轮结果 | 根因 |
|----|---------|---------|------|
| ARM10 | 移除代码任务的专属约束 | ❌ **仍全绿** | 断言写的是 `strings.Contains(out, "最佳实践")`，而"最佳实践"**同时出现在角色定义里** ⇒ 删了约束，角色那句还在 ⇒ 断言照样满足 |
| ARM11 | 角色定义不再随任务类型变化 | ❌ **仍全绿** | 断言写的是 `strings.Contains(trans, "翻译")`，而测试传入的**原文本身含"翻译"** ⇒ 命中的是任务目标章节，不是角色 |

**两条都是同一个病根**：断言落在"标签词"上，而不是"具体措辞/具体结构"上。
修正：断言改为完整措辞（`代码必须遵循最佳实践和编码规范` /
`专业翻译专家`），且传入的原文刻意不含这些词。

### 次轮结果：12/12 → 加 JSON 契约臂后 14/14

新增 ARM13（JSON 字段名退回 Go 默认名）、ARM14（耗时退回纳秒整数）。

ARM14 又暴露一次同类问题：`TestJSONDurationIsReadable` 原本用
`strings.Contains(payload, "s\"")` 全文扫描 ⇒ 意外命中 `"iterations"`
（`iterations` 的 `s` 紧跟引号）⇒ 裸 `time.Duration` 的纳秒整数也能过。
修正为**解析出 `duration` 字段本身**再判断类型。

### 另一处 harness 缺陷（自查发现）

ARM14 首版变异只改字段类型 ⇒ **编译失败** ⇒ 按判定规则不算证据。
修正为同一文件内两处同改（字段 + 赋值），保证可编译。

---

## 三、端到端测试发现的两个问题

### 3.1 JSON 字段名不是文档承诺的 snake_case

`go build` 过得去，但**第一次 curl 就暴露**：响应里是
`{"Original":...,"Optimized":...,"AppliedStrategies":...}` ——
所有导出结构体都缺 `json` tag，`encoding/json` 退回 Go 字段名。

而文档（集成指南 / README）承诺的是 `"original"` / `"optimized"` /
`"applied_strategies"` ⇒ **消费方按文档写代码直接取不到字段**。

**处置**：给所有导出结构体补 snake_case tag；`Duration` 换成自定义类型
输出可读串（`"67.125µs"` 而非纳秒整数 `67125`）。
并补 `json_contract_test.go` 钉住契约 —— 这类问题只有机器能守住。

### 3.2 memora 桩与 client 契约不匹配

`use_memora=true` 的请求 500：
```
context: memory search failed: invalid character 'm' looking for beginning of value
```
桩返回纯文本 `memora-mock-ok`，而 `domains/memory/client` 期望 JSON。
查 `domains/memory/client/client.go:485-513` 拿到真实契约
（`{"query","results":[{id,content,tags,mem_cube_id,score}],"count"}`），
改桩返回合规 JSON。

### 3.3 对比端点在无 LLM 通道时完全不可用

`/api/v1/compare` 与 `/api/v1/optimize/evaluate` 都要真跑一次模型才能打分，
而服务没有 LLM 客户端 ⇒ 必然 500。

**处置（两处）**：
1. 新增 `internal/prompt-optimizer/llm`（OpenAI 兼容客户端）——
   目标里的"内部小型大模型"落在这一层：BaseURL 指向本仓网关即可，
   由网关路由到内部小模型，**不引入任何新依赖**。
2. 新增 `evaluation/prompt_quality.go`：**结构质量**评估（不需要 LLM），
   `/api/v1/compare` 默认走这个口径 ⇒ 开箱可用。
   两种口径都在响应里回显 `mode` 与 `interpretation`，避免消费方
   把"结构分"误读成"效果分"。

配套：新增 `cmd/prompt-optimizer-mockllm`（复用仓内 `internal/providers/mock`
的 OpenAI 协议实现）+ compose 里的 `mock-llm` 服务 ⇒ output 模式端到端可测。

### 3.4 测试脚本自身的 bash 坑

`log_error "$name —— 期望 HTTP $want，实际 $got"` ⇒ bash 把全角逗号并入
变量名 ⇒ `want: 未绑定的变量` ⇒ 脚本中途退出。
**处置**：变量引用一律 `${}` 定界。

### 3.5 compose `command` 覆盖 Dockerfile 的 CMD

`command: ["-listen", ":8099"]` ⇒ compose 的 command **替换**而非追加 CMD
⇒ 容器去执行 `-listen` ⇒ `executable file not found in $PATH`。
**处置**：`command: ["./mockllm", "-listen", ":8099"]`。

---

## 四、最终验证证据

### 4.1 单元测试

```
$ go test ./internal/prompt-optimizer/...
ok  github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer
ok  github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/context
ok  github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/evaluation
ok  github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/examples
ok  github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/harness
ok  github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/llm
ok  github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/strategies
```

`go vet ./internal/prompt-optimizer/... ./cmd/prompt-optimizer/...` 无输出（通过）。

### 4.2 变异验证

```
$ ./scripts/audit/prompt-optimizer-mutation-verify.sh
...
全部 14 条变异臂均被测试抓住 ✓
```

### 4.3 Docker 端到端

```
$ ./scripts/test-prompt-optimizer.sh all
镜像构建完成
服务健康检查通过
  ✓ 健康检查返回 healthy
  ✓ 基础优化返回 200 (HTTP 200)
  ✓ 返回优化后提示词 / 记录了应用策略 / 注入了 harness 要素
  ✓ 产出角色定义 / 产出安全规则
  ✓ 未开启 harness 时无 harness 章节
  ✓ memora 链路返回 200 (HTTP 200)
  ✓ 批量优化返回 3 条结果
  ✓ quality 对比返回 200 / 回显对比口径 / 回显判读口径 / 返回胜者
  ✓ 结构更完整的提示词得分更高 (0 → 0.8)
  ✓ output 对比返回 200 / 返回胜者
  ✓ 带评估优化返回 200 / 返回基线分 / 优化后分 / 绝对分差
  ✓ 空 prompt 返回 400 / 非法 JSON 返回 400 / 未知字段返回 400
  ✓ GET 打到 POST 端点返回 405
  ✓ output 模式缺测试用例返回 400 / 非法 mode 返回 400
  ✓ 50 次请求总耗时 663ms，平均 13ms/次
✓ 全部 31 项断言通过
```

---

## 五、本轮**没有**做的事（明确边界）

1. **没有**接入真实 memora 实例 —— 本地跑的是契约对齐的桩。
2. **没有**接入真实内部小模型 —— 跑的是仓内 mock（OpenAI 协议）。
3. **没有**做端到端**效果**实验（优化前后在真实任务上的正确率对比）。
   本模块现在的度量是"结构完备度"与"输出相似度"，**都不等于任务正确率**。
   要拿到"优化到底带来多少提升"的答案，必须接入真实流量 + 真实度量后重测。
4. **没有**实现 README 初稿承诺的 `ensemble` / `dynamic` 策略 ——
   已在 README 显式标注「未实现」，而不是留成文档与代码的偏差。
5. **没有**改仓库共享的 `.dockerignore` —— 用 Dockerfile 专属 ignore 避免影响其它镜像。
6. **没有**把记忆检索、评估这些能力接进网关主链路 —— 本轮只交付独立模块与服务。

## 六、复现方式

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go

go test ./internal/prompt-optimizer/...            # 单元测试
go vet ./internal/prompt-optimizer/... ./cmd/prompt-optimizer/...
./scripts/audit/prompt-optimizer-mutation-verify.sh # 变异验证
./scripts/test-prompt-optimizer.sh all              # Docker 端到端
./scripts/test-prompt-optimizer.sh stop             # 清理
```
