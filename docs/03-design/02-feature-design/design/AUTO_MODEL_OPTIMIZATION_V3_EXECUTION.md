# AUTO_MODEL Optimization V3 - Execution Plan

**Created**: 2026-09-02  
**Status**: In Progress  
**Based On**: AUTO_MODEL_OPTIMIZATION_V3_PLAN.md

## Execution Overview

This document tracks the actual implementation progress of the AUTO_MODEL V3 optimization plan.

## Implementation Strategy

### Approach: Incremental Feature Flags

All new functionality will be controlled by feature flags to enable:
- Safe deployment without disrupting existing behavior
- Shadow mode testing (dual-write without routing changes)
- Gradual rollout by percentage
- Quick rollback if issues arise

### Feature Flags

```go
const (
    // Phase 1-2: Classification & Tier Selection
    FeatureEnhancedClassification = "auto_v3_enhanced_classification"
    FeatureTierSelection         = "auto_v3_tier_selection"
    
    // Phase 3: Tier-Based Routing
    FeatureTierBasedRouting      = "auto_v3_tier_routing"
    
    // Phase 4: Entry/Exit Separation
    FeatureRequestTypeSeparation = "auto_v3_request_separation"
    FeatureDepthTracking         = "auto_v3_depth_tracking"
    
    // Phase 5: Queue Architecture
    FeatureClientQueue           = "auto_v3_client_queue"
    FeatureTierQueueSegmentation = "auto_v3_tier_queue_segmentation"
    
    // Phase 6: Quality Gates
    FeatureQualityGates          = "auto_v3_quality_gates"
    FeatureAutoEscalation        = "auto_v3_auto_escalation"
)
```

## Phase 1: Database Foundation ✅

### Tasks

- [x] Create migration 01: Add request type fields
- [x] Create migration 02: Create task_type_tier_config table
- [x] Create migration 03: Add tier to provider_models
- [x] Create rollback scripts
- [x] Create validation queries
- [x] Create automated migration runner script
- [ ] Test migrations locally
- [ ] Apply migrations to postgres-252
- [ ] Validate with EXPLAIN ANALYZE

### Deliverables

- ✅ Migration scripts in `sql/migrations/`
- ✅ Rollback scripts in `sql/rollback/`
- ✅ Automated migration runner: `sql/run_migrations.sh`
- ✅ Automated rollback runner: `sql/rollback_migrations.sh`
- ✅ Validation queries: `sql/migrations/validate_v3_migrations.sql`
- ⏳ Migration validation report (pending execution)

### Status: Complete (Ready for Testing)

**Completed**: 2026-09-02

**Files Created**:
- `sql/migrations/202609_01_add_request_type_fields.sql`
- `sql/migrations/202609_02_create_task_type_tier_config.sql`
- `sql/migrations/202609_03_add_tier_to_provider_models.sql`
- `sql/migrations/validate_v3_migrations.sql`
- `sql/rollback/202609_01_rollback_request_type_fields.sql`
- `sql/rollback/202609_02_rollback_task_type_tier_config.sql`
- `sql/rollback/202609_03_rollback_tier_to_provider_models.sql`
- `sql/run_migrations.sh` (executable)
- `sql/rollback_migrations.sh` (executable)

**Next Steps**:
1. Review migration scripts with DBA
2. Test migrations in local environment
3. Apply to postgres-252 dev database
4. Validate query performance with EXPLAIN ANALYZE

---

## Phase 2: Enhanced Classification ✅

### Sub-Tasks

#### 2.1 Task Type Constants
- [x] Create `autoroute/task_types_v3.go` with 10 categories
- [x] Add confidence thresholds (MinConfidenceThresholds map)
- [x] Add keyword definitions (DefaultV3Keywords with strong + weak signals)
- [x] Add tier mapping (TaskTypeTierMapping)

#### 2.2 Enhanced Classifier
- [x] Create `autoroute/classifier_v3.go`:
  - [x] V3Classifier with 10-category support
  - [x] Keyword matching for all 10 categories
  - [x] Confidence scoring (0.35 per keyword hit)
  - [x] Signal extraction (code blocks, IDE fingerprints, error patterns)
  - [x] Feature flag support (enableV3)
  - [x] Legacy fallback when V3 disabled
- [ ] Create unit tests: `autoroute/classifier_v3_test.go`

#### 2.3 Tier Selector
- [x] Create `autoroute/tier_selector.go`:
  - [x] TierSelector with database integration
  - [x] Query task_type_tier_config with caching
  - [x] Implement depth-based degradation (depth >= 2 → tier-c)
  - [x] Support tenant overrides
  - [x] Support header overrides (X-Gw-Model-Tier, highest priority)
  - [x] In-memory fallback when database unavailable
  - [x] 5-minute cache TTL with auto-refresh
- [ ] Create unit tests: `autoroute/tier_selector_test.go`

