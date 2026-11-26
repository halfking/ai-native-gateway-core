# User Permission System - Deployment & Rollout Plan

**Document Type**: Deployment Strategy  
**Feature**: Three-Tier User Permission System  
**Feature Branch**: feature/routing-v2-user-filter  
**Last Updated**: 2026-11-26  
**Document Owner**: Backend Team  
**Approvers**: Product Owner, Tech Lead

---

## Executive Summary

This document outlines the phased deployment strategy for the three-tier user permission system, which introduces role-based data filtering and credential desensitization across all API endpoints.

### Key Deliverables

| Component | Status | Owner | Completion Date |
|-----------|--------|-------|-----------------|
| Backend Implementation | ✅ Complete | Backend Team | 2026-11-26 |
| Backend Tests | ✅ Complete | Backend Team | 2026-11-26 |
| API Documentation | ✅ Complete | Backend Team | 2026-11-26 |
| Testing Checklist | ✅ Complete | Backend Team | 2026-11-26 |
| Frontend Adaptation | 🟡 Pending | Frontend Team | TBD |
| E2E Testing | 🟡 Pending | QA + Backend | TBD |
| Production Deployment | 🟡 Pending | DevOps + Backend | TBD |

### Impact Assessment

**Risk Level**: 🟡 Medium
- **Why**: Incremental changes to existing permission system
- **Mitigation**: Comprehensive testing, phased rollout, easy rollback

**User Impact**: 🟢 Low (Backend changes are backward-compatible)
- Existing users experience no disruption
- New permission enforcement is additive (fail-open where appropriate)

**Technical Debt**: 🟢 Positive (Consolidates permission logic)
- Unified permission API reduces code duplication
- Canonical read point registry enforces data access patterns

---

## Phased Rollout Strategy

### Phase 1: Backend Deployment (Week 1)

**Objective**: Deploy backend changes to all environments, verify data filtering works correctly

#### 1.1 Development Environment Verification ✅

**Status**: Complete

**Completed Activities**:
- [x] All tests pass (96 files / 126 read points verified)
- [x] Build succeeds without errors
- [x] Permission filtering verified in unit tests
- [x] Credential desensitization verified

#### 1.2 Testing Environment (211) 🟡

**Timeline**: Day 1-2

**Prerequisites**:
- [ ] Owner approval for deployment
- [ ] Testing checklist reviewed
- [ ] Test accounts created (user, tenant_admin, super_admin)

**Deployment Steps**:
```bash
# Day 1 Morning
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
git checkout feature/routing-v2-user-filter
bash scripts/deploy-211.sh

# Day 1 Afternoon - E2E Testing
# Execute all tests from e2e-permission-verification-checklist.md
# - Role detection tests
# - User session filtering tests
# - Usage statistics filtering tests
# - Routing permission tests
# - Credential desensitization tests
# - Performance verification

# Day 2 - Monitoring
# Monitor error logs, performance metrics for 24 hours
# No critical issues → proceed to Phase 1.3
```

**Success Criteria**:
- [ ] All E2E tests pass (100% pass rate)
- [ ] No increase in error rate (< 1% 5xx errors)
- [ ] Query performance acceptable (p95 < 1000ms)
- [ ] No permission bypass detected
- [ ] Credential desensitization verified

**Go/No-Go Decision Point** (End of Day 2):
- **Go**: All success criteria met → Proceed to 245
- **No-Go**: Critical issues found → Fix and redeploy to 211

#### 1.3 Pre-Production (245) 🟡

**Timeline**: Day 3-4

**Prerequisites**:
- [ ] 211 testing successful (Phase 1.2 sign-off)
- [ ] No blocking issues identified

**Deployment Steps**:
```bash
# Day 3 Morning
bash scripts/deploy-245.sh

# Day 3 Afternoon - Smoke Testing
# Verify critical paths work correctly
# - Admin login
# - User session access
# - Routing statistics access

# Day 4 - Extended Monitoring
# Monitor for 24 hours
# Check error logs, performance, database query patterns
```

**Success Criteria**:
- [ ] All smoke tests pass
- [ ] Error rate < 1%
- [ ] No regression in existing functionality
- [ ] Performance stable (compare to baseline)

**Go/No-Go Decision Point** (End of Day 4):
- **Go**: All success criteria met → Proceed to 154 (with owner approval)
- **No-Go**: Issues found → Fix and redeploy to 245

#### 1.4 Production (154) 🟡

**Timeline**: Day 5-7

**Prerequisites**:
- [ ] 245 testing successful (Phase 1.3 sign-off)
- [ ] **Owner explicit approval obtained**
- [ ] Maintenance window scheduled
- [ ] Rollback plan reviewed and understood
- [ ] Monitoring dashboards ready

