# Multimodal Testing Phase 1-5 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 完整执行多模态端到端测试（Phase 1-5），验证三层实现 + E2E 真实推理，产出回归脚本与测试报告。

**Architecture:** Phase 1 环境准备（migration 451 + 素材 + 编译启动）→ Phase 2 Go 集成测试（Layer A 检测 / Layer B 路由 / Layer C 探测+Admin，按层优先组织）→ Phase 3 独立脚本真实模型推理（OpenAI/Anthropic/条件性 Gemini）→ Phase 4 修复（migration 451 已修 video CHECK；modality 写入链路核查；规则补缺）→ Phase 5 归档（test-results.md + 回归脚本）。

**Tech Stack:** Go 1.22+ / testify / httptest / PostgreSQL 17 (Docker) / Redis / bash / curl / ffmpeg

**Spec:** `docs/multimodal-testing/00-test-plan.md`

---

## File Structure

| 文件 | 职责 | 阶段 |
|---|---|---|
| `sql/migrations/startup/451_models_canonical_modality_video.sql` | 应用 video CHECK | P1 |
| `docs/multimodal-testing/samples/*` | 6 个测试素材 | P1 |
| `domains/streaming/modality_e2e_test.go` | Layer A 检测 + Layer B 路由 E2E | P2 |
| `bg/probe_modality_e2e_test.go` | Layer C 探测 + Admin httptest | P2 |
| `modelname/modality_defaults_test.go` | 扩展 T-21 一致性校验 | P2 |
| `docs/multimodal-testing/phase2-issues.md` | Phase 2 问题记录 | P2 |
| `scripts/multimodal-e2e/run_phase3.sh` | Phase 3 编排 | P3 |
| `scripts/multimodal-e2e/cases/*.json` | 用例 payload | P3 |
| `docs/multimodal-testing/phase3-issues.md` | Phase 3 问题记录 | P3 |
| `modelname/modality_defaults.go` | 规则补缺（若发现） | P4 |
| `docs/multimodal-testing/test-results.md` | 最终报告 | P5 |
| `scripts/test-multimodal-regression.sh` | 一键回归 | P5 |

**现有模式参考**（写测试前必读）：
- `domains/streaming/modality_detect_test.go` — table-driven `cases []struct{name, body, want}` + `t.Run`
- `bg/probe_modality_test.go` — httptest server + `ProbeModality()` 调用
- `modelname/modality_defaults_test.go` — `InferModality(name)` 直接断言

---

## Task 1: Phase 1 — 应用 migration 451

**Files:**
- Read: `sql/migrations/startup/451_models_canonical_modality_video.sql`
- DB: `llm-gateway-pg` Docker container

- [ ] **Step 1: 确认 migration 文件内容**

Run: `cat sql/migrations/startup/451_models_canonical_modality_video.sql`
Expected: ALTER TABLE models_canonical DROP/ADD CHECK constraint 含 'video'

- [ ] **Step 2: 应用 migration**

```bash
docker exec -i llm-gateway-pg psql -U llm_gateway -d llm_gateway \
  < sql/migrations/startup/451_models_canonical_modality_video.sql
```
Expected: `ALTER TABLE` / `ALTER CONSTRAINT` 成功，无报错

- [ ] **Step 3: 验证 CHECK 约束含 video**

Run:
```bash
docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -tAc \
  "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='models_canonical_modality_check'"
```
Expected: 输出含 `'video'::text`

- [ ] **Step 4: 回滚验证（dry-run，不执行）**

Read `451_models_canonical_modality_video.down.sql` 确认回滚脚本存在。仅记录，不执行。

---

## Task 2: Phase 1 — 生成测试素材

**Files:**
- Create: `docs/multimodal-testing/samples/test-image-small.jpg`
- Create: `docs/multimodal-testing/samples/test-image-small.png`
- Create: `docs/multimodal-testing/samples/test-video-short.mp4`
- Create: `docs/multimodal-testing/samples/test-audio-short.mp3`
- Create: `docs/multimodal-testing/samples/test-image-large.jpg`
- Create: `docs/multimodal-testing/samples/test-base64-invalid.txt`

