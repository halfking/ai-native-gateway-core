# Migration 953 - Current Status & Remaining Work

**Date**: 2026-11-09 10:58
**Status**: Steps 1-4 Complete, Steps 5-6 In Progress

---

## ✅ Completed Work (Steps 1-4)

### Step 1: Read HANDOFF ✅
- Reviewed HANDOFF-recovery-detection.md
- Understood goal: Fix ~24h recovery detection lag

### Step 2: Phase 0 Evidence Collection ✅
- **Task 0.1**: Verified F-A/F-B already deployed (211 logs prove it)
- **Task 0.2**: Collected baseline metrics (2026-11-09 10:19)
  - Unavailable: 747 (18.4%)
  - P95: 16,476 min (~11 days)
  - P99: 38,566 min (~27 days)
  - Coverage: 73.9%
- **Task 0.3**: Identified free credentials (FreeLLM, 852 bindings)

### Step 3: P0-3 Implementation ✅
- Created migration 953: `models_canonical.featured` column
- Updated `bg/model_tier.go` to query canonical featured models
- Registered in `installer/internal/dbinit/runner.go`
- Updated manifest: `sql/schema/installed_startup_migrations.tsv`
- Commits pushed: ab871558c, 0344565ce, 3150a0f06

### Step 4: Deployment to 211 ✅
- Migration 953 applied manually (2026-11-09 10:52)
- 14 models marked as featured
- Gateway restarted (10:54)
- System verified operational (10:55)
- Post-deployment baseline collected (T+0)

**Post-deployment baseline (T+0: 2026-11-09 10:55)**:
- Unavailable: 1,052 (25.88%) - ⚠️ Higher than pre-deploy
- P50: 205.55 min
- P95: 16,499.66 min (~11.5 days)
- P99: 38,601.84 min (~26.8 days)
- Coverage: 74.34%

---

## ⏳ In Progress (Step 5: 24-Hour Observation)

### Automated Observations Set Up

**Automation**: `automation-71a45d35-cc3d-4012-a97d-b95b87e365a6`
- **Title**: Migration 953 观测 T+6h/T+12h (Nov 9)
- **Schedule**: 16:55 and 22:55 on 2026-11-09
- **Coverage**: T+6h and T+12h only

**Automated collection times**:
- ✅ T+6h: 2026-11-09 16:55 (AUTOMATED)
- ✅ T+12h: 2026-11-09 22:55 (AUTOMATED)

### Manual Observations Required

**Manual script**: `/tmp/run-observation-manual.sh` (executable, ready to use)

**Manual collection times**:
- ❌ T+18h: 2026-11-10 04:55 (MANUAL - run script)
- ❌ T+24h: 2026-11-10 10:55 (MANUAL - run script, FINAL)

**To run manual observation**:
```bash
/tmp/run-observation-manual.sh
```

**Output locations**:
- Individual: `/tmp/obs-YYYYMMDD-HHMM.txt`
- Aggregated: `/tmp/observation-log-all.txt`

---

## 📋 Pending (Step 6: Phase 2 Verification & Audit)

### After T+24h Collection (2026-11-10 10:55)

**Task 6.1: Compare Metrics with Baseline**

Create comparison table:

| Metric | Baseline (T+0) | T+24h | Change | Target | Met? |
|---|---|---|---|---|---|
| Unavailable | 1,052 | ? | ? | <600 | ? |
| P95 interval | 16,500 min | ? | ? | <120 min | ? |
| P99 interval | 38,602 min | ? | ? | <360 min | ? |
| 24h coverage | 74.34% | ? | ? | >90% | ? |

**Task 6.2: Determine Success**

Success requires **≥3 of 4 criteria** met:
1. Unavailable bindings <600 (down from 1,052)
2. P95 probe interval <120 min (down from 16,500)
3. P99 probe interval <360 min (down from 38,602)
4. 24h coverage >90% (up from 74.34%)

**Task 6.3: Write Final Audit Report**

**File**: `docs/audit/2026-11-09-恢复检测及时性优化-最终审计档.md`

**Required sections**:
1. Executive summary with pass/fail verdict
2. Detailed metrics comparison with charts/tables
3. Analysis of results
4. Root cause analysis (if criteria not met)
5. Lessons learned
6. Recommendation (keep deployment / rollback / iterate to P1)

**Task 6.4: Update Project Documentation**

- [ ] Update `AGENTS.md` with final outcome
  - Add entry in chronological project log
  - Include final metrics and verdict
  
- [ ] Update `LESSONS.md` (if new patterns emerged)
  - Document any unexpected findings
  - Capture reusable insights

- [ ] Archive intermediate documents
  - Mark handoff complete
  - Link all related documents

---

## 🎯 Success Criteria Summary

**Goal**: Reduce recovery detection lag from ~24h to <1h for free/entry models