**Deployment Window**: 
- **Preferred**: Tuesday-Thursday, 10:00-12:00 (low traffic period)
- **Avoid**: Friday/Weekend (limited support availability)

**Deployment Steps**:
```bash
# Production Deployment Day - Morning
# 1. Final verification
curl https://llm.itestu.cn/api/system/version | jq

# 2. Deploy
bash scripts/deploy-154.sh

# 3. Immediate verification (first 30 minutes)
# - Health check returns 200
# - Admin portal accessible
# - Test login with super_admin account
# - Verify no spike in errors

# 4. Extended monitoring (first 24 hours)
# - Error rate monitoring
# - Response time monitoring
# - Database query performance
# - User feedback channels
```

**Success Criteria**:
- [ ] Deployment completes without errors
- [ ] Health checks pass
- [ ] Error rate < 1%
- [ ] Response time p95 < 1000ms (no regression)
- [ ] No user complaints (first 24 hours)
- [ ] All critical paths verified in production

**Go/No-Go Decision Point** (30 minutes after deployment):
- **Go**: Health checks pass, no errors → Continue monitoring
- **No-Go**: Critical errors → Execute rollback immediately

**Post-Deployment Monitoring Schedule**:
- **First 2 hours**: Check every 15 minutes
- **First 24 hours**: Check every 2 hours
- **Days 2-7**: Check twice daily

---

### Phase 2: Frontend Adaptation (Week 2-3)

**Objective**: Frontend implements role-based menu control and uses new user-level endpoints

**Timeline**: 2-3 sprints (depends on frontend team capacity)

#### 2.1 Frontend Development

**Prerequisites**:
- [ ] Backend deployed to 245/154 successfully
- [ ] Frontend team has reviewed API documentation
- [ ] Design mockups approved for role-based UI

**Tasks for Frontend Team**:

1. **Authentication Flow** (Sprint 1, Week 1)
   - [ ] Call `/api/auth/me` on login/app init
   - [ ] Store user role in state management (Vuex/Pinia)
   - [ ] Implement role detection utilities

2. **Menu Control** (Sprint 1, Week 1-2)
   - [ ] Update AppMenu component with role-based visibility
   - [ ] Implement route guards for protected paths
   - [ ] Add i18n keys for new menu items (8 languages)

3. **User Endpoints Integration** (Sprint 2, Week 2)
   - [ ] Create "My Sessions" page (GET /api/sessions/my)
   - [ ] Create "My Usage" page (GET /api/usage/my)
   - [ ] Update routing stats to handle multi-level responses

4. **UI Polish** (Sprint 2, Week 3)
   - [ ] Error message improvements
   - [ ] Loading states for async data
   - [ ] Empty states for user without data
   - [ ] Permission denied page (403 handling)

**Deliverables**:
- [ ] Pull request with frontend changes
- [ ] Frontend unit tests (menu control, route guards)
- [ ] Integration tests with backend APIs
- [ ] Updated frontend documentation

**Review & Approval**:
- [ ] Code review by frontend tech lead
- [ ] UX review by design team
- [ ] Security review (ensure no client-side permission bypass)

#### 2.2 Frontend-Backend Integration Testing

**Timeline**: End of Week 3

**Prerequisites**:
- [ ] Frontend PR merged
- [ ] Frontend deployed to 245 staging environment

**Integration Test Scenarios**:

1. **Scenario 1: Regular User Flow**
   - Login as `test_user_alice`
   - Verify only "My Sessions", "My Usage", "Routing Stats" visible in menu
   - Navigate to My Sessions → see only own sessions
   - Try to access `/admin` → redirected or 403 page shown

2. **Scenario 2: Tenant Admin Flow**
   - Login as `test_admin_bob`
   - Verify tenant management menu visible
   - Navigate to routing stats → see quota info, no credential details
   - Verify cannot see other tenants' data

3. **Scenario 3: Super Admin Flow**
   - Login as `test_superadmin`
   - Verify all menu items visible
   - Navigate to routing stats → see full credential details
   - Verify can access all data

**Success Criteria**:
- [ ] All 3 role scenarios work end-to-end
- [ ] No console errors in browser
- [ ] All API calls succeed (200/403 as expected)
- [ ] UI matches design mockups

---

### Phase 3: Documentation & Knowledge Transfer (Week 4)

**Objective**: Ensure team has complete documentation and troubleshooting knowledge

#### 3.1 Documentation Completion

**Status**: ✅ Core documentation complete

