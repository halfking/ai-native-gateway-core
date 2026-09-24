# R55 48h 审计轮收口 handoff —— 2026-09-22

> 收口轮：R55（执行 R54 §七入口；本地溯源期 0h，未起 48h 滚动窗口）
> 上轮：R54（af34fc8b8 → c48651e58 / 8dcafa31a）
> 下一轮：R56（按本 R55 §九入口执行）

## 一、本轮目标与发现概览

R54 handoff §七 入口列了 10 项（修复面 3 + 接线面 2 + 观测面 3 + 治理面 2）。
本轮聚焦：

- **R54-F1a dl_load_project_env 4 测补强**（修复面 #1，最高优先级）
- **§11.6 真实供应商验收 s12 scenario**（修复面 #2）

执行中暴露 R54 修复 commit c48651e58 的两层额外 bug，登记为 **R55-F1b**
与原 R54-F1a 同一文件、同一 commit 修复，避免污染历史。

接线面 / D17 瘦身 / 观测面 / 治理面共 6 项**全部延后 R56**（§九 入口
明示），理由：本轮已捕获一个真生产事故（c48651e58 让 deploy 实质无效，
但 R54 §二 误判为『已根修』），R56 应优先把 §11.6 真机 154/245 验收跑
一遍再做大范围重构，避免重构覆盖未实测的 wire-error 路径。

## 二、本轮修复（R55-F1b：dl_load_project_env 三层 bug）

### 2.1 bug 1：bash 调用层 silent no-op（c48651e58 引入）

```bash
# 原 commit（c48651e58）:
done < <(
  { _dl_safe_env_source "$file" >/dev/null 2>&1; env -0; }
)
```

两条路都断：

1. `_dl_safe_env_source ... >/dev/null 2>&1` 把 Python 的 NUL-delimited
   `KEY=value\x00` 输出直接扔到 /dev/null。Python 的 stdout 是 bash
   loop 读 NUL 记录的唯一来源，丢了就无值可设。
2. `env -0` 是独立进程，它 dump 自己的 env。Python 是 `env` 的兄弟进程
   （都是 dl_load_project_env 的子），Python 写 `os.environ[key] = val`
   不会传给 `env` 进程。所以 `env -0` 只看到调用 shell 既有 env，
   看不到任何新值。

实测验证（bash 5.3.9）：

```bash
unset FOO
source deploy-local-lib.sh
cat > /tmp/foo.env <<EOF
FOO=bar&baz
EOF
dl_load_project_env /tmp/foo.env
echo "${FOO:-UNSET}"   # → UNSET  ← 函数无操作
```

修复后（drop redirect + drop env -0）：

```bash
done < <(_dl_safe_env_source "$file")
```

### 2.2 bug 2：bash 5.x parse 错误 / segfault

原结构把 14 行长注释（含 `${VAR}`、`(paren)`、`|` 字符）放在
`<(...)` process substitution 内部。在标准测试包装 `(...)` subshell
里调用时：

- bash 5.3.9（homebrew）：**segfault**（`1671 Segmentation fault: 11`）
- bash 3.2.57（macOS 系统）：报 `bad substitution: no closing ')' in <(`

定位过程：逐步简化 `<(...)` 内的注释（短 / 长 / 含 `${VAR}` / 含
`` `backticks` ``），最后定位到任意 7+ 行注释 + 含 `$` 或 `(` 字符即
触发；移到 `<(...)` 外部即修复。修复：

```bash
dl_load_project_env() {
  # ... 长注释放在函数顶部 ...
  while ...; done < <(_dl_safe_env_source "$file")  # 只一行
}
```

### 2.3 bug 3：Python 解析器单引号仍做 `${VAR}` 插值

bash 单引号语义禁止任何展开，但 Python parser 的 expand() 对所有
value 无差别调用：

```python
if len(raw_val) >= 2 and raw_val[0] == raw_val[-1] and raw_val[0] in ('"', "'"):
    val = raw_val[1:-1]
else:
    val = raw_val
val = expand(val)   # ← 单引号也展开，与 bash 不符
```

修复：跟踪 `quote_char`，单引号跳过 expand：

```python
quote_char = ''
if len(raw_val) >= 2 and raw_val[0] == raw_val[-1] and raw_val[0] in ('"', "'"):
    quote_char = raw_val[0]
    val = raw_val[1:-1]
else:
    val = raw_val
if quote_char != "'":    # bash: 单引号禁止展开
    val = expand(val)
```