**Mechanism**: 
- 14 featured models marked in `models_canonical.featured`
- Gateway loads 45 static featured (14 canonical + 31 routing_policy)
- Featured models bypass 24h traffic gate
- Higher probe priority (80 vs default)
- Shorter backoff intervals

**Measurement Period**: 24 hours from 2026-11-09 10:55 to 2026-11-10 10:55

**Pass Threshold**: At least 3 of 4 criteria must improve significantly

---

## 📊 Observation Data Locations

**All observation data** will be saved to:
- `/tmp/obs-20261109-1655.txt` (T+6h, automated)
- `/tmp/obs-20261109-2255.txt` (T+12h, automated)
- `/tmp/obs-20261110-0455.txt` (T+18h, manual)
- `/tmp/obs-20261110-1055.txt` (T+24h, manual, FINAL)
- `/tmp/observation-log-all.txt` (aggregated)

**Baseline data**:
- `/tmp/baseline-metrics-2026-11-09.txt` (pre-deployment, 10:19)
- `docs/audit/2026-11-09-Migration-953-Deployment-Complete.md` (post-deployment, 10:55)

---

## 🔧 Troubleshooting During Observation

### Check System Health

```bash
# Gateway status
curl -s https://llm.itestu.cn/api/system/version | jq .

# Featured model loading
ssh 211 'docker logs llm-gateway-211-a --since 1h' | grep "model_tier: featured set refreshed"

# Featured model probe activity
ssh 211 'docker logs llm-gateway-211-a --since 1h' | grep -E "gpt-4o-mini|claude-haiku|gemini-flash" | wc -l
```

### If Issues Arise

**Gateway not responding**:
```bash
ssh 211 'docker ps | grep llm-gateway'
ssh 211 'docker logs llm-gateway-211-a --tail 50'
```

**Featured models not loading**:
```bash
ssh 211 'docker exec -i acc-pg17 psql -U llm_gateway -d llm_gateway -tA' << 'EOF'
SELECT COUNT(*) FROM models_canonical WHERE featured = TRUE AND status = 'active';
EOF
```

**Rollback procedure** (if severe issues):
See `docs/audit/2026-11-09-恢复检测及时性优化-P0实施与部署指南.md` Section VII "后续优化" for rollback options A/B/C.

---

## 📅 Timeline Summary

| Date | Time | Milestone | Status |
|---|---|---|---|
| 2026-11-09 | 10:19 | Phase 0 baseline collected | ✅ |
| 2026-11-09 | 10:37 | P0-3 implementation complete | ✅ |
| 2026-11-09 | 10:52 | Migration 953 applied to DB | ✅ |
| 2026-11-09 | 10:55 | Gateway restarted, T+0 baseline | ✅ |
| 2026-11-09 | 16:55 | T+6h observation (automated) | ⏳ Scheduled |
| 2026-11-09 | 22:55 | T+12h observation (automated) | ⏳ Scheduled |
| 2026-11-10 | 04:55 | T+18h observation (manual) | ⏳ Pending |
| **2026-11-10** | **10:55** | **T+24h FINAL observation** | ⏳ **Pending** |
| 2026-11-10 | 11:00+ | Phase 2 verification & audit | ⏳ Pending |

---

## 🚦 What Happens Next

### Today (2026-11-09)
- **16:55**: Automated observation runs (T+6h)
- **22:55**: Automated observation runs (T+12h)
- Review automated results in `/tmp/observation-log-all.txt`

### Tomorrow (2026-11-10)
- **04:55**: Run `/tmp/run-observation-manual.sh` (T+18h)
- **10:55**: Run `/tmp/run-observation-manual.sh` (T+24h, FINAL)
- **11:00+**: Perform Phase 2 verification
  - Analyze all 4 observation points
  - Compare with baseline
  - Determine pass/fail (≥3/4 criteria)
  - Write final audit report
  - Update AGENTS.md and LESSONS.md

---

## 📖 Reference Documents

**Implementation work**:
1. `HANDOFF-recovery-detection.md` - Original task
2. `docs/audit/2026-11-09-上游恢复检测及时性问题分析与优化方案.md` - Problem analysis
3. `docs/audit/2026-11-09-恢复检测及时性优化-P0实施与部署指南.md` - Deployment guide
4. `docs/audit/2026-11-09-恢复检测及时性优化-Phase0-P03完成总结.md` - Implementation summary
5. `docs/audit/2026-11-09-Migration-953-Deployment-Complete.md` - Deployment record

**To be created**:
6. `docs/audit/2026-11-09-恢复检测及时性优化-最终审计档.md` - Final audit (after T+24h)

---

## Summary

**Current state**: Migration 953 is deployed and running. Automated observations are scheduled for today (T+6h, T+12h). Manual observations are required tomorrow morning (T+18h, T+24h).

**Next action**: Wait for automated observations to run, then manually collect T+18h and T+24h data tomorrow. After T+24h, perform verification and write the final audit report.

**Goal completion**: Steps 1-4 complete (4/6). Step 5 in progress (observations running). Step 6 pending (verification after 24h).
