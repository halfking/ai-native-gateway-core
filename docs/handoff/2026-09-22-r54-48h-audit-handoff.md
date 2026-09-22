# R54 48h 审计轮收口 handoff —— 2026-09-22

> 收口轮：R54（48h 滚动窗口 af34fc8b8 → c48651e58）
> 下一轮：R55（按 R54 §九入口执行）

## 一、本轮基线与改动面

- **基线**：af34fc8b8（48h 起点）→ HEAD c48651e58（顺手 ff 到 origin/main，最后再 rebase 8e1a6a5c5）
- **窗口**：48h 内 79 个非 merge commit
- **关键改动**：
  1. §11.6 EOF-without-DONE 翻转（a1b17b224）——committed 截断走结构化错误
  2. Handoff-B 7 模块落库（a1b17b224）——全部未接线主路径
  3. Wave1-A1..A5 修复波（659324c93 / 0fc829916 / 2de612429 / 83567d0ca / 4f88f1861）
  4. 部署脚本 dl_load_project_env Python 解析器（c48651e58）——Veritrans&9527 拆分事故根修

## 二、本轮收口

- **§11.6 stream.go:1150+**：主代理亲读复核 ✅ 路径闭合（wire 顺序 + HTTP 200 + executor Kind + 没有 silent 200 四维度均钉）
- **Handoff-B 7 模块**：全部为 `feat` + 测试，未接线即零生产影响；每项记录下次接线时的"双轨期防双计 / 对照表 / 边界矩阵"注意事项（R54-D1..D5）
- **Wave1 五项**：每项配测试 + 设计文档（ADR / 注释），意图基线对齐
- **部署脚本修复**：Python 解析器逻辑正确；现有测试仅覆盖 dl_load_env_file 不覆盖 dl_load_project_env（R54-F1a）

## 三、并行子代理策略教训（必读）

**R54 派发 8 路并行域子代理全部被 TPM 限流取消**（2/10 直接失败 rate limit exceeded；8/10 cancel 退避）：

- 主代理派发并行子代理时，单轮并发 ≤8 路（按 conventions.md）仍可能触发 TPM 限流
- 后续审计轮派发时，**单轮并发 ≤4 路**（更稳妥），或派发后让 mavis 主代理使用 `run_in_background=false` 等待关键域返回
- 或者在派发前明确让用户/调度方配置更大的 TPM 配额

## 四、本轮新发现登记（5 项 P3）

| # | 项 | 状态 |
|---|---|---|
| R54-D1 | Handoff-B 5/7 模块零调用方，需在下次接线轮做"双轨并存期防双计"对照 | 登记 |
| R54-D2 | ctxpool 复用 context deadline 必须做对照表 | 登记 |
| R54-D3 | minheap_topk unstable 排序，接入时若原 sort.Slice 要求 stable 需补 comparator | 登记 |
| R54-D4 | chunk_buffer 接入必须验证 SSE frame 边界 + Content-Length 重写 + tool_call 累积顺序 | 登记 |
| R54-D5 | prompt_compress 接入前回答"折叠 reasoning/thinking 块是否影响语义"问题 | 登记 |
| R54-F1a | dl_load_project_env 测试覆盖空白（Veritrans&9527 / ${VAR} / 多行 PEM） | 待 R55 修 |

## 五、测试与验证

- 门一 build：`go build ./...` ✓
- 门二 vet：`go vet ./...` ✓
- 门三 test：
  - `go test ./domains/streaming/ -count=1 -timeout 180s` → ok 71.2s
  - `go test ./domains/dispatch/ -count=1 -timeout 120s` → ok 26.8s
  - `go test ./bg/ -race -count=1 -timeout 60s` → ok 8.7s
  - `go test ./credentialhealth/ -count=1` → ok 0.5s
  - `go test ./internal/ir/ -count=1` → ok 0.7s
  - `go test ./internal/ctxpool/ -count=1` → ok 0.4s
  - `go test ./domains/routing/ -count=1 -timeout 60s` → ok 7.3s

## 六、推送状态

- Commit: `94fa12e96 docs(audit): R54 48h 审计轮留档 —— §11.6 翻转 + Handoff-B 7 模块落库 + Wave1-A1..A5 复核`
- Push: ✅ `8e1a6a5c5..94fa12e96  main -> main`

## 七、R55 入口（按 R54 §九 顺延）

### 修复面优先

1. **R54-F1a dl_load_project_env 4 测补强**
   - `scripts/deploy-local-lib-envload_test.sh` 新增 4 测试：
     1. `Veritrans&9527` 拆分事故回归（带 `&` 未加引号）
     2. `${VAR}` 插值仍生效
     3. 单/双引号透传
     4. 多行 PEM block
   - 复用 `dl_load_env_file` 测试模板（行 11-76）写新一组；放到 `scripts/deploy-local-lib-project_env_test.sh`

2. **§11.6 真实供应商验收**：154/245 故障注入脚本
   - test(stress) s11 覆盖缺 [DONE] 路径，未覆盖 commit 后 EOF
   - 需补 `tests/stress/scripts/scenarios.json` 的 s12 scenario + `tests/stress/scripts/scenario.go` 实现

3. **D17 清理候选清单分批瘦身**（按 R53-D17 待办）
   - 候选：D17 §3 列出的 >800 行文件 / >150 行函数
   - 本轮挑选 2-3 个候选做瘦身（如 `domains/streaming/handler.go` `domains/streaming/stream.go`）

### 接线面（Handoff-B 7 模块本轮选 1-2 个先接，验证 R54-D1..D5）

4. **Handoff-B 接线计划**：
   - 第一批：minheap_topk（影响最小，R54-D3 仅需 stable 验证）
   - 第二批：chunk_buffer（影响中等，R54-D4 需建测试矩阵）
   - 第三批：ctxpool / ring_error_counter / error_detector_ring / prompt_compress
   - 接线时建立 SSOT 切换表 + 双轨期防双计对照

5. **R53 顺延**：Phrases NFKC / 零宽剥离；X-Agent-Name 维度基数治理；count_tokens 观测补齐

### 观测面

6. Handoff-B 接线后做端到端压测（吞吐 / P99 / 内存 / 断路器频率）
7. promote 时间型饥饿 gauge 数据裁决 + 钉桩测试
8. lite journal 「幽灵轮」view 侧过滤

### 治理面

9. UA 三副本 SSOT 收敛
10. bg 剩余约 50 处裸 go（R53-F2 收 BaseWorker，余下 worker 盘点）

## 八、并发策略调整建议

- R55 派发并行子代理时，**单轮并发 ≤4 路**（避免再次触发 TPM 限流）
- 或者在派发前明确让用户配置更大的 TPM 配额
- 主代理亲读复核 ≥40 个文件仍可执行（本轮已验证）
- 若要 8 路并发域覆盖，分两批（4 + 4）派发，每批间隔 5-10 分钟

## 九、附：R54 关键证据 / 命令清单

- `git log --since="48 hours ago" --no-merges` → 79 commit
- `git diff af34fc8b8..c48651e58 --stat` → 418 文件变更
- `go build ./...` → exit 0
- `go vet ./...` → exit 0
- `git push origin main` → `8e1a6a5c5..94fa12e96  main -> main`