### 2.4 新测试覆盖（R54-F1a 4 测）

`scripts/deploy-local-lib-project_env_test.sh`，复用 `dl_load_env_file`
测试模板（行 11-76）：

| # | 场景 | 钉桩 | 验证语义 |
|---|------|------|----------|
| 1 | Veritrans&9527 | `LLM_GATEWAY_ADMIN_PASSWORD=Veritrans&9527` → `Veritrans&9527` | unquoted `&` 不分割、不后台化 |
| 2 | `${VAR}` 插值 | `CRM_DATABASE_URL=postgres://${CRM_DB_USER}@${CRM_DB_HOST}/crm` + caller `export CRM_DB_USER=crm CRM_DB_HOST=db.local` → `postgres://crm@db.local/crm` | DSN 拼接仍工作 |
| 3 | 单/双引号透传 | `DQ="ampersand & ${DB_USER} interpolated"` + `SQ='dollar ${NOT_EXPANDED} literal'` + caller `export NOT_EXPANDED=should_not_be_used` → SQ 保留 `${NOT_EXPANDED}` 字面 | bash 引号语义完整对齐 |
| 4 | 多行 PEM block（字面 `\n`） | `PEM_KEY="-----BEGIN [REDACTED-KEY]-----\nMIIE...\n-----END [REDACTED-KEY]-----\n"` → 字面保留 | 单行 + `\n` 转义约定（注释 325-327 钉桩） |

外加 missing-file 守卫回归（caller warn + return 0）。

测试在 bash 5.3.9 + bash 3.2.57 + zsh 5.9 三环境下均绿。

### 2.5 顺延：真实多行 PEM（行间换行）

`scripts/deploy/test-deploy-local-env.sh` 测试 4 仍是 pre-existing
失败（got= 截断，expected=完整 PEM），但本轮**未触及**：其 .env 文件用
**行间换行**（每行 PEM 一行），Python parser 按行解析只取第一行
`"-----BEGIN PUBLIC KEY-----`，关闭引号匹配失败，回退到未引用值流，
把开头的 `"` 当字面字节写入——这与 deploy-local-lib.sh:325-327 注释
『Multi-line quoted values (PEM blocks in .env.local) survive because
the sourcing subshell hands its environment over as NUL-delimited
records』是不一致的（旧 bash-source 路径支持行间换行；Python 路径只
支持单行 + 字面 `\n`）。R56 D07 应当：(a) 显式决定 Python parser 是否
支持行间换行；(b) 若不支持，文档写明；(c) 删/改 pre-existing 测试 4
与之对齐。

## 三、本轮 s17 scenario（§11.6 wire-error fallback 钉桩）

### 3.1 缺口识别

R54 handoff §九 §11.6 验收说『s11 覆盖缺 [DONE] 路径，未覆盖 commit 后
EOF』。复盘 s11_stream_broken_provider：

```json
"setup": [
  {"provider": "mock-alpha", "state": "broken_stream"},
  {"provider": "mock-beta",  "state": "healthy"},
  ...
]
"expect": { "require_done": true, "require_providers": ["mock-beta"] }
```

s11 实际是『1 provider 坏 + 其它健康，走 failover 拿完整 [DONE]』。
**没有覆盖**『所有 bound provider 都 commit-then-EOF 时，gateway 是否
按 §11.6 翻转（a1b17b224）走 wire-error fallback』这条核心契约。

### 3.2 新增 s17 场景

`tests/stress/scripts/scenarios.json` 末尾追加 s17_committed_eof_all_broken：

- mock-stress-pool（绑定 alpha/beta/gamma/delta 全部 4 个 provider）
- 全部置 broken_stream
- 80 req @ c12
- expect：min_success_rate=1.0, max_success_rate=1.0,
  require_status=[200], forbidden_status=[500,502,503],
  **require_error_envelope=true, min_error_envelope_rate=1.0**

`require_error_envelope=true` 是新断言字段，scenario.go 配套扩展：

- `sample.sawErrorEnvelope bool` —— stream reader 检测
  `"type":"upstream_incomplete"` 或 `"code":"eof_without_done"`
  子串（SSOT：domains/streaming/stream.go:1157）