- [ ] **Step 1: 检查 ffmpeg 可用**

Run: `ffmpeg -version | head -1`
如缺失：`brew install ffmpeg`（或跳过音视频用例，记入 phase2-issues）

- [ ] **Step 2: 生成小尺寸图片**

```bash
cd docs/multimodal-testing/samples
# JPEG: picsum 随机图
curl -sL -o test-image-small.jpg https://picsum.photos/400/300
# PNG: ffmpeg 生成纯色
ffmpeg -y -f lavfi -i color=c=red:s=400x300:d=1 -frames:v 1 test-image-small.png 2>/dev/null
```
Expected: 两个文件均 < 200KB

- [ ] **Step 3: 生成音视频**

```bash
# 音频: 1kHz sine 3s, 16kHz mono
ffmpeg -y -f lavfi -i "sine=frequency=1000:duration=3" -ar 16000 -ac 1 test-audio-short.mp3 2>/dev/null
# 视频: 纯色 5s H.264
ffmpeg -y -f lavfi -i color=c=blue:s=320x240:d=5 -c:v libx264 -pix_fmt yuv420p test-video-short.mp4 2>/dev/null
```
Expected: mp3 < 100KB, mp4 < 1MB

- [ ] **Step 4: 生成边界/错误素材**

```bash
# 超大图片: 高频噪声 5000x5000
ffmpeg -y -f lavfi -i "nullsrc=s=5000x5000:d=1" -frames:v 1 -c:v mjpeg -q:v 1 test-image-large.jpg 2>/dev/null
# 畸形 base64
printf '!!!this-is-not-valid-base64!!!' > test-base64-invalid.txt
```
Expected: test-image-large.jpg > 5MB；test-base64-invalid.txt 约 30 bytes

- [ ] **Step 5: 设权限并验证**

```bash
chmod 600 test-image-* test-video-* test-audio-* test-base64-*
ls -lh
```
Expected: 6 个文件齐全，大小符合预期

---

## Task 3: Phase 1 — 编译并启动本地 gateway

**Files:**
- Build: `bin/llm-gateway` from `cmd/gateway`
- Run: 本地进程

- [ ] **Step 1: 注入凭据**

```bash
/Users/xutaohuang/.agents/skills/env-injector/scripts/env-injector.sh inject server_154
source .env.local
```
Expected: `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` / `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 已 export

- [ ] **Step 2: 编译**

Run: `go build -o bin/llm-gateway ./cmd/gateway`
Expected: 无报错，`bin/llm-gateway` 生成

- [ ] **Step 3: 启动（后台，DEBUG 日志）**

```bash
LOG_LEVEL=debug PORT=8080 \
  LLM_GATEWAY_DATABASE_URL="postgres://llm_gateway:<pwd>@localhost:5432/llm_gateway?sslmode=disable" \
  LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="$LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY" \
  ./bin/llm-gateway > /tmp/llm-gateway-test.log 2>&1 &
echo $! > /tmp/llm-gateway-test.pid
sleep 3
```
Expected: 进程存活；log 无 fatal

- [ ] **Step 4: 健康检查**

Run: `curl -s http://localhost:8080/health || curl -s http://localhost:8080/healthz`
Expected: 200 OK（或记录实际健康路径）

- [ ] **Step 5: 验证 gemini credential 可解密**

Run: `curl -s http://localhost:8080/api/models?provider=google-gemini -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" | head -c 500`
Expected: 返回模型列表（非解密错误）。若报解密错 → 记入 phase3-issues，Phase 3 gemini 用例跳过。

---

## Task 4: Phase 2 — Layer A 检测 E2E 测试

**Files:**
- Create: `domains/streaming/modality_e2e_test.go` (package streaming)
- Reference: `domains/streaming/modality_detect_test.go` (现有 table-driven 模式)

- [ ] **Step 1: 写 T-01~T-13/T-18/T-20 检测用例**