#### 2.4 Integration
- [ ] Update `autoroute/decision.go` to use V3 classifier
- [ ] Add feature flag checks throughout
- [ ] Integration tests with sample requests
- [ ] End-to-end testing

### Status: Complete (Code Ready, Testing Pending)

**Completed**: 2026-09-02

**Files Created**:
- `autoroute/task_types_v3.go` (10 categories, keywords, tier mappings)
- `autoroute/classifier_v3.go` (V3 classification algorithm)
- `autoroute/tier_selector.go` (Tier determination with DB integration)

**Key Features Implemented**:

1. **10-Category Classification**:
   - Tier-A: architecture, audit, debugging
   - Tier-B: coding, refactoring, testing
   - Tier-C: devops, documentation, summary, dependency

2. **Enhanced Keyword System**:
   - Strong signals: "system design", "code review", "stack trace"
   - Weak signals: "improve", "analyze", "build"
   - Bilingual support (English + Chinese)

3. **Tier Selection Priority**:
   1. Header override (X-Gw-Model-Tier)
   2. Depth-based degradation (nested sub-agents → tier-c)
   3. Tenant-specific config
   4. Global config
   5. In-memory default

4. **Feature Flags**:
   - V3Classifier.enableV3: Enable/disable V3 classification
   - TierSelector.enableV3: Enable/disable tier selection
   - Graceful fallback to legacy behavior when disabled

**Next Steps**:
1. Create comprehensive unit tests
2. Create integration tests with sample requests
3. Tune keyword weights based on test results
4. Begin Phase 3: Tier-Based Routing integration

---

## Phase 3: Tier-Based Routing

### Sub-Tasks

#### 3.1 Recommendation Updates
- [ ] Update `autoroute/recommend_v2.go`:
  - [ ] Filter candidates by tier
  - [ ] Implement fallback tier logic
  - [ ] Query provider_models with tier filter

#### 3.2 Scoring Updates
- [ ] Update `autoroute/scoring.go`:
  - [ ] Add tier matching bonus
  - [ ] Adjust scoring weights

#### 3.3 Integration
- [ ] Update `autoroute/decision.go`:
  - [ ] Pass tier to recommendation
  - [ ] Store tier in auto_decision JSONB
- [ ] Add tier to response headers (X-Gw-Task-Tier)

### Status: Pending

---

## Phase 4: Entry/Exit Request Separation

### Sub-Tasks

#### 4.1 Client Entry Logging
- [ ] Update `domains/streaming/handler.go`:
  - [ ] Create client entry log on request admission
  - [ ] Extract X-Gw-Agent-Depth header
  - [ ] Store client request body
  - [ ] Store auto_decision metadata
  - [ ] Generate and pass client_request_id

#### 4.2 Outbound Request Logging
- [ ] Update `domains/dispatch/pipeline.go`:
  - [ ] Create outbound log on dispatch
  - [ ] Link via parent_request_id
  - [ ] Mark is_terminal on completion
  - [ ] Store full assembled request body

#### 4.3 Telemetry Updates
- [ ] Update `domains/hooks/observability/telemetry/request_logger.go`:
  - [ ] Support request_type distinction
  - [ ] Handle dual-write pattern
  - [ ] Add feature flag checks

#### 4.4 Depth Tracking
- [ ] Add X-Gw-Agent-Depth header propagation
- [ ] Apply depth-based tier override in tier_selector

### Status: Pending

---

## Phase 5: Four-Layer Queue Architecture

### Sub-Tasks

#### 5.1 Client Queue (Layer 0)
- [ ] Create `domains/dispatch/client_queue.go`:
  - [ ] Semaphore-based admission control (capacity: 1000)
  - [ ] Backpressure on saturation
  - [ ] Prometheus metrics

#### 5.2 Outbound Queue (Layer 1)
- [ ] Create `domains/dispatch/outbound_queue.go`:
  - [ ] Track outbound request depth (capacity: 5000)
  - [ ] Retry ratio tracking
  - [ ] Prometheus metrics

#### 5.3 Model Queue Enhancement (Layer 2)
- [ ] Update `domains/dispatch/pipeline.go`:
  - [ ] Segment model queues by tier + task_type
  - [ ] Fair scheduling across segments
  - [ ] Tier-specific queue metrics

#### 5.4 Metrics
- [ ] Update `domains/hooks/observability/metrics/dispatch_metrics.go`:
  - [ ] Add client_queue_depth
  - [ ] Add outbound_queue_depth
  - [ ] Add tier_queue_depth
  - [ ] Add escalation_rate

### Status: Pending

---

## Phase 6: Quality Gates & Escalation

### Sub-Tasks

#### 6.1 Quality Gate Implementation
- [ ] Create `autoroute/quality_gate.go`:
  - [ ] Compilation check interface (for code tasks)
  - [ ] Test execution check interface
  - [ ] Confidence threshold check
  - [ ] Escalation decision logic

