# AUTO_MODEL Optimization V3 - Comprehensive Implementation Plan

**Version**: 3.0  
**Date**: 2026-09-02  
**Status**: Planning  
**Target Cost Reduction**: 30-40%

---

## Executive Summary

This plan optimizes the AUTO_MODEL routing system through:
1. **10-category fine-grained task classification** (vs current 8 categories)
2. **3-tier model selection strategy** (High-performance/Standard/Economy)
3. **Entry/Exit request separation** for better analytics
4. **4-layer queue architecture** for improved observability
5. **Sub-agent depth detection** for cost optimization

**Expected Impact:**
- 30-40% cost reduction through intelligent tier matching
- Improved analytics through entry/exit request separation
- Better queue management with 4-layer visibility
- Enhanced cost control for sub-agent tasks

---

## Table of Contents

1. [Current State Analysis](#1-current-state-analysis)
2. [Task Classification Enhancement](#2-task-classification-enhancement)
3. [Three-Tier Model Strategy](#3-three-tier-model-strategy)
4. [Entry/Exit Request Architecture](#4-entryexit-request-architecture)
5. [Four-Layer Queue System](#5-four-layer-queue-system)
6. [Data Flow Diagrams](#6-data-flow-diagrams)
7. [Database Schema Changes](#7-database-schema-changes)
8. [Implementation Roadmap](#8-implementation-roadmap)
9. [Risk Mitigation](#9-risk-mitigation)
10. [Success Metrics](#10-success-metrics)

---

## 1. Current State Analysis

### 1.1 Existing Architecture Strengths

✅ **Strong Foundation:**
- 8-category task classification in `autoroute/classifier.go`
- Multi-dimensional scoring system (intent, price, quality, reliability)
- 5-minute rolled-up metrics in `credential_model_index`
- Session affinity and cache reuse
- Work-type routing with tier support

✅ **Robust Infrastructure:**
- 4-layer concurrency control (global/pool/credential/identity)
- Weighted credential routing
- Background index refresher
- Comprehensive telemetry logging

### 1.2 Identified Gaps

❌ **Classification Limitations:**
- 8 task categories too coarse (e.g., "code" includes both greenfield coding and code review)
- No sub-agent depth detection
- Limited support for programming sub-tasks (DevOps, documentation, etc.)

❌ **Data Structure Issues:**
- Single `request_logs` table mixes client requests and upstream retries
- No explicit tier field in logs
- Difficult to analyze: "How many client requests?" vs "How many retry attempts?"
- No `auto_decision` JSONB in schema (may exist in production only)

❌ **Queue Visibility:**
- Only 2-layer queue (model + credential)
- No separation of client queue vs outbound queue
- Limited observability of queue depth per task type/tier

❌ **Cost Optimization:**
- No automatic detection of sub-agent tasks for economy model routing
- Task types not mapped to cost tiers systematically

---

## 2. Task Classification Enhancement

### 2.1 New 10-Category System

| **Category** | **Description** | **Key Signals** | **Default Tier** | **Fallback** |
|-------------|-----------------|-----------------|------------------|--------------|
| **architecture** | System design, API design, technical proposals, architecture reviews | "system design", "architecture", "proposal", "design doc", "API design" | **Tier-A** | Tier-B |
| **audit** | Code review, security audit, PR review, vulnerability analysis | "review", "audit", "security", "vulnerability", "PR review" | **Tier-A** | Tier-B |
| **debugging** | Bug investigation, root cause analysis, stack trace debugging | "debug", "why", "error", "stack trace", "bug", "doesn't work" | **Tier-A** | Tier-B |
| **coding** | Greenfield development, feature implementation, API integration | Code blocks + "create", "add", "build", "implement" | **Tier-B** | Tier-A, Tier-C |
| **refactoring** | Code restructuring, optimization, clean-up | "refactor", "optimize", "improve", "clean up", "restructure" | **Tier-B** | Tier-A, Tier-C |
| **testing** | Unit/integration test generation, test coverage | "test", "coverage", "mock", "assert", "jest", "pytest" | **Tier-B** | Tier-C |
| **devops** | CI/CD, deployment, infrastructure scripting, container config | "deploy", "CI/CD", "docker", "k8s", "terraform", "ansible" | **Tier-C** | Tier-B |
| **documentation** | Comments, README, API docs, inline documentation | "document", "comment", "explain", "describe", "README" | **Tier-C** | None |
| **summary** | Code summarization, session recap, overview generation | "summarize", "explain what", "overview", "recap", "TLDR" | **Tier-C** | None |
| **dependency** | Dependency analysis, upgrade planning, package management | "dependency", "upgrade", "package", "version", "npm", "pip" | **Tier-C** | Tier-B |

### 2.2 Classification Flow

```
┌─────────────────────────────────────────────────────────────┐
│ Phase 1: Signal Extraction                                  │
├─────────────────────────────────────────────────────────────┤
│ • Extract system prompt, user prompt                        │
│ • Count messages, tool definitions, images                  │
│ • Detect code blocks, stack traces                          │
│ • Check IDE signature (VSCode/Cursor/JetBrains)             │
│ • Extract X-Gw-Agent-Depth header                           │
└─────────────────┬───────────────────────────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────────────────────────┐
│ Phase 2: Heuristic Classification                           │
├─────────────────────────────────────────────────────────────┤
│ • Keyword matching (strong + weak signals)                  │
│ • Pattern detection (code blocks + imperative verbs)        │
│ • Context analysis (message count, tool usage)              │
│ • Output: Primary task + confidence score (0.0-1.0)         │
└─────────────────┬───────────────────────────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────────────────────────┐
│ Phase 3: Confidence Check                                   │
├─────────────────────────────────────────────────────────────┤
│ IF confidence >= 0.7:                                       │
│   ├─ Accept classification                                  │
│   └─ Proceed to tier selection                              │
│ ELSE:                                                       │
│   ├─ Trigger LLM-based re-classification                    │
│   └─ Use small fast model (glm-5.2-flash)                   │
└─────────────────┬───────────────────────────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────────────────────────┐
│ Phase 4: Tier Determination                                 │
├─────────────────────────────────────────────────────────────┤
│ Step 1: Query task_type_tier_config                         │
│   └─ Get preferred_tier + fallback_tiers                    │
│                                                             │
│ Step 2: Check Sub-agent Depth                               │
│   ├─ IF X-Gw-Agent-Depth = 0: Use task default tier        │
│   ├─ IF X-Gw-Agent-Depth = 1: Use task default tier        │
│   └─ IF X-Gw-Agent-Depth >= 2: Force Tier-C (nested)       │
│                                                             │
│ Step 3: Check Tenant Override                               │
│   └─ tenant_config overrides global default                 │
│                                                             │
│ Step 4: Check Header Override                               │
│   └─ X-Gw-Model-Tier: tier-a/tier-b/tier-c (highest pri)   │
└─────────────────┬───────────────────────────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────────────────────────┐
│ Output: TaskType + Tier + Confidence                        │
│ Example: {                                                  │
│   "task_type": "coding",                                    │
│   "tier": "tier-b",                                         │
│   "confidence": 0.85,                                       │
│   "reasoning": "code blocks + 'implement' keyword"          │
│ }                                                           │
└─────────────────────────────────────────────────────────────┘
```

### 2.3 Sub-agent Depth Strategy

**Depth-based Tier Degradation:**

```
Depth 0 (Primary Client Request):
└─ Use task default tier (architecture → Tier-A)

Depth 1 (First-level Sub-agent):
└─ Use task default tier (allows important sub-tasks to use higher tier)
   Example: Main agent delegates "design API schema" → Tier-A

Depth 2+ (Nested Sub-agents):
└─ Force Tier-C regardless of task type
   Example: Nested agent for "generate README" → Tier-C
   Rationale: Deep nesting = simpler, more focused tasks
```

**Cost Impact:**
- Depth 0-1: Normal cost distribution (20% A, 40% B, 40% C)
- Depth 2+: 100% Tier-C (estimated 10-15% of total requests)
- **Savings from depth optimization alone: ~5-10%**

---

## 3. Three-Tier Model Strategy

### 3.1 Tier Definitions

#### **Tier-A: High-Performance** ($15-50 per 1M tokens)

**Models:**
- claude-opus-5
- gpt-5.6-sol
- glm-5.3
- kimi-m3

**Characteristics:**
- Deep reasoning capability
- Multi-file understanding
- Novel problem-solving
- Long-context handling (200k+ tokens)

**Use Cases:**
- Architecture design
- Security audits
- Complex debugging
- Production incident response

**Expected Volume:** 20% of requests

---

#### **Tier-B: Standard** ($5-15 per 1M tokens)

**Models:**
- claude-opus-4.8
- gpt-4.9
- glm-5.2
- deepseek-v4-pro
- minimax-m3

**Characteristics:**
- Balanced speed/quality
- Good instruction following
- Single-file mastery
- Standard patterns

**Use Cases:**
- Feature implementation
- Code refactoring
- Test generation
- Standard bug fixes

**Expected Volume:** 40% of requests

---

#### **Tier-C: Economy** ($0.5-5 per 1M tokens)

**Models:**
- minimax-m2.7
- glm-5.2-flash
- deepseek-v4-flash
- local/minimax-m3

**Characteristics:**
- Fast response (<2s)
- Template-based work
- High throughput
- Deterministic outputs

**Use Cases:**
- DevOps scripting
- Documentation generation
- Code summarization
- Dependency management
- All nested sub-agents (depth ≥ 2)

**Expected Volume:** 40% of requests

---

### 3.2 Cost Analysis

**Current State (No Tier Optimization):**
```
100% requests × $20/1M avg = $20 per 1M tokens
```

**Optimized State (Tier-based Routing):**
```
20% × $30/1M (Tier-A) = $6.00
40% × $10/1M (Tier-B) = $4.00
40% × $2/1M  (Tier-C) = $0.80
─────────────────────────
Total = $10.80 per 1M tokens
```

**Savings: 46% cost reduction**

**Conservative Estimate (accounting for escalations):**
- 5% of Tier-C tasks escalate to Tier-B
- 2% of Tier-B tasks escalate to Tier-A
- **Net savings: 35-40%**

---

### 3.3 Quality Gates & Auto-Escalation

**Escalation Triggers:**

1. **Compilation Failure** (for code generation tasks)
   - If Tier-C generates code that doesn't compile → retry with Tier-B
   - If Tier-B fails → escalate to Tier-A

2. **Test Failure** (for test generation tasks)
   - If generated tests fail to run → escalate one tier

3. **Low Confidence** (classifier confidence < 0.5)
   - Start with Tier-A to avoid quality issues

4. **User Feedback** (explicit dissatisfaction)
   - User says "try again with better model" → escalate one tier
   - Track via X-Gw-Retry-Reason header

**Escalation Flow:**
```
Tier-C attempt → Quality gate failure
    ↓
Tier-B attempt → Quality gate failure
    ↓
Tier-A attempt → Final attempt (no further escalation)
```

**Cost Protection:**
- Max 2 escalations per request
- Escalation tracked in `request_logs.escalation_count`
- Alert if escalation rate > 15% (indicates misclassification)

---

## 4. Entry/Exit Request Architecture

### 4.1 Problem Statement

**Current Issue:**
```sql
-- Cannot distinguish client requests from retries
SELECT COUNT(*) FROM request_logs WHERE client_model = 'auto';
-- Returns: 10,000 (but includes 3,000 retries!)

-- Cannot calculate true client request volume
SELECT COUNT(DISTINCT session_id) FROM request_logs;
-- Inaccurate: same session may have multiple requests
```

**Root Cause:**
- `request_logs` table stores both:
  1. Entry requests (what client asked for)
  2. Exit requests (what we sent to providers, including retries)
- No clear separation → analytics confusion

### 4.2 Solution: Request Type Classification

**Add metadata fields to distinguish request types:**

```sql
ALTER TABLE request_logs ADD COLUMN request_type TEXT DEFAULT 'outbound';
-- 'client' = entry request from client
-- 'outbound' = exit request to provider

ALTER TABLE request_logs ADD COLUMN parent_request_id TEXT;
-- Links child requests to parent

ALTER TABLE request_logs ADD COLUMN request_depth INTEGER DEFAULT 0;
-- 0 = client entry, 1 = first-level sub-agent, 2 = nested

ALTER TABLE request_logs ADD COLUMN is_terminal BOOLEAN DEFAULT FALSE;
-- TRUE = final successful/failed attempt (stores response)

ALTER TABLE request_logs ADD COLUMN task_tier TEXT;
-- 'tier-a' | 'tier-b' | 'tier-c'

ALTER TABLE request_logs ADD COLUMN auto_decision JSONB;
-- Full decision metadata: candidates, scores, reasoning

ALTER TABLE request_logs ADD COLUMN escalation_count INTEGER DEFAULT 0;
-- Number of tier escalations for this request
```

### 4.3 Data Relationship Pattern

```
CLIENT REQUEST (Entry)
├─ request_id: req_client_001
├─ request_type: 'client'
├─ request_depth: 0
├─ parent_request_id: NULL
├─ session_id: sess_abc
├─ client_model: 'auto'
├─ task_type: 'coding'
├─ task_tier: 'tier-b'
├─ request_body: {"messages": [{"role":"user","content":"Fix bug in handler"}]}
└─ auto_decision: {
      "task_type": "coding",
      "confidence": 0.85,
      "tier": "tier-b",
      "candidates": [...],
      "chosen_model": "glm-5.2"
    }

    ├─────────────────────────────────────────────┐
    │                                             │
    ▼                                             ▼
OUTBOUND REQUEST 1 (Retry 1)               OUTBOUND REQUEST 2 (Success)
├─ request_id: req_out_002                 ├─ request_id: req_out_003
├─ request_type: 'outbound'                ├─ request_type: 'outbound'
├─ request_depth: 1                        ├─ request_depth: 1
├─ parent_request_id: req_client_001       ├─ parent_request_id: req_client_001
├─ upstream_model: 'deepseek-v4-pro'       ├─ upstream_model: 'glm-5.2'
├─ credential_id: 123                      ├─ credential_id: 456
├─ task_tier: 'tier-b'                     ├─ task_tier: 'tier-b'
├─ is_terminal: FALSE                      ├─ is_terminal: TRUE ✓
├─ status_code: 429                        ├─ status_code: 200
├─ error_message: 'rate_limit_exceeded'    ├─ latency_ms: 2300
├─ request_body: {                         ├─ request_body: {
│     [full assembled prompt with           │     [full assembled prompt]
│      session history]                     │   }
│   }                                       ├─ response_body: {
├─ response_body: NULL                      │     [full response] ✓
└─ escalation_count: 0                      │   }
                                           └─ escalation_count: 0

    ├─────────────────────────────────────┐
    │                                     │
    ▼                                     ▼
SUB-AGENT REQUEST                   NESTED SUB-AGENT
├─ request_type: 'client'           ├─ request_type: 'client'
├─ request_depth: 1                 ├─ request_depth: 2
├─ parent_request_id: req_client_001├─ parent_request_id: req_client_004
├─ task_type: 'documentation'       ├─ task_type: 'summary'
├─ task_tier: 'tier-c'              ├─ task_tier: 'tier-c' (forced)
└─ (follows same pattern)           └─ (follows same pattern)
```

### 4.4 Storage Optimization Strategy

**Configurable Retention Policy:**

```
Phase 1: Hot Storage (0-7 days)
├─ ALL records stored: client + outbound (terminal + non-terminal)
├─ Full request_body + response_body for all
└─ Purpose: Real-time debugging, failure analysis

Phase 2: Warm Storage (8-30 days)
├─ Client records: Keep all fields
├─ Outbound records:
│   ├─ Terminal (is_terminal=TRUE): Keep all fields
│   └─ Non-terminal (is_terminal=FALSE): Prune request_body, keep metadata
└─ Purpose: Cost analysis, trend analysis

Phase 3: Cold Storage (31-90 days)
├─ Client records: Keep metadata only, compress response_body
├─ Outbound records: Keep only terminal records
└─ Purpose: Long-term compliance, audit trail

Phase 4: Archive (90+ days)
└─ Aggregate statistics only, delete individual records
```

**Storage Savings:**
- Without optimization: 100% full storage = 10 GB/day
- With optimization: ~40% storage (hot phase + selective retention)
- **Savings: 60% storage cost reduction**

### 4.5 Analytics Benefits

**Before (Mixed Data):**
```sql
-- Impossible to answer: "How many client requests yesterday?"
SELECT COUNT(*) FROM request_logs WHERE ts >= NOW() - INTERVAL '1 day';
-- Result: 100,000 (includes 30,000 retries - meaningless!)
```

**After (Separated Data):**
```sql
-- Clear client request count
SELECT COUNT(*) FROM request_logs 
WHERE request_type = 'client' AND ts >= NOW() - INTERVAL '1 day';
-- Result: 70,000 ✓ (true client volume)

-- Retry rate analysis
SELECT 
  parent_request_id,
  COUNT(*) as retry_count
FROM request_logs
WHERE request_type = 'outbound'
GROUP BY parent_request_id
HAVING COUNT(*) > 1;
-- Shows: req_client_001 had 2 retries, req_client_005 had 3 retries

-- Cost per tier
SELECT 
  task_tier,
  SUM(total_tokens * unit_price_in) as total_cost
FROM request_logs
WHERE is_terminal = TRUE AND ts >= NOW() - INTERVAL '1 day'
GROUP BY task_tier;
-- Result:
-- tier-a: $50.00
-- tier-b: $120.00
-- tier-c: $30.00
```

---

## 5. Four-Layer Queue System

### 5.1 Queue Architecture

**Current: 2-Layer System**
```
Layer 1: Model Queue
└─ Per-model FIFO queue
   └─ Feeds into credential selection

Layer 2: Credential Queue
└─ Per-credential rate-limited queue
   └─ Governor controls admission
```

**Limitations:**
- No visibility into client request backlog
- Cannot distinguish client wait time vs retry wait time
- No tier-based queue segmentation

---

**New: 4-Layer System**

```
┌────────────────────────────────────────────────────────────┐
│ Layer 0: CLIENT REQUEST QUEUE                              │
├────────────────────────────────────────────────────────────┤
│ Scope: Only client-facing requests (request_type='client') │
│ Capacity: 1000 concurrent requests                         │
│ Purpose: Prevent client starvation by internal retries     │
│ Metrics:                                                   │
│   • dispatch_client_queue_depth{tenant_id}                 │
│   • dispatch_client_wait_time_p95{tenant_id}               │
│   • dispatch_client_rejected_total{tenant_id}              │
└────────────────────────────────────────────────────────────┘
                         │
                         ▼
┌────────────────────────────────────────────────────────────┐
│ Layer 1: OUTBOUND REQUEST QUEUE                            │
├────────────────────────────────────────────────────────────┤
│ Scope: All upstream attempts (including retries/escalations│
│ Capacity: 5000 concurrent requests                         │
│ Purpose: Aggregate view of all provider load               │
│ Metrics:                                                   │
│   • dispatch_outbound_queue_depth{tenant_id}               │
│   • dispatch_retry_ratio{tenant_id}                        │
│   • dispatch_escalation_rate{tenant_id, tier}              │
└────────────────────────────────────────────────────────────┘
                         │
                         ▼
┌────────────────────────────────────────────────────────────┐
│ Layer 2: MODEL QUEUE (Enhanced with Tier Segmentation)    │
├────────────────────────────────────────────────────────────┤
│ Scope: Per-model queues segmented by task_type + tier      │
│ Example:                                                   │
│   • glm-5.2:coding:tier-b                                  │
│   • minimax-m3:documentation:tier-c                        │
│   • claude-opus-5:architecture:tier-a                      │
│ Purpose: Fair scheduling across tiers and task types       │
│ Metrics:                                                   │
│   • dispatch_model_queue_depth{model, task_type, tier}     │
│   • dispatch_tier_queue_depth{tier}                        │
│   • dispatch_task_type_queue_depth{task_type}              │
└────────────────────────────────────────────────────────────┘
                         │
                         ▼
┌────────────────────────────────────────────────────────────┐
│ Layer 3: CREDENTIAL QUEUE (Existing, Enhanced Metrics)    │
├────────────────────────────────────────────────────────────┤
│ Scope: Per-credential queues with rate limiting            │
│ Governor: RPM/TPM/concurrency limits                       │
│ Purpose: Respect provider rate limits                      │
│ Metrics:                                                   │
│   • dispatch_credential_queue_depth{credential_id}         │
│   • dispatch_credential_pressure_ratio{credential_id}      │
│   • dispatch_credential_throttle_events{credential_id}     │
└────────────────────────────────────────────────────────────┘
```

### 5.2 Queue Flow Diagram

```
Client Request
     │
     ▼
┌─────────────────────┐
│ Layer 0: Client Q   │  ◄─── IF full: reject with 429
│ Capacity: 1000      │       (protect server resources)
└──────────┬──────────┘
           │
           ▼ (Classification + Tier Selection)
┌─────────────────────┐
│ Layer 1: Outbound Q │  ◄─── IF full: backpressure to client queue
│ Capacity: 5000      │       (slow down admission)
└──────────┬──────────┘
           │
           ▼ (Model Selection)
┌─────────────────────┐
│ Layer 2: Model Q    │  ◄─── Segmented by tier + task_type
│ Per-model sub-queues│       Fair scheduling across segments
└──────────┬──────────┘
           │
           ▼ (Credential Selection)
┌─────────────────────┐
│ Layer 3: Credential │  ◄─── Rate limiting + concurrency control
│ Per-cred governors  │       Back-pressure on queue full
└──────────┬──────────┘
           │
           ▼
     Upstream Call
```

### 5.3 Queue Metrics & Alerts

**Prometheus Metrics:**

```prometheus
# Layer 0: Client Queue
dispatch_client_queue_depth{tenant_id="default"} 245
dispatch_client_wait_time_seconds{tenant_id="default",quantile="0.95"} 1.2
dispatch_client_rejected_total{tenant_id="default"} 15

# Layer 1: Outbound Queue
dispatch_outbound_queue_depth{tenant_id="default"} 1250
dispatch_retry_ratio{tenant_id="default"} 0.15  # 15% of outbound requests are retries
dispatch_escalation_rate{tenant_id="default",from_tier="tier-c",to_tier="tier-b"} 0.08

# Layer 2: Model Queue
dispatch_model_queue_depth{model="glm-5.2",task_type="coding",tier="tier-b"} 45
dispatch_tier_queue_depth{tier="tier-a"} 120
dispatch_tier_queue_depth{tier="tier-b"} 380
dispatch_tier_queue_depth{tier="tier-c"} 750

# Layer 3: Credential Queue
dispatch_credential_queue_depth{credential_id="123"} 8
dispatch_credential_pressure_ratio{credential_id="123"} 0.85  # 85% of concurrency limit
```

**Alerting Rules:**

```yaml
groups:
  - name: dispatch_queue_alerts
    rules:
      - alert: ClientQueueSaturated
        expr: dispatch_client_queue_depth > 800
        for: 5m
        annotations:
          summary: "Client queue at 80% capacity"
          
      - alert: HighRetryRatio
        expr: dispatch_retry_ratio > 0.25
        for: 10m
        annotations:
          summary: "Retry ratio > 25%, investigate credential health"
          
      - alert: TierCostSpike
        expr: rate(dispatch_tier_cost_usd{tier="tier-a"}[5m]) > 100
        for: 3m
        annotations:
          summary: "Tier-A cost spike: $100/hour rate"
```

---

## 6. Data Flow Diagrams

### 6.1 End-to-End Request Flow

```
┌──────────────────────────────────────────────────────────────┐
│ 1. CLIENT REQUEST                                            │
│    POST /v1/chat/completions                                 │
│    Body: {"model": "auto", "messages": [...]}                │
│    Headers: X-Gw-Agent-Depth: 0                              │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 2. STREAMING HANDLER (domains/streaming/handler.go)         │
│    • Extract classification signals                          │
│    • Check Layer-0 client queue capacity                     │
│    • IF full: return 429 Too Many Requests                   │
│    • ELSE: admit to client queue                             │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 3. AUTO-ROUTE CLASSIFIER (autoroute/classifier.go)          │
│    • Classify() → 10-category classification                 │
│    • Confidence check (>= 0.7 accept, < 0.7 LLM re-classify) │
│    • Output: TaskType + Confidence                           │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 4. TIER DETERMINATION                                        │
│    • Query task_type_tier_config (preferred + fallback)      │
│    • Check agent depth:                                      │
│      - depth 0-1: Use task default tier                      │
│      - depth >= 2: Force tier-c                              │
│    • Check tenant override                                   │
│    • Check header override (X-Gw-Model-Tier)                 │
│    • Output: Tier (tier-a/tier-b/tier-c)                     │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 5. CREATE ENTRY LOG (NEW)                                   │
│    INSERT INTO request_logs (                                │
│      request_id = gen_request_id(),                          │
│      request_type = 'client',                                │
│      request_depth = X-Gw-Agent-Depth,                       │
│      parent_request_id = X-Gw-Parent-Request-Id,             │
│      client_model = 'auto',                                  │
│      task_type = classified_task,                            │
│      task_tier = determined_tier,                            │
│      auto_decision = decision_json,                          │
│      request_body = client_request                           │
│    )                                                         │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 6. CANDIDATE RECOMMENDATION (autoroute/recommend_v2.go)      │
│    • Query credential_model_index                            │
│    • Filter by tier (same tier first, fallback second)       │
│    • Filter by availability (cmb.available = TRUE)           │
│    • Hot Top-3 canonicals from 48h usage                     │
│    • Output: Candidate pool                                  │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 7. SCORING & SELECTION (autoroute/scoring.go)               │
│    • Score each candidate (multi-dimensional)                │
│    • Apply tier policy boost                                 │
│    • Apply correction score (session cache affinity)         │
│    • Sort by composite score                                 │
│    • Select top candidate                                    │
│    • Output: ChosenModel + ChosenCredentialID                │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 8. MODEL SUBSTITUTION                                        │
│    • Replace request body "model" = "auto"                   │
│      → "model" = ChosenModel                                 │
│    • Add response headers:                                   │
│      X-Gw-Auto-Decision: <decision_json>                     │
│      X-Gw-Task-Type: coding                                  │
│      X-Gw-Task-Tier: tier-b                                  │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 9. CREATE OUTBOUND LOG (NEW)                                │
│    INSERT INTO request_logs (                                │
│      request_id = gen_outbound_id(),                         │
│      request_type = 'outbound',                              │
│      request_depth = depth + 1,                              │
│      parent_request_id = client_request_id,                  │
│      upstream_model = ChosenModel,                           │
│      credential_id = ChosenCredentialID,                     │
│      task_tier = determined_tier,                            │
│      is_terminal = FALSE,  # updated on completion           │
│      request_body = assembled_full_prompt                    │
│    )                                                         │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 10. DISPATCH PIPELINE (domains/dispatch/pipeline.go)        │
│     • Enqueue to Layer-1 outbound queue                      │
│     • Enqueue to Layer-2 model queue (tier-segmented)        │
│     • runDispatcher() picks from model queue                 │
│     • Credential selection via weighted_router               │
│     • Enqueue to Layer-3 credential queue                    │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 11. CREDENTIAL LIMITER (domains/credential/limiter.go)      │
│     • 4-layer concurrency control                            │
│     • Weighted semaphore admission                           │
│     • FP slot allocation                                     │
│     • Rate limiting (RPM/TPM)                                │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 12. UPSTREAM CALL                                            │
│     • HTTP POST to provider API                              │
│     • Streaming response handling                            │
│     • Error handling (rate limit, timeout, etc.)             │
└─────────────────────┬────────────────────────────────────────┘
                      │
            ┌─────────┴─────────┐
            │                   │
            ▼ (SUCCESS)         ▼ (FAILURE)
┌────────────────────┐  ┌────────────────────────┐
│ 13a. SUCCESS PATH  │  │ 13b. FAILURE PATH      │
│ • status_code: 200 │  │ • status_code: 429/500 │
│ • Store response   │  │ • Check quality gate   │
│ • UPDATE request:  │  │ • IF escalate:         │
│   is_terminal=TRUE │  │   → Go back to step 6  │
│                    │  │      with higher tier  │
│                    │  │ • ELSE:                │
│                    │  │   → Mark terminal=TRUE │
│                    │  │   → Return error       │
└────────────────────┘  └────────────────────────┘
            │                   │
            └─────────┬─────────┘
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ 14. TELEMETRY UPDATE                                         │
│     UPDATE request_logs SET                                  │
│       is_terminal = TRUE,                                    │
│       status_code = result_status,                           │
│       latency_ms = duration,                                 │
│       prompt_tokens = usage.prompt,                          │
│       completion_tokens = usage.completion,                  │
│       response_body = final_response                         │
│     WHERE request_id = outbound_request_id;                  │
└──────────────────────────────────────────────────────────────┘
```

### 6.2 Quality Gate & Escalation Flow

```
┌──────────────────────────────────────────────────────────────┐
│ Tier-C Attempt                                               │
│ Model: minimax-m3                                            │
└─────────────────────┬────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────────────────────┐
│ Quality Gate Check                                           │
│ • Compilation check (for code tasks)                         │
│ • Test execution (for test generation tasks)                 │
│ • Confidence score check                                     │
└─────────────────────┬────────────────────────────────────────┘
                      │
            ┌─────────┴─────────┐
            │                   │
            ▼ (PASS)            ▼ (FAIL)
┌────────────────────┐  ┌────────────────────────────────────┐
│ Return Response    │  │ Escalate to Tier-B                 │
│ • is_terminal=TRUE │  │ • escalation_count++               │
│ • Success          │  │ • Create new outbound record       │
└────────────────────┘  │ • tier = 'tier-b'                  │
                        │ • parent = same client_request_id  │
                        └─────────┬──────────────────────────┘
                                  │
                                  ▼
                        ┌────────────────────────────────────┐
                        │ Tier-B Attempt                     │
                        │ Model: glm-5.2                     │
                        └─────────┬──────────────────────────┘
                                  │
                                  ▼
                        ┌────────────────────────────────────┐
                        │ Quality Gate Check                 │
                        └─────────┬──────────────────────────┘
                                  │
                        ┌─────────┴─────────┐
                        │                   │
                        ▼ (PASS)            ▼ (FAIL)
                ┌────────────────┐  ┌──────────────────────┐
                │ Return Response│  │ Escalate to Tier-A   │
                └────────────────┘  │ • escalation_count++ │
                                    │ • Final attempt      │
                                    └─────────┬────────────┘
                                              │
                                              ▼
                                    ┌──────────────────────┐
                                    │ Tier-A Attempt       │
                                    │ • No further escalate│
                                    │ • Return regardless  │
                                    └──────────────────────┘
```

---

## 7. Database Schema Changes

### 7.1 Migration Scripts

#### **Migration 1: Add Request Type Fields**

```sql
-- File: sql/migrations/202609_01_add_request_type_fields.sql

BEGIN;

-- Add request classification fields
ALTER TABLE request_logs 
    ADD COLUMN IF NOT EXISTS request_type TEXT DEFAULT 'outbound',
    ADD COLUMN IF NOT EXISTS parent_request_id TEXT,
    ADD COLUMN IF NOT EXISTS request_depth INTEGER DEFAULT 0,
    ADD COLUMN IF NOT EXISTS is_terminal BOOLEAN DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS task_tier TEXT,
    ADD COLUMN IF NOT EXISTS auto_decision JSONB,
    ADD COLUMN IF NOT EXISTS escalation_count INTEGER DEFAULT 0;

-- Add comments
COMMENT ON COLUMN request_logs.request_type IS 
    'Request type: ''client'' (entry from client) or ''outbound'' (to provider)';
COMMENT ON COLUMN request_logs.parent_request_id IS 
    'Parent request ID for sub-agents and retries';
COMMENT ON COLUMN request_logs.request_depth IS 
    '0=client entry, 1=first-level sub-agent, 2+=nested sub-agents';
COMMENT ON COLUMN request_logs.is_terminal IS 
    'TRUE if this is the final successful/failed attempt (stores response_body)';
COMMENT ON COLUMN request_logs.task_tier IS 
    'Tier classification: ''tier-a'', ''tier-b'', or ''tier-c''';
COMMENT ON COLUMN request_logs.auto_decision IS 
    'Full auto-route decision metadata: candidates, scores, reasoning';
COMMENT ON COLUMN request_logs.escalation_count IS 
    'Number of tier escalations for this request (quality gate failures)';

-- Create indexes for new query patterns
CREATE INDEX IF NOT EXISTS idx_request_logs_request_type_ts 
    ON request_logs(request_type, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_parent_request_id 
    ON request_logs(parent_request_id) 
    WHERE parent_request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_task_tier_ts 
    ON request_logs(task_tier, ts DESC) 
    WHERE task_tier IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_terminal 
    ON request_logs(is_terminal, ts DESC) 
    WHERE is_terminal = TRUE;

-- Backfill existing data (mark as outbound, terminal by default)
UPDATE request_logs 
SET 
    request_type = 'outbound',
    is_terminal = TRUE,
    request_depth = 0
WHERE request_type IS NULL;

COMMIT;
```

#### **Migration 2: Create Task Type Tier Config Table**

```sql
-- File: sql/migrations/202609_02_create_task_type_tier_config.sql

BEGIN;

CREATE TABLE IF NOT EXISTS task_type_tier_config (
    id SERIAL PRIMARY KEY,
    task_type TEXT NOT NULL,
    preferred_tier TEXT NOT NULL CHECK (preferred_tier IN ('tier-a', 'tier-b', 'tier-c')),
    fallback_tiers TEXT[] DEFAULT ARRAY[]::TEXT[],
    min_confidence DECIMAL(3,2) DEFAULT 0.70 CHECK (min_confidence >= 0 AND min_confidence <= 1),
    tenant_id TEXT, -- NULL = global default
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(task_type, COALESCE(tenant_id, ''))
);

CREATE INDEX idx_task_type_tier_config_lookup 
    ON task_type_tier_config(task_type, tenant_id, enabled);

COMMENT ON TABLE task_type_tier_config IS 
    'Maps task types to preferred model tiers with fallback options';

-- Insert default configuration
INSERT INTO task_type_tier_config (task_type, preferred_tier, fallback_tiers, min_confidence) VALUES
('architecture', 'tier-a', ARRAY['tier-b'], 0.70),
('audit', 'tier-a', ARRAY['tier-b'], 0.70),
('debugging', 'tier-a', ARRAY['tier-b'], 0.65),
('coding', 'tier-b', ARRAY['tier-a', 'tier-c'], 0.75),
('refactoring', 'tier-b', ARRAY['tier-a', 'tier-c'], 0.70),
('testing', 'tier-b', ARRAY['tier-c'], 0.75),
('devops', 'tier-c', ARRAY['tier-b'], 0.80),
('documentation', 'tier-c', ARRAY[]::TEXT[], 0.85),
('summary', 'tier-c', ARRAY[]::TEXT[], 0.85),
('dependency', 'tier-c', ARRAY['tier-b'], 0.75)
ON CONFLICT (task_type, COALESCE(tenant_id, '')) DO NOTHING;

COMMIT;
```

#### **Migration 3: Add Tier to Provider Models**

```sql
-- File: sql/migrations/202609_03_add_tier_to_provider_models.sql

BEGIN;

-- Add tier field to provider_models
ALTER TABLE provider_models 
    ADD COLUMN IF NOT EXISTS tier TEXT CHECK (tier IN ('tier-a', 'tier-b', 'tier-c'));

CREATE INDEX IF NOT EXISTS idx_provider_models_tier 
    ON provider_models(tier) 
    WHERE tier IS NOT NULL;

COMMENT ON COLUMN provider_models.tier IS 
    'Model tier classification: tier-a (high-perf), tier-b (standard), tier-c (economy)';

-- Classify existing models (adjust based on actual canonical names)
UPDATE provider_models SET tier = 'tier-a' 
WHERE canonical_name IN (
    'claude-opus-5', 
    'gpt-5.6-sol', 
    'glm-5.3', 
    'kimi-m3'
);

UPDATE provider_models SET tier = 'tier-b' 
WHERE canonical_name IN (
    'claude-opus-4.8',
    'gpt-4.9',
    'glm-5.2',
    'deepseek-v4-pro',
    'minimax-m3'
);

UPDATE provider_models SET tier = 'tier-c' 
WHERE canonical_name IN (
    'minimax-m2.7',
    'glm-5.2-flash',
    'deepseek-v4-flash',
    'local/minimax-m3'
);

COMMIT;
```

### 7.2 Schema Validation Queries

```sql
-- Verify request type separation
SELECT 
    request_type,
    COUNT(*) as count,
    COUNT(DISTINCT session_id) as unique_sessions
FROM request_logs
WHERE ts >= NOW() - INTERVAL '1 day'
GROUP BY request_type;

-- Expected output:
-- request_type | count  | unique_sessions
-- client       | 70000  | 15000
-- outbound     | 95000  | 0 (outbound doesn't have unique sessions)

-- Verify tier distribution
SELECT 
    task_tier,
    COUNT(*) as count,
    SUM(total_tokens)::BIGINT as total_tokens,
    ROUND(AVG(latency_ms)) as avg_latency_ms
FROM request_logs
WHERE is_terminal = TRUE AND ts >= NOW() - INTERVAL '1 day'
GROUP BY task_tier
ORDER BY task_tier;

-- Verify parent-child relationships
SELECT 
    parent_request_id,
    COUNT(*) as retry_count,
    ARRAY_AGG(upstream_model ORDER BY created_at) as models_tried
FROM request_logs
WHERE request_type = 'outbound' 
  AND parent_request_id IS NOT NULL
  AND ts >= NOW() - INTERVAL '1 day'
GROUP BY parent_request_id
HAVING COUNT(*) > 1
LIMIT 10;
```

---

## 8. Implementation Roadmap

### Phase 1: Database Foundation (Week 1)
**Goal: Prepare data layer for new architecture**

**Tasks:**
- [ ] Write and review all migration scripts
- [ ] Test migrations in local dev environment
- [ ] Run migrations on postgres-252 (dev database)
- [ ] Validate indexes with EXPLAIN ANALYZE
- [ ] Create rollback scripts
- [ ] Document schema changes

**Deliverables:**
- `sql/migrations/202609_01_add_request_type_fields.sql`
- `sql/migrations/202609_02_create_task_type_tier_config.sql`
- `sql/migrations/202609_03_add_tier_to_provider_models.sql`
- Migration validation report

**Success Criteria:**
- ✓ All migrations run without errors
- ✓ Query performance validated (no regression)
- ✓ Rollback tested successfully

---

### Phase 2: Enhanced Classification (Week 2)
**Goal: Implement 10-category classification with tier determination**

**Tasks:**
- [ ] Extend `autoroute/classifier.go`:
  - Add 10 new task categories
  - Implement enhanced keyword matching
  - Add confidence threshold logic
  - Add LLM fallback classifier
- [ ] Create `autoroute/tier_selector.go`:
  - Implement tier determination logic
  - Query `task_type_tier_config` table
  - Handle depth-based degradation
  - Support tenant overrides
- [ ] Unit tests for classification:
  - Test all 10 categories
  - Test confidence thresholds
  - Test sub-agent depth detection
- [ ] Integration tests:
  - End-to-end classification flow
  - Tier selection validation

**Deliverables:**
- `autoroute/classifier.go` (updated)
- `autoroute/task_types.go` (new constants)
- `autoroute/tier_selector.go` (new)
- `autoroute/classifier_test.go` (expanded)
- `autoroute/tier_selector_test.go` (new)

**Success Criteria:**
- ✓ Classification accuracy >= 85% on test dataset
- ✓ All unit tests pass
- ✓ Tier selection validated for all scenarios

---

### Phase 3: Tier-Based Routing (Week 2-3)
**Goal: Update recommendation and scoring to respect tiers**

**Tasks:**
- [ ] Update `autoroute/recommend_v2.go`:
  - Filter candidates by tier
  - Implement fallback tier logic
  - Add tier-based scoring boost
- [ ] Update `autoroute/scoring.go`:
  - Add tier preference to scoring
  - Adjust weights for tier matching
- [ ] Update `autoroute/decision.go`:
  - Integrate tier selector
  - Pass tier to recommendation
  - Store tier in decision metadata
- [ ] Integration tests:
  - Test tier filtering
  - Test fallback tier selection
  - Validate scoring with tier boost

**Deliverables:**
- `autoroute/recommend_v2.go` (updated)
- `autoroute/scoring.go` (updated)
- `autoroute/decision.go` (updated)
- Integration test suite

**Success Criteria:**
- ✓ Tier-based routing functional
- ✓ Fallback tiers work correctly
- ✓ No regression in model selection quality

---

### Phase 4: Entry/Exit Request Separation (Week 3)
**Goal: Implement dual logging for client and outbound requests**

**Tasks:**
- [ ] Update `domains/streaming/handler.go`:
  - Create client entry log on request admission
  - Store client request body and auto_decision
  - Pass client_request_id to dispatch pipeline
- [ ] Update `domains/dispatch/pipeline.go`:
  - Create outbound log on dispatch
  - Link via parent_request_id
  - Mark is_terminal on completion
- [ ] Update `domains/hooks/observability/telemetry/request_logger.go`:
  - Support request_type distinction
  - Handle dual-write pattern
  - Implement configurable retention
- [ ] Add request depth tracking:
  - Read X-Gw-Agent-Depth header
  - Pass depth through pipeline
  - Apply depth-based tier override
- [ ] Integration tests:
  - Validate parent-child linking
  - Test retry scenarios
  - Verify terminal marking

**Deliverables:**
- `domains/streaming/handler.go` (updated)
- `domains/dispatch/pipeline.go` (updated)
- `domains/hooks/observability/telemetry/request_logger.go` (updated)
- Data integrity validation script

**Success Criteria:**
- ✓ Client and outbound logs correctly separated
- ✓ Parent-child relationships accurate
- ✓ Terminal flag set correctly
- ✓ No data loss or duplication

---

### Phase 5: Four-Layer Queue Architecture (Week 4)
**Goal: Add Layer-0 and Layer-1 queues, segment Layer-2 by tier**

**Tasks:**
- [ ] Create `domains/dispatch/client_queue.go`:
  - Implement Layer-0 admission control
  - Capacity: 1000 concurrent clients
  - Backpressure on saturation
- [ ] Create `domains/dispatch/outbound_queue.go`:
  - Implement Layer-1 outbound tracking
  - Capacity: 5000 concurrent outbound
  - Retry ratio tracking
- [ ] Update `domains/dispatch/pipeline.go`:
  - Integrate client queue admission
  - Track outbound queue depth
  - Segment model queues by tier
- [ ] Add Prometheus metrics:
  - `dispatch_client_queue_depth`
  - `dispatch_outbound_queue_depth`
  - `dispatch_tier_queue_depth`
  - `dispatch_escalation_rate`
- [ ] Load testing:
  - Test queue behavior under load
  - Validate backpressure
  - Tune capacity limits

**Deliverables:**
- `domains/dispatch/client_queue.go` (new)
- `domains/dispatch/outbound_queue.go` (new)
- `domains/dispatch/pipeline.go` (updated)
- `domains/hooks/observability/metrics/dispatch_metrics.go` (updated)
- Load testing report

**Success Criteria:**
- ✓ 4-layer queue operational
- ✓ Queue depth metrics accurate
- ✓ Backpressure works correctly
- ✓ No queue deadlocks under load

---

### Phase 6: Quality Gates & Escalation (Week 4-5)
**Goal: Implement automatic tier escalation on quality failures**

**Tasks:**
- [ ] Create `autoroute/quality_gate.go`:
  - Implement compilation check (for code tasks)
  - Implement test execution check
  - Confidence threshold check
  - Escalation decision logic
- [ ] Update `domains/dispatch/pipeline.go`:
  - Integrate quality gate check
  - Implement retry with higher tier
  - Track escalation_count
  - Max escalation limit (2 attempts)
- [ ] Add escalation metrics:
  - `dispatch_escalation_total{from_tier, to_tier}`
  - `dispatch_quality_gate_failure_total{tier, reason}`
- [ ] Integration tests:
  - Test escalation flow
  - Validate max escalation limit
  - Test quality gate triggers

**Deliverables:**
- `autoroute/quality_gate.go` (new)
- `domains/dispatch/pipeline.go` (updated with escalation)
- Escalation metrics
- Quality gate test suite

**Success Criteria:**
- ✓ Quality gates functional
- ✓ Escalation works correctly
- ✓ Max escalation enforced
- ✓ Escalation rate < 15%

---

### Phase 7: Observability & Monitoring (Week 5)
**Goal: Comprehensive dashboards and alerting**

**Tasks:**
- [ ] Create Grafana dashboard:
  - Client vs outbound queue depth
  - Tier distribution (request count + cost)
  - Task type distribution
  - Escalation rate per tier
  - Retry ratio
  - Cost per tier per hour
- [ ] Configure Prometheus alerts:
  - Client queue saturation
  - High retry ratio
  - Tier cost spike
  - Escalation rate spike
- [ ] Create cost analysis queries:
  - Cost per tier per day
  - Cost savings vs baseline
  - Tier distribution trends
- [ ] Documentation:
  - Dashboard usage guide
  - Alert response playbook
  - Cost optimization guide

**Deliverables:**
- Grafana dashboard JSON
- Prometheus alerting rules
- Cost analysis SQL queries
- Observability documentation

**Success Criteria:**
- ✓ Dashboard shows real-time metrics
- ✓ Alerts fire correctly
- ✓ Cost tracking accurate

---

### Phase 8: Shadow Mode & Validation (Week 6)
**Goal: Dual-write data without changing routing behavior**

**Tasks:**
- [ ] Enable shadow mode:
  - Write new fields (request_type, tier, etc.)
  - Continue using old routing logic
  - Log what tier *would* have been selected
- [ ] Collect shadow data for 7 days:
  - Validate classification accuracy
  - Analyze tier distribution
  - Estimate cost impact
- [ ] Manual validation:
  - Sample 100 requests
  - Verify task classification correctness
  - Check tier assignment logic
- [ ] Tuning:
  - Adjust classification keywords
  - Tune tier thresholds
  - Refine fallback logic

**Deliverables:**
- Shadow mode validation report
- Classification accuracy metrics
- Tier distribution analysis
- Tuning recommendations

**Success Criteria:**
- ✓ Classification accuracy >= 85%
- ✓ Tier distribution matches expectations
- ✓ No data integrity issues
- ✓ Estimated cost savings >= 30%

---

### Phase 9: Gradual Rollout (Week 7-8)
**Goal: Enable tier-based routing in production**

**Rollout Plan:**

**Week 7 Day 1-2: 10% Traffic**
- Enable tier routing for 10% of requests
- Monitor metrics:
  - Cost per tier
  - Quality (session continuation rate)
  - Queue health
  - Escalation rate
- Daily review and adjustment

**Week 7 Day 3-5: 50% Traffic**
- Ramp to 50% if 10% stable
- Continue monitoring
- Compare A/B cohorts (tier vs non-tier)

**Week 7 Day 6-7: 100% Traffic**
- Full rollout if 50% stable
- Monitor for 48 hours

**Week 8: Optimization**
- Tune tier thresholds based on data
- Adjust fallback policies
- Refine escalation triggers
- Document learnings

**Rollback Triggers:**
- Escalation rate > 20%
- Session continuation rate drops > 10%
- Cost increase (unexpected)
- Queue saturation

**Deliverables:**
- Rollout execution log
- A/B comparison report
- Optimization recommendations
- Post-rollout retrospective

**Success Criteria:**
- ✓ Cost reduction >= 30%
- ✓ Session continuation rate maintained (< 5% drop)
- ✓ Escalation rate < 15%
- ✓ No production incidents

---

## 9. Risk Mitigation

### Risk 1: Task Misclassification → Quality Degradation

**Risk Level:** HIGH  
**Impact:** User dissatisfaction, increased support tickets

**Mitigation Strategies:**
1. **Conservative tier fallback:**
   - Tier-B includes Tier-A models as fallback
   - Low confidence (<0.7) → default to Tier-A
2. **LLM re-classification:**
   - Confidence < 0.7 → trigger LLM classifier
   - Use small fast model (glm-5.2-flash) to avoid cost
3. **User feedback loop:**
   - Track "retry with different model" signals
   - Manual labeling for training data
   - Continuous classification retraining
4. **Quality gate escalation:**
   - Auto-retry with higher tier on failures
   - Max 2 escalations per request

**Monitoring:**
- Alert if escalation rate > 20%
- Weekly manual audit of 100 samples
- Session continuation rate tracking

---

### Risk 2: Schema Migration Disrupts Production

**Risk Level:** MEDIUM  
**Impact:** Downtime, data loss, query performance degradation

**Mitigation Strategies:**
1. **Zero-downtime migrations:**
   - ADD COLUMN with DEFAULT (no table rewrite)
   - Backfill in background
   - No breaking changes to existing columns
2. **Backward-compatible writes:**
   - NULL tier = legacy behavior
   - Continue writing old fields
   - Dual-read pattern during transition
3. **Shadow mode testing:**
   - 7-day shadow write period
   - Validate data integrity before routing change
4. **Rollback plan:**
   - Keep migration rollback scripts ready
   - Test rollback in staging
   - Documented rollback procedure

**Monitoring:**
- Query performance before/after migration
- EXPLAIN ANALYZE on key queries
- Database CPU/memory metrics

---

### Risk 3: Queue Segmentation → Starvation

**Risk Level:** MEDIUM  
**Impact:** Some tiers/tasks experience high latency

**Mitigation Strategies:**
1. **Cross-tier overflow:**
   - If Tier-C queue full → spill to Tier-B
   - Prevents starvation of low-priority tasks
2. **Dynamic capacity adjustment:**
   - Monitor queue depth per tier
   - Adjust capacity limits based on load
3. **Fair scheduling:**
   - Round-robin across tier segments
   - Prevent head-of-line blocking
4. **Comprehensive alerting:**
   - Alert on queue depth > 80% capacity
   - Alert on P95 wait time > 5s

**Monitoring:**
- Per-tier queue depth metrics
- Per-tier wait time P95/P99
- Queue starvation detection

---

### Risk 4: Cost Increase (Escalation Overhead)

**Risk Level:** LOW  
**Impact:** Unexpected cost increase from frequent escalations

**Mitigation Strategies:**
1. **Max escalation limit:**
   - Max 2 escalations per request
   - Prevents infinite retry loops
2. **Escalation tracking:**
   - Monitor escalation rate per tier
   - Alert if rate > 15%
3. **Conservative initial tier:**
   - Low confidence → start with Tier-A
   - Reduces need for escalation
4. **Cost guardrails:**
   - Daily cost budget per tier
   - Auto-disable escalation if budget exceeded

**Monitoring:**
- `dispatch_escalation_total` metric
- Daily cost per tier
- Escalation rate trending

---

### Risk 5: Storage Growth (Full Request Body Storage)

**Risk Level:** LOW  
**Impact:** Increased storage costs

**Mitigation Strategies:**
1. **Configurable retention:**
   - 7-day hot storage (all records)
   - 30-day warm storage (prune non-terminal)
   - 90-day cold storage (metadata only)
2. **Selective storage:**
   - Non-terminal outbound: metadata only
   - Terminal outbound: full request + response
3. **Compression:**
   - JSONB automatic compression
   - Columnar storage for archived data
4. **Monitoring:**
   - Track storage growth rate
   - Alert on unexpected growth

**Estimated Storage:**
- Current: 10 GB/day (all full records)
- Optimized: 4 GB/day (selective retention)
- **Savings: 60%**

---

## 10. Success Metrics

### Primary Metrics

#### 1. Cost Reduction

**Target:** 30-40% reduction in total model cost

**Measurement:**
```sql
-- Weekly cost comparison
WITH baseline AS (
  SELECT SUM(total_tokens * unit_price_in / 1000000) as cost
  FROM request_logs
  WHERE ts >= '2026-08-01' AND ts < '2026-09-01'
    AND client_model != 'auto'  -- Non-auto baseline
),
optimized AS (
  SELECT SUM(total_tokens * unit_price_in / 1000000) as cost
  FROM request_logs
  WHERE ts >= '2026-09-01' AND ts < '2026-10-01'
    AND is_terminal = TRUE
)
SELECT 
  baseline.cost as baseline_cost,
  optimized.cost as optimized_cost,
  ROUND((1 - optimized.cost / baseline.cost) * 100, 2) as savings_pct
FROM baseline, optimized;
```

**Success Criteria:** >= 30% cost reduction

---

#### 2. Quality Maintenance

**Target:** < 5% degradation in user satisfaction

**Measurement:**
- **Session continuation rate:** % of sessions with > 1 follow-up message
- **Retry rate:** % of requests with user re-asking
- **Explicit feedback:** User ratings (if available)

```sql
-- Session continuation rate
SELECT 
  task_tier,
  COUNT(DISTINCT session_id) as total_sessions,
  COUNT(DISTINCT CASE WHEN message_count > 1 THEN session_id END) as continued_sessions,
  ROUND(
    COUNT(DISTINCT CASE WHEN message_count > 1 THEN session_id END)::NUMERIC / 
    COUNT(DISTINCT session_id) * 100, 
    2
  ) as continuation_rate_pct
FROM (
  SELECT 
    session_id,
    task_tier,
    COUNT(*) as message_count
  FROM request_logs
  WHERE request_type = 'client' 
    AND ts >= NOW() - INTERVAL '7 days'
  GROUP BY session_id, task_tier
) subq
GROUP BY task_tier;
```

**Success Criteria:** Continuation rate drop < 5%

---

#### 3. Queue Health

**Target:** P95 client wait time < 2s

**Measurement:**
```prometheus
histogram_quantile(0.95, 
  rate(dispatch_client_wait_time_seconds_bucket[5m])
) < 2
```

**Success Criteria:** P95 wait time < 2s

---

### Secondary Metrics

#### 4. Classification Accuracy

**Target:** >= 85% correct task classification

**Measurement:**
- Manual audit: Sample 100 requests/week
- Compare LLM classification vs manual label
- Track confidence score distribution

**Success Criteria:** >= 85% accuracy on manual audit

---

#### 5. Tier Distribution

**Target:** Match expected distribution (20% A, 40% B, 40% C)

**Measurement:**
```sql
SELECT 
  task_tier,
  COUNT(*) as count,
  ROUND(COUNT(*)::NUMERIC / SUM(COUNT(*)) OVER () * 100, 2) as pct
FROM request_logs
WHERE is_terminal = TRUE 
  AND ts >= NOW() - INTERVAL '7 days'
GROUP BY task_tier;
```

**Success Criteria:** Distribution within 10% of target

---

#### 6. Escalation Rate

**Target:** < 15% of requests escalate to higher tier

**Measurement:**
```sql
SELECT 
  COUNT(*) FILTER (WHERE escalation_count > 0) as escalated_requests,
  COUNT(*) as total_requests,
  ROUND(
    COUNT(*) FILTER (WHERE escalation_count > 0)::NUMERIC / 
    COUNT(*) * 100, 
    2
  ) as escalation_rate_pct
FROM request_logs
WHERE request_type = 'client'
  AND ts >= NOW() - INTERVAL '7 days';
```

**Success Criteria:** Escalation rate < 15%

---

#### 7. Cost Per Tier

**Target:** Tier-A: $30/1M, Tier-B: $10/1M, Tier-C: $2/1M (avg)

**Measurement:**
```sql
SELECT 
  task_tier,
  ROUND(AVG(unit_price_in / 1000000)::NUMERIC, 2) as avg_cost_per_1m_tokens,
  SUM(total_tokens) as total_tokens,
  ROUND(SUM(total_tokens * unit_price_in / 1000000)::NUMERIC, 2) as total_cost_usd
FROM request_logs
WHERE is_terminal = TRUE
  AND ts >= NOW() - INTERVAL '7 days'
GROUP BY task_tier;
```

**Success Criteria:** Avg cost per tier within 20% of target

---

## Appendix A: Code File Checklist

### Files to Create

- [ ] `autoroute/task_types.go` - 10-category constants
- [ ] `autoroute/tier_selector.go` - Tier determination logic
- [ ] `autoroute/quality_gate.go` - Quality gate checks
- [ ] `domains/dispatch/client_queue.go` - Layer-0 queue
- [ ] `domains/dispatch/outbound_queue.go` - Layer-1 queue
- [ ] `sql/migrations/202609_01_add_request_type_fields.sql`
- [ ] `sql/migrations/202609_02_create_task_type_tier_config.sql`
- [ ] `sql/migrations/202609_03_add_tier_to_provider_models.sql`

### Files to Update

- [ ] `autoroute/classifier.go` - Add 10-category classification
- [ ] `autoroute/recommend_v2.go` - Tier-based filtering
- [ ] `autoroute/scoring.go` - Tier preference in scoring
- [ ] `autoroute/decision.go` - Integrate tier selector
- [ ] `domains/streaming/handler.go` - Client entry log creation
- [ ] `domains/dispatch/pipeline.go` - Outbound log creation + escalation
- [ ] `domains/hooks/observability/telemetry/request_logger.go` - Dual-write support
- [ ] `domains/hooks/observability/metrics/dispatch_metrics.go` - New queue metrics

---

## Appendix B: Testing Strategy

### Unit Tests

**autoroute/classifier_test.go:**
- Test all 10 task categories
- Test confidence threshold logic
- Test keyword matching (strong + weak signals)
- Test LLM fallback trigger

**autoroute/tier_selector_test.go:**
- Test tier determination for all task types
- Test depth-based degradation (depth 0, 1, 2+)
- Test tenant override
- Test header override

**autoroute/quality_gate_test.go:**
- Test compilation check
- Test confidence threshold
- Test escalation decision

### Integration Tests

**End-to-end classification:**
- Submit request with model=auto
- Verify task_type classification
- Verify tier selection
- Verify candidate filtering by tier

**Entry/Exit separation:**
- Submit request
- Verify client log created
- Verify outbound log created
- Verify parent_request_id linking

**Escalation flow:**
- Simulate Tier-C failure
- Verify escalation to Tier-B
- Verify escalation_count increment
- Verify max escalation limit

### Load Tests

**Queue capacity:**
- Submit 2000 concurrent requests (2x client queue capacity)
- Verify backpressure (429 responses)
- Verify queue metrics accuracy

**Tier distribution under load:**
- Submit 10,000 requests (mixed task types)
- Verify tier distribution matches config
- Verify no tier starvation

---

## Appendix C: Rollback Plan

### If Classification Issues Detected

**Symptoms:**
- Escalation rate > 25%
- Session continuation rate drops > 10%
- User complaints spike

**Rollback Steps:**
1. Disable tier-based routing:
   ```go
   // In autoroute/decision.go
   // Force all requests to Tier-B (balanced tier)
   tierOverride := "tier-b"
   ```
2. Continue writing new fields (data collection continues)
3. Investigate classification issues
4. Retrain/retune classifier
5. Re-enable tier routing with fixes

### If Database Performance Degradation

**Symptoms:**
- Query latency increases > 50%
- Database CPU spikes
- Index bloat

**Rollback Steps:**
1. Disable writing to new fields:
   ```go
   // In telemetry/request_logger.go
   // Skip writing request_type, tier, etc.
   if !feature.Enabled("request_type_separation") {
     return
   }
   ```
2. Drop new indexes if causing issues
3. Investigate query plans
4. Optimize indexes
5. Re-enable new fields

### If Queue Starvation

**Symptoms:**
- Specific tier queue depth > 1000
- P95 wait time > 10s for specific tier
- Timeout rate increases

**Rollback Steps:**
1. Disable tier-based queue segmentation:
   ```go
   // In dispatch/pipeline.go
   // Revert to single model queue
   ```
2. Increase queue capacity limits
3. Investigate load distribution
4. Tune capacity per tier
5. Re-enable segmentation

---

## Conclusion

This comprehensive plan provides a structured approach to optimizing AUTO_MODEL routing through:

1. **Fine-grained classification** (10 categories)
2. **Cost-aware tier selection** (3 tiers)
3. **Clear data separation** (entry/exit requests)
4. **Enhanced observability** (4-layer queues)
5. **Quality protection** (auto-escalation)

**Expected Outcomes:**
- 30-40% cost reduction
- Maintained or improved user satisfaction
- Better analytics and cost tracking
- Enhanced operational visibility

**Next Steps:**
1. Review and approve this plan
2. Begin Phase 1: Database migrations
3. Weekly progress reviews
4. Adjust timeline based on learnings

---

**Document Version:** 3.0  
**Last Updated:** 2026-09-02  
**Status:** Ready for Implementation Approval