- `scenarioResult.ErrorEnvelopeCount / ErrorEnvelopeRate`
- `expectSpec.RequireErrorEnvelope / MinErrorEnvelopeRate`

向后兼容：s1-s16 全部 unchanged（zero-value bool/float 不触发断言）。

### 3.3 ID 选 s17 而非 s12 的理由

R54 §九 R55 入口原话是『s12 scenario』，但 s12 已被 s12_flaky_provider
占用。重编号 s12→s13, s13→s14, ..., s16→s17 会扩散到：

- `tests/stress/scripts/capacity_matrix.sh:63-64`（引用 s13, s15）
- `tests/stress/scripts/memprofile/main.go:180-181, 363, 368`
  （引用 s13, s15）

3 文件 8 处需要同步改。本轮选择 s17 末端追加 + scenarios.json 注释
明示『替换原 R54 §九『s12』提议』，避免扩散到 memprofile / capacity
报告链路。R56 若要做 s12 真名，可一次完成编号迁移。

## 四、本轮变更清单

### 4.1 production code 修复

`scripts/deploy-local-lib.sh`：

- dl_load_project_env：移除 `{ _dl_safe_env_source ... >/dev/null 2>&1; env -0; }`
  改为 `< <(_dl_safe_env_source "$file")`；长上下文注释从 `<(...)` 内部
  移到函数顶部；注释行加 R55-F1b 标记
- _dl_safe_env_source（Python）：增加 `quote_char` 跟踪；单引号值
  跳过 expand()

### 4.2 测试

新增：

- `scripts/deploy-local-lib-project_env_test.sh`（R54-F1a 4 测 +
  missing-file 守卫）

修改：

- `tests/stress/scripts/scenario.go` —— expectSpec / sample /
  scenarioResult / aggregateSamples / assertExpectation 增加 wire-error
  envelope 字段
- `tests/stress/scripts/scenarios.json` —— 末尾追加 s17

## 五、测试与验证

- **门一 build**：`go build ./...` ✓
- **门二 vet**：`go vet ./...` ✓
- **门三 test**：
  - `bash scripts/deploy-local-lib-envload_test.sh` → ✓（既有测试未回归）
  - `bash scripts/deploy-local-lib-project_env_test.sh` → ✓（R54-F1a 4 测全绿）
  - `bash scripts/deploy/test-deploy-local-env.sh` → 6/8 通过，2 项
    pre-existing 失败（测试 4 真实多行 PEM + 测试 6 bump-version），
    **与本轮改动无关**
  - `go test ./domains/streaming/ -count=1 -timeout 180s` → ok 71.3s
  - `go test ./domains/dispatch/ -count=1 -timeout 120s` → ok 26.9s
  - `go test ./bg/ -race -count=1 -timeout 60s` → ok 20.7s
  - `go test ./credentialhealth/ -count=1` → ok 0.5s
  - `go test ./internal/ir/ -count=1` → ok 0.5s
  - `go test ./internal/ctxpool/ -count=1` → ok 0.5s
  - `go test ./domains/routing/ -count=1 -timeout 60s` → ok 7.5s
- **scenario.go build + vet**：`go build` ✓ / `go vet` ✓

## 六、推送状态

- Commit 1: `d2426fd2a fix(deploy-local): dl_load_project_env 真正生效 + R54-F1a 4 测补强`
- Commit 2: `32102b57f feat(stress): s17_committed_eof_all_broken 钉桩 §11.6 wire-error fallback`
- Push：待 R55 commit 完成执行后执行

## 七、新发现登记（5 项）

