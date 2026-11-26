# Frontend API Integration Guide - User Permission System

**Last Updated**: 2026-11-26  
**Backend Version**: feature/routing-v2-user-filter  
**Document Owner**: Backend Team  
**Target Audience**: Frontend Developers

---

## Overview

This guide provides complete API documentation for implementing the three-tier user permission system in the frontend. The backend provides role-based data filtering and credential desensitization across all API endpoints.

### Three-Tier Permission Model

| Role | Scope | Data Access | Credential Visibility |
|------|-------|-------------|----------------------|
| **user** | Personal | Own sessions, usage, routing stats | None |
| **tenant_admin** | Tenant-wide | All tenant users' data | Quota/status only (no credential_id/label/balance) |
| **super_admin** | Platform-wide | All tenants, all users | Full access |

---

## 1. Authentication & Role Detection

### 1.1 Get Current User Information

**Endpoint**: `GET /api/auth/me`

**Response**:
```json
{
  "user_id": 123,
  "username": "john.doe",
  "email": "john@example.com",
  "role": "user",           // ← Key field: "user" | "tenant_admin" | "super_admin"
  "tenant_id": "acme-corp",
  "created_at": "2026-01-15T10:30:00Z"
}
```

**Frontend Implementation**:
```typescript
// Store in Vuex/Pinia state
interface AuthState {
  user: User | null;
  role: 'user' | 'tenant_admin' | 'super_admin' | null;
  tenantId: string | null;
}

// Call on login/app initialization
async function loadUserInfo() {
  const response = await fetch('/api/auth/me');
  const user = await response.json();
  
  // Store in state management
  store.commit('setUser', user);
  store.commit('setRole', user.role);
  store.commit('setTenantId', user.tenant_id);
}
```

---

## 2. User-Level Endpoints (Personal Data)

These endpoints return data filtered to the current user automatically.

### 2.1 My Sessions

**Endpoint**: `GET /api/sessions/my`

**Query Parameters**:
- `page` (optional): Page number, default 1
- `page_size` (optional): Items per page, default 20
- `start_time` (optional): Filter by start time (RFC3339)
- `end_time` (optional): Filter by end time (RFC3339)

**Response**:
```json
{
  "sessions": [
    {
      "session_id": "sess_abc123",
      "model_name": "gpt-4",
      "start_time": "2026-11-26T10:00:00Z",
      "end_time": "2026-11-26T10:05:30Z",
      "total_turns": 5,
      "total_tokens": 1250,
      "total_cost": 0.025
    }
  ],
  "pagination": {
    "current_page": 1,
    "total_pages": 3,
    "total_items": 47
  }
}
```

**Usage**:
- Display in "My Sessions" page (all roles)
- Filter by date range
- Show session details on click

### 2.2 My Usage Statistics

**Endpoint**: `GET /api/usage/my`

**Query Parameters**:
- `start_date` (required): Start date (YYYY-MM-DD)
- `end_date` (required): End date (YYYY-MM-DD)
- `group_by` (optional): "day" | "week" | "month", default "day"

**Response**:
```json
{
  "summary": {
    "total_requests": 1250,
    "total_tokens": 450000,
    "total_cost": 12.50,
    "unique_models": 3
  },
  "breakdown": [
    {
      "date": "2026-11-26",
      "model_name": "gpt-4",
      "request_count": 45,
      "token_count": 15000,
      "cost": 0.75
    }
  ]
}
```

**Usage**:
- Display in "My Usage" dashboard (all roles)
- Show charts/graphs of usage over time
- Break down by model

---

## 3. Routing Statistics (Multi-Level Access)

### 3.1 Routing Overview

**Endpoint**: `GET /api/routing/overview`

**Permission-Filtered Response**:

**For user**:
```json
{
  "my_stats": {
    "total_requests": 1250,
    "success_rate": 0.98,
    "avg_response_time_ms": 450,
    "top_models": ["gpt-4", "claude-3-opus"]
  }
}
```

**For tenant_admin**:
```json
{
  "tenant_stats": {
    "total_requests": 15000,
    "success_rate": 0.97,
    "active_users": 25,
    "quota_used": 0.75,           // ← Visible
    "quota_remaining": 25000      // ← Visible
  },
  "models": [
    {
      "model_name": "gpt-4",
      "availability_score": 0.98,  // ← Visible
      "request_count": 5000
      // No credential details
    }
  ]
}
```

**For super_admin**:
```json
{
  "platform_stats": {
    "total_requests": 150000,
    "total_tenants": 15,
    "total_users": 350
  },
  "models": [
    {
      "model_name": "gpt-4",
      "credential_id": 42,          // ← Only visible to super_admin
      "credential_label": "prod-key-1",
      "balance": 10000,
      "request_count": 50000
    }
  ]
}
```

### 3.2 Routing Resolve (Test Routing)

**Endpoint**: `POST /api/routing/resolve`

