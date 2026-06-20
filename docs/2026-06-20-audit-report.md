# llm-gateway-go 24h Audit Report (2026-06-20)

**Commit range:** `166df6d8..c95994da` (48 commits)  
**Audit date:** 2026-06-20  
**Files changed:** 104 files, +15705/-513 lines  

---

## Executive Summary

48 commits in 24 hours touching the entire llm-gateway-go stack: relay protocol conversion, passive probe system, meta-tools foundation, data lifecycle management, compression overview, and extensive UI work. The codebase is well-tested (41 packages pass, `go vet` clean) but several issues were identified and fixed.

---

## Issues Found & Fixed

### 1. 🔴 M020 Migration: Unique Index Would Fail on Old Duplicates

**File:** `db/migrations/020_request_logs_unique_request_id.sql`  
**Problem:** The migration only cleaned duplicates from the last 7 days, but PostgreSQL `CREATE UNIQUE INDEX` fails if ANY duplicates exist — the comment claiming "older duplicates won't block index creation" was incorrect.  
**Fix:** Changed to a full-table dedup using `ROW_NUMBER() OVER (PARTITION BY request_id)` window function, which removes ALL duplicates regardless of age.

### 2. 🟠 Blocking `time.Sleep(150ms)` in Request Hot Path

**File:** `routing/executor_chat.go:388`  
**Problem:** Every `model_not_found` retry added a hardcoded 150ms blocking delay. In high-concurrency scenarios with transient errors, this stalls goroutines and reduces throughput.  
**Fix:** Replaced with a cancellable `select` pattern (matching the existing retry backoff pattern at line 204-208), so client disconnect aborts the wait immediately.

### 3. 🟡 `_tools_cached` Comparison: JSON String vs Boolean

**File:** `relay/handler.go:772`  
**Problem:** `string(cached) == "true"` only works for JSON boolean `true`. If the compressor outputs `"_tools_cached": "true"` (string), the comparison silently fails and tools are never restored.  
**Fix:** Changed to `bytes.Equal(cached, []byte("true")) || bytes.Equal(cached, []byte('"true"'))` to handle both JSON boolean and string representations.

### 4. 🟡 CSS Hardcodes Break Theme Consistency

**File:** `web/src/views/PricingManagementView.vue:1010-1037`  
**Problem:** Pagination bar used hardcoded `#1e1e2e`, `#333`, `#888` instead of CSS variables (`var(--card)`, `var(--border)`, `var(--muted)`). Breaks if theme variables change.  
**Fix:** Replaced all hardcoded colors with CSS custom properties, matching the style used in `RequestLogsView.vue`.

### 5. 🟡 passive_probe_state: No TTL/Cleanup

**File:** `bg/passive_probe_listener.go`  
**Problem:** The `passive_probe_state` table accumulates entries via `ON CONFLICT DO UPDATE` but had no TTL. Over months, the table grows unbounded per `(credential_id, raw_model_name, error_kind)` combination.  
**Fix:** Added `cleanupStaleEntries()` method that runs daily, deleting entries not seen in 30 days. Integrated into the existing `run()` loop with a counter-based schedule (2880 ticks × 30s = 24h).

---

## Issues Not Fixed (Low/Informational)

| Issue | Severity | Reason Not Fixed |
|-------|----------|------------------|
| `relay/handler.go` misleading indentation | Low | Go uses braces, not indentation. Code compiles and logic is correct. |
| `metatools/handler_test.go` zero DB test coverage | Medium | Requires sqlmock setup; tracked for future improvement. |
| `relay/anthropic_to_chat_request.go` image blocks degraded to text | Medium | Functional but loses image content; tracked for future enhancement. |
| `relay/metatool_interceptor.go` no timeout on DB calls | Medium | Uses parent context which has its own timeout; low risk. |
| `bg/probe_http.go` 65KB read limit | Low | Most model lists fit; can increase if needed. |
| Frontend `any` type usage (9 functions) | Low | Pre-existing pattern, not a regression. |
| `ReportRecentFailures` unimplemented | Low | No callers; dead code. |

---

## Verification

| Check | Result |
|-------|--------|
| `go build ./...` | ✅ PASS |
| `go vet ./...` | ✅ PASS (0 issues) |
| `go test ./...` | ✅ ALL PASS (41 packages) |
| `npm run build` (web/) | ✅ PASS (2.36s) |

---

## Code Quality Summary

| Metric | Value |
|--------|-------|
| New Go files | 12 (relay, bg, metatools, admin, scripts) |
| New Vue files | 2 (CompressionView, DataLifecycleView) |
| New SQL migrations | 3 (019, 020, 021) |
| New test files | 12 |
| Test packages | 41 (all passing) |
| Security issues | 0 (no hardcoded secrets, parameterized SQL) |
| XSS vectors | 0 (Vue auto-escaping, no v-html) |

---

## Architecture Observations

1. **Relay protocol conversion** (Anthropic↔OpenAI) is well-structured with proper tool_use/tool_calls mapping and thinking block extraction.
2. **Passive probe system** (Layer 5) correctly separates model_unavailable from provider_error categories, preventing false-positive model banning.
3. **Meta-tools foundation** is clean — the tools-array interception approach is more reliable than the original assistant-side tool_calls detection.
4. **Data lifecycle management** provides proper cleanup scripts with crontab templates.
5. **Compression overview** gives operators visibility into byte/token savings across the fleet.
