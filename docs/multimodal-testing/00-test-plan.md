# Multimodal Testing Plan — llm-gateway-go

**Date**: 2026-07-21
**Author**: Phase 0 brainstorming session
**Status**: Approved
**Related**: handoff `handoff-multimodal-testing-2026-07-20.md`; commits `41f87d32d`..`ebfc0b1e`

---

## 1. Goal

完整验证多模态能力识别的端到端正确性，覆盖三层实现 + E2E 真实推理：

1. **Layer A 请求体检测** — `domains/streaming/modality_detect.go`
2. **Layer B 路由过滤** — `provider/client.go:GetCandidatesByModality()`
3. **Layer C 探测验证 + Admin 覆盖** — `bg/probe_modality.go` / `admin/model_modality.go`
4. **Layer D 端到端真实推理** — 真实上游模型

范围：Phase 0 → Phase 5 全流程。Phase 2 用 Go 集成测试，Phase 3 用独立脚本，Phase 5 产出回归脚本。

---

## 2. Decisions (locked)

| 维度 | 决策 |
|---|---|
| 范围 | Phase 0 → Phase 5 全流程 |
| Phase 3 执行 | 本地 gateway + 本地 DB（已从生产同步，含 provider/credentials） |
| 测试产物 | Phase 2 = Go 集成测试（进 `go test`）；Phase 3 = 独立脚本；Phase 5 = 回归脚本 |
| 组织策略 | 层优先（Phase 2）+ 模态矩阵（Phase 3 E2E）+ 风险场景内嵌 |
| 真实模型 | OpenAI（gpt-4o/gpt-4o-mini/whisper-1）+ Anthropic（claude-3-5-sonnet）；Gemini 待 credential 解密验证 |

---

## 3. Test Case Matrix

20 个用例 + 1 个一致性校验用例。Phase 2 跑全部，Phase 3 跑可用子集。

| ID | 模态 | 格式 | 场景 | Phase | 入口层 |
|---|---|---|---|---|---|
| T-01 | text | `{content:"..."}` | 正向 | P2+P3 | A 检测 |
| T-02 | vision | OpenAI `image_url` (URL) | 正向 | P2+P3 | A→B→D |
| T-03 | vision | OpenAI `image_url` (base64 data URL) | 正向 | P2+P3 | A→B→D |
| T-04 | vision | Anthropic `source.base64` | 正向 | P2+P3 | A→B→D |
| T-05 | audio | OpenAI `input_audio` (base64) | 正向 | P2+P3 | A→B→D |
| T-06 | audio | Whisper `/v1/audio/transcriptions` 上传 | 正向 | P2 | A→B |
| T-07 | video | Gemini `inlineData` (mimeType video/mp4) | 正向 | P2 | A 检测 |
| T-08 | video | OpenAI `video_url` | 正向 | P2 | A 检测 |
| T-09 | multimodal | text + image + audio 混合 | 正向 | P2 | A 检测（优先级） |
| T-10 | vision | 无效 URL (404) | 边界 | P2 | A→B（容错） |
| T-11 | vision | 畸形 base64 | 边界 | P2 | A（检测不崩） |
| T-12 | vision | 超大图片 (>5MB) | 边界 | P2 | A→B（拒绝/裁剪） |
| T-13 | audio | 空文件上传 | 边界 | P2 | A→B |
| T-14 | text | 向 vision-only 模型发纯文本 | 错误 | P2 | B 路由（应通过） |
| T-15 | vision | 向 text-only 模型发图片 | 错误 | P2+P3 | B 路由（应过滤） |
| T-16 | audio | 向 vision 模型发音频 | 错误 | P2 | B 路由（应拒绝） |
| T-17 | video | 向非 video 模型发视频 | 错误 | P2 | B 路由 |
| T-18 | multimodal | image+audio（优先级 audio>vision） | 边界 | P2 | A 检测（优先级） |
| T-19 | embedding | text-only embedding 请求 | 正向 | P2 | B 路由 |
| T-20 | vision | Gemini `fileData` image mimeType | 正向 | P2 | A 检测 |
| T-21 | 一致性 | `models_canonical.modality` vs `InferModality()` 一致 | 校验 | P2 | 规则推断 |

### Phase 3 真实模型子集

| 模型 | 模态 | 用例 | 上游 key |
|---|---|---|---|
| `gpt-4o-mini` | vision | T-02, T-03 | OPENAI_API_KEY ✅ |
| `gpt-4o` | vision | T-02（对比 mini） | OPENAI_API_KEY ✅ |
| `claude-3-5-sonnet-20241022` | vision (source) | T-04 | ANTHROPIC_API_KEY ✅ |
| `whisper-1` | audio | T-05 | OPENAI_API_KEY ✅ |
| `gemini-1.5-pro` | multimodal | T-07/T-09 | **条件性**：待 credential 解密验证 |