| # | 项 | 级别 | 状态 |
|---|----|------|------|
| R55-D1 | R54 c48651e58 误判为『Python 解析器根修 Veritrans&9527 拆分事故』，实际函数三处全坏（bash no-op + bash parse error + Python 单引号插值）—— 主代理亲读复核 + 在 () subshell 跑回归测试才能发现 | P1 | 本轮已修 |
| R55-D2 | 测试 `scripts/deploy/test-deploy-local-env.sh` 第 4 测（真实多行 PEM）与 R55-F1b 文档承诺（单行 + `\n`）口径不一致——需 D07 决定 Python parser 是否支持行间换行 | P3 | R56 顺延 |
| R55-D3 | scenario.go `sample` 结构体扩展字段名（如 `sawErrorEnvelope`）是公开 API 的一部分——后续如改 envelope 字段名（`type`/`code`/`message`）须同时改本测试与 stream.go:1157 SSOT | P3 | 留档 |
| R55-D4 | `scripts/deploy/test-deploy-local-env.sh` 测试 6 bump-version 解析失败 [latest=v2.4.7 target=2.5.6] —— 语义为何不是 latest=v2.5.6？需 bump-version.sh 单独排查 | P3 | R56 顺延 |
| R55-D5 | bash 5.x 在 `<(...)` process substitution 里含 `$` / `(` 注释触发 segfault / parse error——deploy-local-lib.sh 与 deploy-local.sh 其它 `<(...)` 用法若注释多，需逐一亲测 | P3 | 留档 |

## 八、R55 §11.6 真机验收顺延说明

R54 §九 §11.6 真实供应商验收要求在 154/245 上执行故障注入脚本。
本轮**未执行**，因为：

1. mock 层（tests/stress/mocks/main.go `broken_stream`）已能精确
   模拟『1 chunk + EOF + hijack close』，与真供应商协议违例的 SSE
   截断等价
2. s17 scenario 在 mock 层已钉桩 §11.6 翻转契约，本机可重复
3. 真机 154/245 验收需要故障注入脚本（mock 没暴露的协议细节），
   单轮范围大；推迟到 R56，先跑 s17 验本机契约再上真机

## 九、R56 入口（按 R55 §一 + R54 §九 顺延）

### 修复面（最高优先）

1. **R55-D2 D07 schema/parser 决议**：Python parser 是否支持真实多行 PEM
   （行间换行）。决策选项：(a) parser 升级支持；(b) parser 维持 +
   测试 4 删/改；(c) 改 .env 约定文档。
2. **R55-D4 bump-version.sh 解析失败**：查 latest vs target 的语义差异。
3. **§11.6 真机 154/245 故障注入**：在 mock s17 钉桩通过后跑真机验收。

### 接线面（R54 §九 #4）

4. **Handoff-B 接线第一批**：minheap_topk（影响最小，R54-D3 仅需
   stable 验证；建立 SSOT 切换表 + 双轨期防双计对照）
5. **Handoff-B 接线第二批**：chunk_buffer（R54-D4 SSE frame + CL 改写 +
   tool_call 累积矩阵）
6. **Handoff-B 接线第三批**：ctxpool / ring_error_counter /
   error_detector_ring / prompt_compress

### 观测面

7. Handoff-B 接线后端到端压测（吞吐 / P99 / 内存 / 断路器频率）
8. promote 时间型饥饿 gauge 数据裁决 + 钉桩测试
9. lite journal 「幽灵轮」view 侧过滤

### 治理面

10. UA 三副本 SSOT 收敛
11. bg 剩余约 50 处裸 go（R53-F2 收 BaseWorker，余下 worker 盘点）

### 反思（R55 教训带回）

- **R54 §二『Python 解析器逻辑正确』判断错**——主代理亲读复核
  deploy-local-lib.sh 时只在 deploy-local.sh 调用路径下读，没在测试
  用的 `() subshell` 调用模式下跑回归。R55 §一采纳 R54 §九 入口后，
  写第一个测试时就暴露 silent no-op + parse error。**R56 起，所有
  R4x handoff 列的『已根修』类断言必须在标准测试 subshell + 多种
  bash 版本下回归**，不能仅看代码 commit message。
- **scenario ID 命名 R54 §九 与实际占用冲突**（s12 已占）。R56 命名
  场景时先 grep capacity_matrix.sh / memprofile/main.go 占用情况。

## 十、附：本轮关键证据 / 命令清单

- `git log --since="48 hours ago" --no-merges` → 本轮无新窗口（顺延执行）
- `git diff c48651e58..HEAD --stat` → 4 文件变更（deploy-local-lib.sh +
  scenario.go + scenarios.json + 新 test）
- `go build ./...` → exit 0
- `go vet ./...` → exit 0
- `bash scripts/deploy-local-lib-project_env_test.sh` → ✓
- `python3 -c "import json; print(len(json.load(open('tests/stress/scripts/scenarios.json'))))"` → 17
- `go build ./tests/stress/scripts/...` → exit 0
- `git push origin main` → 待执行