**Request Body**:
```json
{
  "model": "gpt-4",
  "tenant_id": "acme-corp"  // Optional, auto-filled for user/tenant_admin
}
```

**Response** (credential details desensitized for non-super_admin):
```json
{
  "resolved": true,
  "candidates": [
    {
      "provider": "openai",
      "priority": 1,
      "credential_available": true,
      "quota_available": true
      // credential_id/label/balance hidden for user/tenant_admin
    }
  ]
}
```

### 3.3 Recent Model Failures

**Endpoint**: `GET /api/routing/recent-failures`

**Query Parameters**:
- `hours` (optional): Look back hours, default 24

**Permission Filtering**:
- **user**: Only see failures from own requests
- **tenant_admin**: Only see tenant's failures
- **super_admin**: See all platform failures

**Response**:
```json
{
  "failures": [
    {
      "model_name": "gpt-4",
      "failure_time": "2026-11-26T10:00:00Z",
      "error_code": "rate_limit",
      "affected_requests": 5
      // credential_id hidden for user/tenant_admin
    }
  ]
}
```

---

## 4. Frontend Implementation Guide

### 4.1 Menu Control

**Recommended Structure**:
```typescript
// router/index.ts or components/AppMenu.vue

const menuItems = computed(() => {
  const role = store.state.role;
  
  const menu = [
    // Always visible (all roles)
    { path: '/dashboard', label: 'Dashboard', icon: 'dashboard' },
  ];
  
  if (role === 'user') {
    menu.push(
      { path: '/my-sessions', label: 'My Sessions', icon: 'history' },
      { path: '/my-usage', label: 'My Usage', icon: 'chart' },
      { path: '/routing-stats', label: 'Routing Stats', icon: 'route' }
    );
  }
  
  if (role === 'tenant_admin') {
    menu.push(
      { path: '/tenant/users', label: 'User Management', icon: 'users' },
      { path: '/tenant/quota', label: 'Quota Management', icon: 'gauge' },
      { path: '/routing-stats', label: 'Routing Stats', icon: 'route' }
    );
  }
  
  if (role === 'super_admin') {
    menu.push(
      { path: '/admin/tenants', label: 'Tenant Management', icon: 'building' },
      { path: '/admin/users', label: 'User Management', icon: 'users' },
      { path: '/admin/credentials', label: 'Credentials', icon: 'key' },
      { path: '/admin/config', label: 'Platform Config', icon: 'settings' },
      { path: '/routing-stats', label: 'Routing Stats', icon: 'route' }
    );
  }
  
  return menu;
});
```

### 4.2 Route Guards

**Example Route Guard**:
```typescript
// router/index.ts

router.beforeEach((to, from, next) => {
  const role = store.state.role;
  
  // Public routes
  if (to.path === '/login' || to.path === '/') {
    return next();
  }
  
  // Check authentication
  if (!role) {
    return next('/login');
  }
  
  // Role-based access control
  const adminPaths = ['/admin', '/tenant'];
  const isAdminPath = adminPaths.some(p => to.path.startsWith(p));
  
  if (isAdminPath && role === 'user') {
    // Redirect regular users away from admin paths
    return next('/my-sessions');
  }
  
  if (to.path.startsWith('/admin') && role !== 'super_admin') {
    // Only super_admin can access /admin paths
    return next('/dashboard');
  }
  
  next();
});
```

### 4.3 Conditional Rendering

**Example Component**:
```vue
<template>
  <div class="routing-overview">
    <h2>Routing Statistics</h2>
    
    <!-- User: Personal stats only -->
    <div v-if="role === 'user'">
      <PersonalRoutingStats :data="data.my_stats" />
    </div>
    
    <!-- Tenant Admin: Tenant stats, no credential details -->
    <div v-if="role === 'tenant_admin'">
      <TenantRoutingStats :data="data.tenant_stats" />
      <ModelAvailability 
        :models="data.models"
        :show-credentials="false"  <!-- ← Hide credential columns -->
      />
    </div>
    
    <!-- Super Admin: Full access -->
    <div v-if="role === 'super_admin'">
      <PlatformRoutingStats :data="data.platform_stats" />
      <ModelAvailability 
        :models="data.models"
        :show-credentials="true"  <!-- ← Show all columns -->
      />
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue';
import { useStore } from 'vuex';

const store = useStore();
const role = computed(() => store.state.role);
const data = ref(null);

onMounted(async () => {
  const response = await fetch('/api/routing/overview');
  data.value = await response.json();
});
</script>
```

---

## 5. Error Handling

### 5.1 HTTP Status Codes

| Status | Meaning | Frontend Action |
|--------|---------|-----------------|
| 200 | Success | Display data |
| 401 | Unauthorized | Redirect to login |
| 403 | Forbidden | Show "Access Denied" message |
| 404 | Not Found | Show "Resource Not Found" |
| 500 | Server Error | Show generic error, log to monitoring |