**约束**：Gemini 用例只在 Phase 2 mock 验证检测逻辑；Phase 3 真实推理仅在 credential 可解密时启用。每用例≤2 次调用，总调用≤15 次，单次超时 30s。

---

## 4. Environment Preparation

### 4.1 已确认状态

| 前置项 | 状态 |
|---|---|
| 本地 PG（`llm-gateway-pg`） | ✅ healthy |
| 本地 Redis（`r112_redis`） | ✅ healthy |
| 本地 DB providers/credentials | ✅ 已从生产同步（含 anthropic/google-gemini/apigpt） |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | ✅ `env-injector inject server_154` |
| `~/Downloads/test-*` 素材 | ❌ 不存在 → 需生成到 `samples/` |
| Migration 451 | ❌ **未应用**（CHECK 不含 `video`） |

### 4.2 DB 现状发现（关键 bug）

`models_canonical_modality_check` 当前允许值：`['text','vision','audio','multimodal','embedding']`——**缺 `video`**。

Layer 1 规则推断缺口：

| 模型 | 当前 modality | 应为 | 诊断 |
|---|---|---|---|
| `claude-3-5-sonnet-20241022` | text ❌ | multimodal | 同族 `claude-3-5-sonnet`→multimodal ✅，带日期后缀变体未匹配 |
| `whisper-1` | text ❌ | audio | `gpt-4o-audio-preview`→audio ✅ 正确，whisper 规则缺失 |
| `gemini-2.0-flash-exp` | text ❌ | multimodal | exp 变体未匹配 |
| `gemini-2.5-flash-image` | text ❌ | multimodal/vision | image 变体未匹配 |

→ Phase 4 修复 `modelname/modality_defaults.go` 规则覆盖。

### 4.3 准备步骤

```bash
# 0. 应用 migration 451（修 video CHECK）
docker exec -i llm-gateway-pg psql -U llm_gateway -d llm_gateway \
  < sql/migrations/startup/451_models_canonical_modality_video.sql

# 1. 注入凭据
/Users/xutaohuang/.agents/skills/env-injector/scripts/env-injector.sh inject server_154

# 2. 编译 + 启动
go build -o bin/llm-gateway ./cmd/gateway
LOG_LEVEL=debug PORT=8080 ./bin/llm-gateway

# 3. 验证 gemini credential 可解密
#    启动后 GET /api/models?provider=google-gemini 观察是否报解密错

# 4. 生成测试素材到 docs/multimodal-testing/samples/
#    图片：picsum 或程序生成；音视频：ffmpeg 生成
```

provider seed 策略删除——本地 DB 已同步。

---

## 5. Pass/Fail Criteria

### Phase 2（Go 集成测试）

| 层 | 用例 | 通过标准 |
|---|---|---|
| A 检测 | T-01~T-09, T-11, T-18, T-20 | `detectRequestModality(body)` 返回值与预期常量相等；畸形 base64 返回 `text` 不 panic |
| A 检测 | T-10, T-12, T-13 | 对无效 URL/超大/空文件不 panic，返回保守 `text` |
| B 路由 | T-14~T-17, T-19 | text-only 模型发图片→空候选；纯文本→全部；embedding→只返回 embedding |
| 一致性 | T-21 | `models_canonical.modality` 与 `InferModality()` 一致；**当前 claude-3-5-sonnet-20241022/whisper-1 不一致 → `expected_failure`，Phase 4 修复后转 pass** |
| C 探测 | mock httptest 上游 | `ProbeModality` 对返回 vision 格式上游识别 vision；401/超时返回 `text` 不崩 |
| C 探测 | Admin 覆盖 | `PATCH /api/models/:id/modality` 非 super_admin→403；allow-list 外→422；合法→200 且 DB 更新 |

**整体**：`go test ./domains/streaming/... ./bg/... ./admin/... ./modelname/...` 全 pass（T-21 expected_failure 除外）；无 panic；覆盖率不降。

### Phase 3（独立脚本）

| 用例 | 通过标准 |
|---|---|
| T-02 (gpt-4o-mini, image URL) | HTTP 200；response 含文本；非空 content |
| T-03 (gpt-4o-mini, base64) | HTTP 200；识别图片内容 |
| T-04 (claude-3-5-sonnet, source format) | HTTP 200；前提 modality 已修正，否则记录为 Phase 4 项 |
| T-05 (whisper-1, audio) | HTTP 200；返回非空转录文本 |
| T-15 (向 text 模型发图片) | 路由空候选 OR 上游 400 → 均为"正确拒绝" |
| T-07/T-09 (gemini) | 条件性：credential 可解密→HTTP 200；否则跳过并记录 |

**成本护栏**：每用例≤2 次调用，总≤15 次；单次超时 30s；失败重试≤1 次；`--dry-run` 只打印不发请求。

### Phase 4 修复验证

