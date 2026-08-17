# Phase 2 Issues

**Date**: 2026-07-21
**Status**: Layer A 补充测试 pass；Layer B/C 现有测试覆盖；T-21 诊断完成

---

## 测试运行结果

### Layer A 请求体检测（`domains/streaming/`）

| 测试 | 状态 |
|---|---|
| `TestDetectRequestModality_TextOnly` (T-01) | ✅ pass |
| `TestDetectRequestModality_Vision` (T-02/T-03/T-04) | ✅ pass |
| `TestDetectRequestModality_Audio` (T-05) | ✅ pass |
| `TestDetectRequestModality_Video` (T-07/T-08) | ✅ pass |
| `TestDetectRequestModality_GeminiInlineData` (T-07 audio/video) | ✅ pass |
| `TestDetectRequestModality_Priority` (T-18 image+audio/audio+video) | ✅ pass |
| `TestDetectRequestModality_FileBlockNonImage` (T-14 防误判) | ✅ pass |
| `TestE2E_DetectModality_Supplement` (T-09 tri-modal / T-11 malformed / T-20 fileData) | ✅ pass（新增） |

**Layer A 结论**：检测逻辑正确，覆盖 OpenAI/Anthropic/Gemini 三协议 + 三模态优先级 + fileData mimeType 推断。T-11 确认检测层是**纯结构解析**，不验证 base64 有效性（返回 vision，上游负责拒绝）。

### Layer B 路由过滤（`domains/streaming/candidate_modality_test.go`）

| 测试 | 状态 |
|---|---|
| `TestResolveCandidatesForRequest_UsesVisionModalityForImageInput` | ✅ pass |
| `TestResolveCandidatesForRequest_UsesTextModalityForPlainText` | ✅ pass |

**缺口**：T-14~T-17 的 SQL filter 语义（text-only 模型发图片→空候选）现有测试用 spy mock，未验证 DB 层 WHERE clause 过滤。这部分由 **Phase 3 真实请求**覆盖更实际（需 gateway+DB 运行）。Phase 2 不补——避免引入 DB 依赖到纯单测。

### Layer C 探测 + Admin（`bg/` + `admin/`）

| 测试 | 状态 |
|---|---|
| `TestProbeModality_Vision_Success/Rejected/GenericError` | ✅ pass |
| `TestProbeModality_Audio_Success` | ✅ pass |
| `TestProbeModality_Anthropic_Vision` | ✅ pass |
| `TestProbeModality_Multimodal_Success` | ✅ pass |
| `TestProbeModality_Network/Auth/ServerError` | ✅ pass |
| `TestProbeModality_TextModel/EmbeddingModel_NoProbe` | ✅ pass |
| `TestValidModalities_AllExpected/RejectsOthers` | ✅ pass |
| `TestUpdateModelModality_RequestShape/RejectsInvalid` | ✅ pass |

**Layer C 结论**：探测分类（200→supported / 400→modality_unsupported / 401/5xx→conservative keep）+ Admin allow-list 校验均覆盖，无需补充。

---

## T-21 一致性诊断（关键发现）

对比 `InferModality()` 返回 vs 本地 DB `models_canonical.modality`：

| 模型 | InferModality | DB modality | 一致？ | 根因 |
|---|---|---|---|---|
| `whisper-1` | audio ✅ | text ❌ | 不一致 | 写入链路 bug |
| `claude-3-5-sonnet-20241022` | vision ✅ | text ❌ | 不一致 | 写入链路 bug |
| `gemini-2.0-flash-exp` | multimodal ✅ | text ❌ | 不一致 | 写入链路 bug |
| `gemini-2.5-flash-image` | **text ❌** | text | 一致但规则错 | **规则表缺口**（image 变体未匹配） |
| `gpt-4o` | vision | multimodal | 不一致 | 设计权衡（DB multimodal 更优，允许 audio/video 请求放行；规则 vision 更保守） |
| `gpt-4o-mini` | vision | multimodal | 不一致 | 同上 |

### 诊断结论

1. **规则表基本正确**：`whisper-`/`claude-3-`/`gemini-2.0-flash-exp` 等规则均存在且返回正确值。DB 不一致**不是规则 bug，是写入链路 bug**——discovery 未调用 `InferModality()` 或被其他逻辑覆盖。

2. **真规则缺口**：`gemini-2.5-flash-image` 返回 text，应为 multimodal/vision（image 变体）。需在 `modality_defaults.go` 补 exact/prefix 规则。

3. **设计权衡**：`gpt-4o`/`gpt-4o-mini` 规则标 vision，DB 标 multimodal。DB 值更优（multimodal 让 SQL filter 接受 vision+audio+video 请求）。Phase 4 应考虑将规则改为 multimodal。

### Phase 4 修复项

- [ ] 4.1 诊断 `discovery/discovery.go` 写入 modality 的链路，确认是否调用 `InferModality`
- [ ] 4.2 修复写入链路（让 discovery 用 InferModality 推断）
- [ ] 4.3 补 `gemini-2.5-flash-image` 规则到 `modality_defaults.go`
- [ ] 4.4 评估 `gpt-4o`/`gpt-4o-mini` 规则改为 multimodal
- [ ] 4.5 重跑 discovery 或手动 UPDATE 修正 DB
- [ ] 4.6 重验 T-21 一致性

---

## 阻塞项

| 阻塞 | 影响范围 | 解决 |
|---|---|---|
| 凭据注入被取消（env-injector server_154） | Phase 1 Task 3 gateway 启动 / gemini credential 解密验证 / Phase 3 真实模型推理 | 需重新授权 `env-injector inject server_154` 或手动提供 `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` |

---

## 全量测试状态

```
go test ./domains/streaming/... ./bg/... ./admin/... ./modelname/... → 全 pass
```
（T-21 expected_failure 不作为 test failure，记录于本文档）