#### 6.2 Escalation Logic
- [ ] Update `domains/dispatch/pipeline.go`:
  - [ ] Integrate quality gate check
  - [ ] Implement retry with higher tier
  - [ ] Track escalation_count
  - [ ] Enforce max escalation limit (2)

#### 6.3 Metrics & Monitoring
- [ ] Add escalation metrics:
  - [ ] dispatch_escalation_total{from_tier, to_tier}
  - [ ] dispatch_quality_gate_failure_total{tier, reason}

### Status: Pending

---

## Phase 7: Observability & Monitoring

### Sub-Tasks

#### 7.1 Grafana Dashboard
- [ ] Create dashboard: AUTO_MODEL V3 Analytics
  - [ ] Client vs outbound queue depth
  - [ ] Tier distribution (request count + cost)
  - [ ] Task type distribution
  - [ ] Escalation rate per tier
  - [ ] Retry ratio
  - [ ] Cost per tier per hour

#### 7.2 Prometheus Alerts
- [ ] Configure alerts:
  - [ ] ClientQueueSaturated
  - [ ] HighRetryRatio
  - [ ] TierCostSpike
  - [ ] EscalationRateHigh

#### 7.3 Cost Analysis Queries
- [ ] Create SQL queries:
  - [ ] Cost per tier per day
  - [ ] Cost savings vs baseline
  - [ ] Tier distribution trends
  - [ ] Escalation impact on cost

### Status: Pending

---

## Phase 8: Shadow Mode & Validation

### Sub-Tasks

#### 8.1 Shadow Mode Configuration
- [ ] Enable shadow mode feature flags:
  - [ ] Write new fields (request_type, tier, etc.)
  - [ ] Continue using old routing logic
  - [ ] Log what tier *would* have been selected

#### 8.2 Data Collection (7 days)
- [ ] Collect shadow data
- [ ] Validate classification accuracy
- [ ] Analyze tier distribution
- [ ] Estimate cost impact

#### 8.3 Manual Validation
- [ ] Sample 100 requests
- [ ] Verify task classification correctness
- [ ] Check tier assignment logic
- [ ] Identify misclassification patterns

#### 8.4 Tuning
- [ ] Adjust classification keywords
- [ ] Tune tier thresholds
- [ ] Refine fallback logic
- [ ] Update tier confidence thresholds

### Status: Pending

---

## Phase 9: Gradual Rollout

### Week 7 Day 1-2: 10% Traffic
- [ ] Enable tier routing for 10% of requests
- [ ] Monitor metrics (cost, quality, queue, escalation)
- [ ] Daily review and adjustment

### Week 7 Day 3-5: 50% Traffic
- [ ] Ramp to 50% if 10% stable
- [ ] Continue monitoring
- [ ] Compare A/B cohorts

### Week 7 Day 6-7: 100% Traffic
- [ ] Full rollout if 50% stable
- [ ] Monitor for 48 hours

### Week 8: Optimization
- [ ] Tune tier thresholds based on data
- [ ] Adjust fallback policies
- [ ] Refine escalation triggers
- [ ] Document learnings

### Status: Pending

---

## Risk Tracking

### Active Risks

| Risk | Level | Status | Mitigation |
|------|-------|--------|------------|
| Task misclassification | HIGH | Monitored | Conservative fallback, LLM re-classification |
| Schema migration disrupts production | MEDIUM | Mitigated | Zero-downtime migrations, rollback scripts ready |
| Queue segmentation causes starvation | MEDIUM | Monitored | Cross-tier overflow, dynamic capacity |
| Cost increase from escalation | LOW | Monitored | Max escalation limit, tracking |
| Storage growth | LOW | Mitigated | Configurable retention, selective storage |

---

## Success Metrics Tracking

### Primary Metrics

| Metric | Target | Current | Status |
|--------|--------|---------|--------|
| Cost reduction | 30-40% | TBD | Pending |
| Session continuation rate | < 5% degradation | TBD | Pending |
| P95 client wait time | < 2s | TBD | Pending |

### Secondary Metrics

| Metric | Target | Current | Status |
|--------|--------|---------|--------|
| Classification accuracy | >= 85% | TBD | Pending |
| Tier distribution | 20/40/40 (A/B/C) | TBD | Pending |
| Escalation rate | < 15% | TBD | Pending |
| Avg cost per tier | A:$30, B:$10, C:$2 per 1M | TBD | Pending |

---

## Next Actions

1. ✅ Create execution plan document
2. ⏳ Create and test database migrations
3. ⏳ Apply migrations to dev database (postgres-252)
4. ⏳ Begin Phase 2: Enhanced Classification implementation

---

**Last Updated**: 2026-09-02  
**Current Phase**: Phase 1 - Database Foundation