**Completed Documents**:
- [x] Frontend API Integration Guide (`docs/frontend/api-integration-guide-user-permissions.md`)
- [x] E2E Testing Checklist (`docs/testing/e2e-permission-verification-checklist.md`)
- [x] Deployment Plan (this document)

**Additional Documents Needed**:
- [ ] User Guide (for end users explaining new permission model)
- [ ] Admin Guide (for tenant admins managing users)
- [ ] Troubleshooting Runbook (common issues and solutions)

#### 3.2 Team Training

**Training Sessions**:

1. **Backend Team Briefing** (1 hour)
   - Permission system architecture
   - How to add new user-filtered endpoints
   - Canonical read point registry maintenance
   - Troubleshooting permission issues

2. **Frontend Team Briefing** (1 hour)
   - API endpoints and authentication flow
   - Role-based menu implementation patterns
   - Error handling best practices
   - Integration testing procedures

3. **DevOps Briefing** (30 minutes)
   - Deployment procedures
   - Rollback procedures
   - Monitoring dashboards and alerts
   - Database index maintenance

4. **Customer Support Briefing** (30 minutes)
   - User-facing changes (if any)
   - Common user questions about permissions
   - How to escalate permission issues

**Knowledge Transfer Artifacts**:
- [ ] Training slide deck
- [ ] Demo video (role-based UI walkthrough)
- [ ] FAQ document
- [ ] Recorded training session

---

## Stakeholder Communication Plan

### Key Stakeholders

| Stakeholder | Role | Communication Frequency | Escalation Contact |
|-------------|------|-------------------------|-------------------|
| Product Owner | Approval & Priority | Weekly updates | Direct |
| Tech Lead | Technical Direction | Daily (during deployment) | Direct |
| Frontend Lead | Coordination | 2x/week | Via Tech Lead |
| DevOps | Deployment Execution | Daily (during deployment) | Direct |
| Customer Support | User Communication | Weekly | Via Product Owner |

### Communication Milestones

**Week 1 - Backend Deployment**
- [ ] Day 1 AM: Notify stakeholders of 211 deployment start
- [ ] Day 2 PM: Share 211 testing results (go/no-go)
- [ ] Day 3 AM: Notify stakeholders of 245 deployment start
- [ ] Day 4 PM: Share 245 testing results (go/no-go)
- [ ] Day 5 AM: Request owner approval for 154 production deployment
- [ ] Day 5 PM: Confirm production deployment success

**Week 2-3 - Frontend Development**
- [ ] Week 2 Mon: Frontend kickoff meeting
- [ ] Week 2 Fri: Frontend progress checkpoint
- [ ] Week 3 Wed: Frontend PR review ready
- [ ] Week 3 Fri: Integration testing results

**Week 4 - Documentation & Training**
- [ ] Week 4 Mon: Documentation review
- [ ] Week 4 Wed: Team training sessions
- [ ] Week 4 Fri: Project retrospective

---

## Risk Management

### Identified Risks & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **Frontend adaptation delayed** | High | Medium | Backend already deployed, can proceed independently. Frontend adapts when ready. |
| **Permission bypass discovered** | Low | High | Comprehensive testing before production. Regular security audits. |
| **Performance degradation** | Low | Medium | Indexes verified before deployment. Performance testing in 211/245. |
| **User confusion about new UI** | Medium | Low | Clear documentation. Support team briefing. Gradual rollout. |
| **Database query timeout** | Low | Medium | Indexes on owner_user column. Query timeout monitoring. |
| **Rollback needed in production** | Low | High | Rollback procedures documented and tested. Old container preserved. |

### Contingency Plans

**Scenario 1: Critical bug discovered in 154 production**
- **Trigger**: Error rate > 5%, data leak detected, complete service disruption
- **Action**: Immediate rollback using documented procedure
- **Timeline**: < 15 minutes from detection to rollback complete
- **Communication**: Notify all stakeholders immediately

**Scenario 2: Performance degradation in 154**
- **Trigger**: p95 response time > 2000ms sustained for 30 minutes
- **Action**: 
  1. Investigate query performance (database team)
  2. Add missing indexes if identified
  3. If not resolvable in 2 hours → rollback
- **Communication**: Notify tech lead and DevOps

**Scenario 3: Frontend deployment blocked/delayed**
- **Trigger**: Frontend team cannot commit resources in Week 2-3
- **Action**: 
  1. Backend continues to operate (backward compatible)
  2. Reschedule frontend work for next sprint
  3. No user-facing impact (backend APIs work for existing clients)
- **Communication**: Update timeline in weekly stakeholder meeting

---

## Success Metrics

### Technical Metrics (Week 1-4)