| 修复项 | 验证 |
|---|---|
| migration 451 | CHECK 含 `video`；可 `UPDATE ... SET modality='video'` |
| `modality_defaults.go` 规则补全 | `go test ./modelname/` 新增规则用例 pass；T-21 转 pass；DB 中相关模型 modality 重跑 discovery 后正确 |
| Phase 2/3 其他 bug | 回归脚本复跑全 pass |

### Phase 5 归档

- `test-results.md`：通过率（Phase 2: X/20, Phase 3: Y/6）+ 问题 + 修复
- 回归脚本 `scripts/test-multimodal-regression.sh`：本地一键复跑，退出码 0=全 pass
- 样本脱敏，`chmod 600`

---

## 6. Deliverables & File Layout

```
docs/multimodal-testing/
├── 00-test-plan.md              ← 本文档
├── phase2-issues.md             ← Phase 2 问题
├── phase3-issues.md             ← Phase 3 问题
├── test-results.md              ← 最终报告
└── samples/
    ├── test-image-small.jpg
    ├── test-image-small.png
    ├── test-video-short.mp4
    ├── test-audio-short.mp3
    ├── test-image-large.jpg
    └── test-base64-invalid.txt

domains/streaming/modality_e2e_test.go      ← Phase 2 Layer A+B
bg/probe_modality_e2e_test.go               ← Phase 2 Layer C
modelname/modality_defaults_test.go         ← 扩展（T-21 + 规则补全）

scripts/multimodal-e2e/
├── run_phase3.sh
├── mock_upstream.go            ← 复用
└── cases/                      ← 各用例 JSON

scripts/test-multimodal-regression.sh  ← Phase 5 一键回归
```

---

## 7. Execution Order

| 步骤 | 动作 | 产出 |
|---|---|---|
| 0.1 | 写本方案 + commit | `00-test-plan.md` |
| 0.2 | writing-plans skill 生成实施计划 | spec 文档 |
| 1.1 | 应用 migration 451 | video CHECK 修复 |
| 1.2 | 生成测试素材 | 6 个文件 |
| 1.3 | 编译 + 启动 gateway | 健康检查通过 |
| 1.4 | 验证 gemini credential | 决定 T-07/T-09 是否启用 |
| 2.1 | Phase 2 Layer A 测试 | T-01~T-13,T-18,T-20 |
| 2.2 | Phase 2 Layer B 测试 | T-14~T-17,T-19,T-21 |
| 2.3 | Phase 2 Layer C 测试 | 探测+Admin |
| 2.4 | `go test` 全跑，记问题 | phase2-issues.md |
| 3.1 | Phase 3 脚本 + cases | scripts/multimodal-e2e/ |
| 3.2 | 跑真实模型，记问题 | phase3-issues.md |
| 4.1 | 修 `modality_defaults.go` | claude-3-5-sonnet-20241022/whisper-1 等 |
| 4.2 | 修正 DB modality | T-21 转 pass |
| 4.3 | 修其他问题 | 回归全 pass |
| 4.4 | `go build && go vet && go test ./...` | 全绿 |
| 5.1 | 写 test-results.md | 报告 |
| 5.2 | 写回归脚本 | test-multimodal-regression.sh |
| 5.3 | commit + push | 远程可见 |

---

## 8. Risks & Rollback

| 风险 | 回退 |
|---|---|
| migration 451 应用失败 | 回滚 `451_..._video.down.sql`；Phase 2 video 用例 skip |
| gemini credential 不可解密 | T-07/T-09 跳过，仅 mock 验证检测 |
| 真实 API 限频/不可用 | 切 `gpt-4o-mini`；失败记录不阻断 Phase 4 |
| Phase 4 规则修复引发现有测试失败 | `git stash` 回滚，保留 T-21 expected_failure，后续 session 修 |
| 本地 gateway 启动失败 | 回退到 server_154 生产 gateway（需批准，默认不碰生产） |

---

## 9. Success Criteria (handoff 对齐)

- ✅ Phase 2: 20 用例全 pass（T-21 expected_failure 除外）
- ✅ Phase 3: ≥3 个真实模型测试通过（OpenAI vision + Anthropic vision + Whisper audio）
- ✅ Phase 4: 所有问题修复，`go test ./...` 无新增失败
- ✅ Phase 5: 文档完整，回归脚本可复现

---

## 10. Entry Points Reference

| 入口 | 文件 | 函数 |
|---|---|---|
| 请求时检测 | `domains/streaming/modality_detect.go` | `detectRequestModality()` |
| 路由过滤 | `provider/client.go` | `GetCandidatesByModality()` |
| 探测验证 | `bg/probe_modality.go` | `ProbeModality()` |
| Admin 覆盖 | `admin/model_modality.go` | `PATCH /api/models/:id/modality` |
| 规则推断 | `modelname/modality_defaults.go` | `InferModality()` |

---

**End of Plan.**