新建 `domains/streaming/modality_e2e_test.go`，沿用现有 table-driven 模式。覆盖正向（text/vision/audio/video/multimodal/gemini fileData）、边界（404 URL/畸形 base64/超大/空文件）、优先级（image+audio→audio）。

关键断言：
```go
func TestE2E_DetectModality_Matrix(t *testing.T) {
    cases := []struct{ id, name, body, want string }{
        {"T-02", "openai image_url http", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}]}`, "vision"},
        {"T-08", "openai video_url", `{"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/x.mp4"}}]}]}`, "video"},
        {"T-11", "malformed base64", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,!!!not-base64!!!"}}]}]}`, "text"}, // 检测不崩
        // ... 其余用例按矩阵补全
    }
    for _, tc := range cases {
        t.Run(tc.id+"_"+tc.name, func(t *testing.T) {
            got := detectRequestModality([]byte(tc.body))
            if got != tc.want {
                t.Errorf("%s: got %q want %q", tc.id, got, tc.want)
            }
        })
    }
}
```

- [ ] **Step 2: 运行测试**

Run: `go test ./domains/streaming/ -run TestE2E_DetectModality_Matrix -v`
Expected: 全 pass（T-11 等边界预期返回 text，不 panic）

- [ ] **Step 3: commit**

```bash
git add domains/streaming/modality_e2e_test.go
git commit -m "test(multimodal): add Layer A detection E2E matrix (T-01~T-13,T-18,T-20)"
```

---

## Task 5: Phase 2 — Layer B 路由过滤测试

**Files:**
- Modify: `domains/streaming/modality_e2e_test.go` (追加 Layer B 测试)
- Reference: `domains/streaming/candidate_modality_test.go` (现有路由测试模式)
- Read: `provider/client.go:GetCandidatesByModality()` (签名确认)

- [ ] **Step 1: 读 GetCandidatesByModality 签名**

Run: `grep -n "func GetCandidatesByModality" provider/client.go`
确认参数与返回类型，测试时构造最小 candidate 集。

- [ ] **Step 2: 写 T-14~T-17/T-19 路由用例**

追加到 `modality_e2e_test.go`。构造 candidate 列表（含 text-only / vision-only / multimodal / embedding 模型），验证 SQL filter 语义：
```go
func TestE2E_RouteFilter_Matrix(t *testing.T) {
    // T-15: 向 text-only 模型发 vision 请求 → 返回空候选
    // T-19: embedding 请求 → 只返回 modality='embedding'
    // 用真实 PG 或 in-memory mock，依现有 candidate_modality_test.go 模式
}
```

- [ ] **Step 3: 运行**

Run: `go test ./domains/streaming/ -run TestE2E_RouteFilter_Matrix -v`
Expected: 全 pass

- [ ] **Step 4: commit**

```bash
git add domains/streaming/modality_e2e_test.go
git commit -m "test(multimodal): add Layer B routing filter E2E (T-14~T-19)"
```

---

## Task 6: Phase 2 — T-21 一致性校验（expected_failure）

**Files:**
- Modify: `modelname/modality_defaults_test.go`
- Read: `modelname/modality_defaults.go` (现有规则)

- [ ] **Step 1: 写 T-21 一致性测试**

追加：对 DB 中已知模型，校验 `InferModality(name)` 与 DB `modality` 一致。当前 `claude-3-5-sonnet-20241022`（DB text vs 规则 vision）不一致 → 用 `t.Skip` 或标记 expected_failure。

```go
func TestE2E_ModalityConsistency_KnownGaps(t *testing.T) {
    gaps := []struct{ model, dbModality string }{
        {"claude-3-5-sonnet-20241022", "text"},     // DB text, 规则 vision → bug
        {"whisper-1", "text"},                      // DB text, 规则 audio → 写入链路 bug
        {"gemini-2.0-flash-exp", "text"},           // DB text, 规则 multimodal → bug
    }
    for _, g := range gaps {
        inferred := InferModality(g.model)
        if inferred == g.dbModality {
            continue // 已修
        }
        t.Logf("EXPECTED_FAILURE %s: DB=%s inferred=%s (Phase 4 修复)", g.model, g.dbModality, inferred)
    }
}
```

- [ ] **Step 2: 运行并记录**

Run: `go test ./modelname/ -run TestE2E_ModalityConsistency -v`
Expected: 日志输出 3 个 expected_failure（不 fail 测试）

- [ ] **Step 3: commit**

```bash
git add modelname/modality_defaults_test.go
git commit -m "test(multimodal): add T-21 modality consistency check (3 expected_failures)"
```

---

## Task 7: Phase 2 — Layer C 探测 + Admin httptest

**Files:**
- Create: `bg/probe_modality_e2e_test.go` (package bg)
- Reference: `bg/probe_modality_test.go` (现有 httptest 模式)
- Read: `admin/model_modality.go` (PATCH endpoint 签名)

- [ ] **Step 1: 写 ProbeModality httptest 用例**

起 `httptest.Server` 模拟上游，分别返回 200 / 400(vision_unsupported) / 401 / 500，验证 `ProbeModality()` 分类正确（Supported/ErrCode）。

- [ ] **Step 2: 写 Admin PATCH 用例**

起 httptest server 或用现有 admin test pattern：
- 非 super_admin token → 403
- allow-list 外 modality 值 → 422
- 合法值 → 200 且 DB 更新

- [ ] **Step 3: 运行**

Run: `go test ./bg/ -run TestE2E_Probe -v && go test ./admin/ -run TestE2E_ModelModality -v`
Expected: 全 pass

- [ ] **Step 4: commit**

```bash
git add bg/probe_modality_e2e_test.go
git commit -m "test(multimodal): add Layer C probe + admin E2E (httptest)"
```

---

## Task 8: Phase 2 — 全量 go test + 问题记录

**Files:**
- Create: `docs/multimodal-testing/phase2-issues.md`

- [ ] **Step 1: 全量跑 modality 相关测试**

Run: `go test ./domains/streaming/... ./bg/... ./admin/... ./modelname/... -v 2>&1 | tee /tmp/p2-test.log`
Expected: 除 T-21 expected_failure 外全 pass

- [ ] **Step 2: 记录问题到 phase2-issues.md**

记录任何 fail / panic / 意外行为。格式：
```markdown
# Phase 2 Issues
| 用例 | 现象 | 根因假设 | 修复任务 |
```

- [ ] **Step 3: commit**

```bash
git add docs/multimodal-testing/phase2-issues.md
git commit -m "docs(multimodal): record Phase 2 issues"
```

---

## Task 9: Phase 3 — 真实模型脚本

**Files:**
- Create: `scripts/multimodal-e2e/run_phase3.sh`
- Create: `scripts/multimodal-e2e/cases/*.json`
- Create: `docs/multimodal-testing/phase3-issues.md`

- [ ] **Step 1: 写 cases JSON**

为 T-02/T-03/T-04/T-05/T-15 写请求 payload JSON（model + messages + image/audio）。base64 内联小图。

- [ ] **Step 2: 写 run_phase3.sh**

```bash
#!/usr/bin/env bash
# 编排：逐个 case curl 本地 gateway /v1/chat/completions，断言 HTTP 200 + 非空 content
# --dry-run: 只打印 curl 不发请求
# 成本护栏：每 case ≤2 次，总 ≤15 次，超时 30s
set -euo pipefail
GATEWAY="${GATEWAY:-http://localhost:8080}"
ADMIN_KEY="${LLM_GATEWAY_ADMIN_API_KEY}"
# ... 逐 case 调用，记结果
```

- [ ] **Step 3: dry-run 验证**

Run: `bash scripts/multimodal-e2e/run_phase3.sh --dry-run`
Expected: 打印 5 个 curl 命令，无实际请求

- [ ] **Step 4: 真实执行**

Run: `bash scripts/multimodal-e2e/run_phase3.sh 2>&1 | tee /tmp/p3-real.log`
Expected: ≥3 个用例 HTTP 200 + 非空响应。记录失败到 phase3-issues.md。

- [ ] **Step 5: commit**

```bash
git add scripts/multimodal-e2e/ docs/multimodal-testing/phase3-issues.md
git commit -m "test(multimodal): Phase 3 real model scripts + results"
```

---

## Task 10: Phase 4 — 修复 modality 写入链路 + 规则补缺

**Files:**
- Read: `modelname/modality_defaults.go` (whisper- 规则已存在)
- Read: `discovery/discovery.go` (写入 modality 的逻辑)
- Possibly Modify: `discovery/discovery.go` / `modelname/modality_defaults.go`

- [ ] **Step 1: 诊断 whisper-1 写入为 text 的根因**

规则表已有 `{"whisper-", "audio", 1}`（modality_defaults.go:56），但 DB whisper-1=text。检查 discovery 写入路径是否调用 `InferModality`，或被其他逻辑覆盖。

Run: `grep -rn "InferModality\|modality" discovery/discovery.go | head -20`

- [ ] **Step 2: 修复写入链路或重跑 discovery**

依诊断：
- 若 discovery 未调 InferModality → 修复调用
- 若已调但被覆盖 → 修复覆盖逻辑
- 若规则本身需补 → 补 claude-3-5-sonnet-20241022 等 exact match 规则

- [ ] **Step 3: 重跑 discovery 或手动 UPDATE**

```bash
# 手动修正（验证用）
docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -c \
  "UPDATE models_canonical SET modality='audio' WHERE canonical_name='whisper-1'"
# 或触发 gateway discovery 周期
```

- [ ] **Step 4: 重跑 T-21，确认转 pass**

Run: `go test ./modelname/ -run TestE2E_ModalityConsistency -v`
Expected: 无 expected_failure 日志（或减少）

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./domains/streaming/... ./bg/... ./admin/... ./modelname/...`
Expected: 全绿

- [ ] **Step 6: commit**

```bash
git add -A
git commit -m "fix(multimodal): correct modality write path + rule gaps (whisper/claude-20241022)"
```

---

## Task 11: Phase 5 — 测试报告 + 回归脚本

**Files:**
- Create: `docs/multimodal-testing/test-results.md`
- Create: `scripts/test-multimodal-regression.sh`

- [ ] **Step 1: 写 test-results.md**

汇总：Phase 2 通过率 (X/20) / Phase 3 通过率 (Y/6) / 修复记录 / 残留项。

- [ ] **Step 2: 写回归脚本**

```bash
#!/usr/bin/env bash
# 一键复跑：go test Phase 2 + Phase 3 dry-run
set -euo pipefail
echo "== Phase 2 =="
go test ./domains/streaming/... ./bg/... ./admin/... ./modelname/...
echo "== Phase 3 (dry-run) =="
bash scripts/multimodal-e2e/run_phase3.sh --dry-run
echo "ALL PASS"
```

- [ ] **Step 3: 跑回归脚本验证**

Run: `bash scripts/test-multimodal-regression.sh`
Expected: 退出码 0

- [ ] **Step 4: commit + push**

```bash
git add docs/multimodal-testing/test-results.md scripts/test-multimodal-regression.sh
git commit -m "docs(multimodal): Phase 5 test results + regression script"
git push -u origin test/multimodal-testing
```

---

## Self-Review

**Spec coverage**: 00-test-plan.md 矩阵 T-01~T-21 全部映射到 Task 4-7；Phase 1-5 全覆盖。Gap: 无。

**Placeholder scan**: Task 4 Step 1 的 cases 列表标了"按矩阵补全"——执行时需补全 T-01/T-03/T-04/T-05/T-06/T-07/T-09/T-10/T-12/T-13/T-18/T-20 的具体 body。这是有意的，因为完整 21 条 body 在 plan 中冗长，执行时从 00-test-plan.md 矩阵直接取。

**Type consistency**: `detectRequestModality([]byte) string` / `InferModality(string) string` / `ProbeModality(ctx, endpoint, apiKey, model, modality, isAnthropic) ModalityProbeResult` 签名与源码一致。