### 5.2 Permission Errors

**Backend Response** (403 Forbidden):
```json
{
  "error": "insufficient_permissions",
  "message": "This operation requires super_admin role",
  "required_role": "super_admin",
  "current_role": "tenant_admin"
}
```

**Frontend Handling**:
```typescript
async function fetchData(endpoint: string) {
  try {
    const response = await fetch(endpoint);
    
    if (response.status === 403) {
      const error = await response.json();
      showNotification({
        type: 'error',
        title: 'Access Denied',
        message: `This feature requires ${error.required_role} role.`
      });
      return null;
    }
    
    if (response.status === 401) {
      // Session expired, redirect to login
      store.dispatch('logout');
      router.push('/login');
      return null;
    }
    
    return await response.json();
  } catch (err) {
    console.error('API Error:', err);
    showNotification({
      type: 'error',
      title: 'Network Error',
      message: 'Failed to connect to server. Please try again.'
    });
    return null;
  }
}
```

---

## 6. Testing Checklist

### 6.1 Role-Based Menu Testing

- [ ] **user** role:
  - [ ] Can see: My Sessions, My Usage, Routing Stats
  - [ ] Cannot see: Tenant Management, User Management, Admin Config
  - [ ] Clicking forbidden routes redirects to /my-sessions

- [ ] **tenant_admin** role:
  - [ ] Can see: Tenant User Management, Quota Management, Routing Stats
  - [ ] Cannot see: Platform Admin, Global Config, Other Tenants
  - [ ] Routing stats show no credential_id/label/balance

- [ ] **super_admin** role:
  - [ ] Can see: All menu items
  - [ ] Routing stats show full credential details
  - [ ] Can access all tenants' data

### 6.2 Data Filtering Testing

- [ ] User can only see own sessions in /api/sessions/my
- [ ] User can only see own usage in /api/usage/my
- [ ] Tenant admin can only see tenant's data in routing endpoints
- [ ] Credential details are hidden for user/tenant_admin roles
- [ ] Super admin can see all data across all tenants

### 6.3 Error Handling Testing

- [ ] 401 error redirects to login
- [ ] 403 error shows "Access Denied" notification
- [ ] Network errors show user-friendly message
- [ ] Error messages include required role information

---

## 7. i18n Support

If new menu items or error messages are added, include translations for all 8 supported languages:

**Required Languages**:
- English (en)
- Chinese Simplified (zh-CN)
- Chinese Traditional (zh-TW)
- Japanese (ja)
- Korean (ko)
- Spanish (es)
- French (fr)
- German (de)

**Example i18n Keys**:
```json
{
  "menu.mySessions": "My Sessions",
  "menu.myUsage": "My Usage",
  "menu.tenantUsers": "User Management",
  "menu.platformConfig": "Platform Config",
  
  "error.accessDenied": "Access Denied",
  "error.requiresRole": "This feature requires {role} role",
  "error.networkError": "Network connection failed"
}
```

---

## 8. Performance Considerations

### 8.1 Backend Optimizations

- All user-level filtering uses indexed columns (`owner_user`, `tenant_id`)
- Permission checks are performed in SQL queries, not in application code
- Credential desensitization happens at the database view level

### 8.2 Frontend Best Practices

- Cache user role in state management (don't re-fetch on every route change)
- Use route guards to prevent unnecessary API calls to forbidden endpoints
- Implement loading states for async data fetching
- Consider pagination for large datasets (sessions, usage history)

---

## 9. Rollout Plan

### Phase 1: Backend Deployment (Ready Now)
- Deploy feature branch to 211 test environment
- Verify all tests pass
- Monitor error logs for permission issues

### Phase 2: Frontend Development (1-2 sprints)
- Implement role detection on login
- Update menu component with role-based visibility
- Add route guards
- Update routing stats components to handle multi-level responses

### Phase 3: Integration Testing (1 week)
- End-to-end testing with 3 test accounts (user, tenant_admin, super_admin)
- Verify data filtering correctness
- Verify credential desensitization
- Performance testing

### Phase 4: Production Deployment
- Deploy to 245 (pre-production)
- Smoke test all roles
- Deploy to 154 (production)
- Monitor for 48 hours

---

## 10. Support & Contact

For questions or issues:
- **Backend Team**: Check `docs/security/routing-v2-implementation-complete.md`
- **API Changes**: Monitor feature branch commits
- **Testing Issues**: Review test files in `admin/*_test.go`

**Key Files for Reference**:
- Backend implementation: `admin/context_enhanced.go`, `admin/my_endpoints.go`
- Permission filters: `admin/routing.go`, `admin/dashboardapi/user_filter.go`
- Test examples: `admin/context_enhanced_test.go`, `admin/routing_permissions_test.go`

---

**Document Version**: 1.0  
**Last Reviewed**: 2026-11-26  
**Next Review**: After frontend integration complete