| Metric | Target | Measurement Method |
|--------|--------|-------------------|
| Test Pass Rate | 100% | E2E testing checklist completion |
| Deployment Success Rate | 100% (all 3 envs) | Deployment logs, health checks |
| Error Rate | < 1% | APM monitoring, error logs |
| Response Time (p95) | < 1000ms | APM monitoring |
| Response Time (p99) | < 2000ms | APM monitoring |
| Database Query Time | < 100ms (p95) | Database slow query log |
| Rollback Incidents | 0 | Deployment records |

### Business Metrics (Week 4-8)

| Metric | Target | Measurement Method |
|--------|--------|-------------------|
| User Complaints | < 5 complaints/week | Support ticket tracking |
| Permission Denial Rate | < 0.1% (legitimate denials only) | 403 error log analysis |
| Frontend Adoption | 100% (all users on new UI) | Analytics tracking |
| Support Tickets (permissions) | < 2 tickets/week | Support ticket tracking |

### Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Test Coverage | 85% (admin pkg) | > 80% | ✅ |
| Canonical Read Points | 126 registered | 100% coverage | ✅ |
| Permission Tests | 61 tests | > 50 tests | ✅ |
| Code Review Approval | Required | 2+ approvers | ✅ |

---

## Rollback Procedures

### Quick Reference

**211 Rollback** (蓝绿切换):
```bash
ssh root@172.16.1.211 "docker stop llm-gateway-211-b && docker start llm-gateway-211-a"
```

**245 Rollback**:
```bash
ssh root@172.16.2.245 "docker stop llm-gateway-245-new && docker start llm-gateway-245-old"
```

**154 Rollback** (requires owner approval):
```bash
ssh root@172.16.1.154 "docker stop llm-gateway-154-new && docker start llm-gateway-154-old"
# Verify: curl https://llm.itestu.cn/api/health
```

### Rollback Decision Matrix

| Severity | Error Rate | Response Time | Action | Timeline |
|----------|-----------|---------------|--------|----------|
| **P0 - Critical** | > 10% | > 5000ms | Immediate rollback | < 15 min |
| **P1 - High** | 5-10% | 2000-5000ms | Investigate 30min, then rollback if not resolved | < 1 hour |
| **P2 - Medium** | 2-5% | 1000-2000ms | Investigate, patch within 4 hours | < 4 hours |
| **P3 - Low** | < 2% | < 1000ms | Log issue, fix in next deployment | Next sprint |

---

## Post-Deployment Review

### Week 5 - Retrospective

**Agenda**:
1. What went well?
2. What could be improved?
3. Action items for future deployments
4. Update deployment playbook based on learnings

**Participants**:
- Product Owner
- Tech Lead
- Backend Engineers
- Frontend Engineers
- DevOps
- QA

**Deliverables**:
- [ ] Retrospective notes
- [ ] Action items with owners and deadlines
- [ ] Updated deployment playbook
- [ ] Lessons learned document

---

## Appendix

### A. Related Documents

| Document | Location | Purpose |
|----------|----------|---------|
| Frontend API Integration Guide | `docs/frontend/api-integration-guide-user-permissions.md` | API documentation for frontend team |
| E2E Testing Checklist | `docs/testing/e2e-permission-verification-checklist.md` | Detailed test procedures |
| Routing-v2 Implementation Report | `docs/security/routing-v2-implementation-complete.md` | Backend implementation details |
| Dashboard User Filter Guide | `docs/security/dashboard-live-user-filter-guide.md` | Dashboard implementation guide |

### B. Test Account Credentials

**211 Test Environment**:
- Super Admin: `test_superadmin` / `[REDACTED:secret]`
- Tenant Admin: `test_admin_bob` / `[REDACTED:secret]`
- Regular User: `test_user_alice` / `[REDACTED:secret]`

**245/154**: Use production accounts with appropriate roles (verify with Product Owner)

### C. Monitoring Dashboard Links

- APM: `https://apm.internal/llm-gateway`
- Database: `https://db-monitor.internal/pg-172.16.2.210`
- Error Tracking: `https://sentry.internal/llm-gateway`
- Uptime: `https://uptime.internal/llm-gateway`

### D. Escalation Contacts

| Issue Type | Primary Contact | Secondary Contact | On-Call |
|------------|----------------|-------------------|---------|
| Backend Issues | Tech Lead | Backend Team | DevOps |
| Frontend Issues | Frontend Lead | Frontend Team | Frontend On-Call |
| Database Issues | DBA | DevOps | Database On-Call |
| Security Issues | Security Lead | Tech Lead | Security On-Call |

---

**Document Status**: ✅ Complete  
**Next Review Date**: After Phase 1 completion (211/245/154 deployment)  
**Version**: 1.0  
**Approval Status**: Pending Owner Sign-Off
