package sessionanalytics

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// TestParseOverviewParams 测试总体统计参数解析
func TestParseOverviewParams(t *testing.T) {
	tests := []struct {
		name        string
		queryParams map[string]string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid params",
			queryParams: map[string]string{
				"start_date": "2024-01-01",
				"end_date":   "2024-01-31",
			},
			wantErr: false,
		},
		{
			name: "valid params with tenant_id",
			queryParams: map[string]string{
				"tenant_id":  "tenant123",
				"start_date": "2024-01-01",
				"end_date":   "2024-01-31",
			},
			wantErr: false,
		},
		{
			name: "missing start_date",
			queryParams: map[string]string{
				"end_date": "2024-01-31",
			},
			wantErr:     true,
			errContains: "start_date is required",
		},
		{
			name: "missing end_date",
			queryParams: map[string]string{
				"start_date": "2024-01-01",
			},
			wantErr:     true,
			errContains: "end_date is required",
		},
		{
			name: "invalid date format",
			queryParams: map[string]string{
				"start_date": "2024/01/01",
				"end_date":   "2024-01-31",
			},
			wantErr:     true,
			errContains: "invalid start_date format",
		},
		{
			name: "start_date after end_date",
			queryParams: map[string]string{
				"start_date": "2024-02-01",
				"end_date":   "2024-01-31",
			},
			wantErr:     true,
			errContains: "start_date must be before",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			// 设置查询参数
			q := url.Values{}
			for k, v := range tt.queryParams {
				q.Add(k, v)
			}
			req.URL.RawQuery = q.Encode()

			params, err := parseOverviewParamsFromRequest(req)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, params)
				assert.Equal(t, tt.queryParams["start_date"], params.StartDate)
				assert.Equal(t, tt.queryParams["end_date"], params.EndDate)

				if tid, ok := tt.queryParams["tenant_id"]; ok {
					assert.NotNil(t, params.TenantID)
					assert.Equal(t, tid, *params.TenantID)
				} else {
					assert.Nil(t, params.TenantID)
				}
			}
		})
	}
}

// TestParseUserRankingParams 测试用户排行参数解析
func TestParseUserRankingParams(t *testing.T) {
	tests := []struct {
		name        string
		queryParams map[string]string
		wantErr     bool
		errContains string
		checkFields func(*testing.T, *userRankingParams)
	}{
		{
			name: "valid params with defaults",
			queryParams: map[string]string{
				"start_date": "2024-01-01",
				"end_date":   "2024-01-31",
			},
			wantErr: false,
			checkFields: func(t *testing.T, p *userRankingParams) {
				assert.Equal(t, "tokens", p.SortBy)
				assert.Equal(t, "desc", p.Order)
				assert.Equal(t, 50, p.Limit)
				assert.Equal(t, 0, p.Offset)
			},
		},
		{
			name: "valid params with all options",
			queryParams: map[string]string{
				"start_date": "2024-01-01",
				"end_date":   "2024-01-31",
				"project":    "test-project",
				"sort_by":    "cost",
				"order":      "asc",
				"limit":      "100",
				"offset":     "50",
			},
			wantErr: false,
			checkFields: func(t *testing.T, p *userRankingParams) {
				assert.NotNil(t, p.Project)
				assert.Equal(t, "test-project", *p.Project)
				assert.Equal(t, "cost", p.SortBy)
				assert.Equal(t, "asc", p.Order)
				assert.Equal(t, 100, p.Limit)
				assert.Equal(t, 50, p.Offset)
			},
		},
		{
			name: "invalid sort_by",
			queryParams: map[string]string{
				"start_date": "2024-01-01",
				"end_date":   "2024-01-31",
				"sort_by":    "invalid",
			},
			wantErr:     true,
			errContains: "invalid sort_by",
		},
		{
			name: "invalid order",
			queryParams: map[string]string{
				"start_date": "2024-01-01",
				"end_date":   "2024-01-31",
				"order":      "invalid",
			},
			wantErr:     true,
			errContains: "invalid order",
		},
		{
			name: "limit too large",
			queryParams: map[string]string{
				"start_date": "2024-01-01",
				"end_date":   "2024-01-31",
				"limit":      "300",
			},
			wantErr:     true,
			errContains: "invalid limit",
		},
		{
			name: "negative offset",
			queryParams: map[string]string{
				"start_date": "2024-01-01",
				"end_date":   "2024-01-31",
				"offset":     "-10",
			},
			wantErr:     true,
			errContains: "invalid offset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			// 设置查询参数
			q := url.Values{}
			for k, v := range tt.queryParams {
				q.Add(k, v)
			}
			req.URL.RawQuery = q.Encode()

			params, err := parseUserRankingParamsFromRequest(req)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, params)
				if tt.checkFields != nil {
					tt.checkFields(t, params)
				}
			}
		})
	}
}

// TestIsValidDate 测试日期格式验证
func TestIsValidDate(t *testing.T) {
	tests := []struct {
		name     string
		dateStr  string
		expected bool
	}{
		{"valid date", "2024-01-01", true},
		{"valid date leap year", "2024-02-29", true},
		{"invalid format slash", "2024/01/01", false},
		{"invalid format dots", "2024.01.01", false},
		{"invalid format no separator", "20240101", false},
		{"invalid month", "2024-13-01", false},
		{"invalid day", "2024-01-32", false},
		{"invalid leap year", "2023-02-29", false},
		{"empty string", "", false},
		{"partial date", "2024-01", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidDate(tt.dateStr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsAdmin 测试管理员权限检查
func TestIsAdmin(t *testing.T) {
	tests := []struct {
		name     string
		userRole interface{}
		expected bool
	}{
		{"super admin", "super_admin", true},
		// ★ 真实角色词表（真库 users 表实测）：super_admin / tenant_admin；
		//   另有 admin_key（遗留 admin key 认证）。没有 "admin" 这个角色。
		{"legacy admin key", "admin_key", true},
		{"tenant admin", "tenant_admin", true},
		{"plain admin is not a role", "admin", false},
		{"user", "user", false},
		{"empty role", "", false},
		{"nil role", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			// ★ 身份注入必须走 admin.SetAuthContext（仓内认证中间件的唯一真源）。
			//   此前测试用 context.WithValue(ctx, "user_role", ...) —— 与生产
			//   路径**不同的键** ⇒ 测试绿而生产恒 403（见 handlers.go 的说明）。
			//   ⇒ 这类"测试自己造了一套假契约"的洞，只有把测试改成走真入口才补得上。
			if tt.userRole != nil {
				role, _ := tt.userRole.(string)
				req = admin.SetAuthContext(req, &admin.AuthContext{Role: role, TenantID: "tenant123"})
			}

			result := isAdminFromRequest(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsSuperAdmin 测试超级管理员权限检查
func TestIsSuperAdmin(t *testing.T) {
	tests := []struct {
		name     string
		userRole interface{}
		expected bool
	}{
		{"super admin", "super_admin", true},
		// ★ 角色词表 = 真库的 super_admin / tenant_admin / admin_key（真库 users
		//   表实测：tenant_admin 21 个、super_admin 3 个，没有 "admin" 角色）。
		//   此前测试把 "admin" 当成合法角色固化了下来，是一条永不命中的分支。
		{"legacy admin key", "admin_key", true},
		{"tenant admin", "tenant_admin", false},
		{"plain admin is not a role", "admin", false},
		{"user", "user", false},
		{"nil role", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			if tt.userRole != nil {
				role, _ := tt.userRole.(string)
				req = admin.SetAuthContext(req, &admin.AuthContext{Role: role, TenantID: "tenant123"})
			}

			result := isSuperAdminFromRequest(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetUserTenantID 测试获取用户租户 ID
func TestGetUserTenantID(t *testing.T) {
	tests := []struct {
		name     string
		tenantID interface{}
		expected string
	}{
		{"valid tenant_id", "tenant123", "tenant123"},
		{"empty tenant_id", "", ""},
		{"nil tenant_id", nil, ""},
		{"wrong type", 123, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			// ★ 同上：身份注入走 admin.SetAuthContext（唯一真源）。
			if tt.tenantID != nil {
				tid, _ := tt.tenantID.(string)
				req = admin.SetAuthContext(req, &admin.AuthContext{Role: "super_admin", TenantID: tid})
			}

			result := getUserTenantIDFromRequest(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestHandleGetOverview_Unauthorized 测试未授权访问
func TestHandleGetOverview_Unauthorized(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/overview?start_date=2024-01-01&end_date=2024-01-31", nil)
	rec := httptest.NewRecorder()

	// 不注入身份 ⇒ 模拟未授权用户
	handler := &Handler{db: nil}
	handler.HandleGetOverview(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestHandleGetUserRanking_InvalidParams 测试无效参数
func TestHandleGetUserRanking_InvalidParams(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/users", nil) // 缺少必需参数
	rec := httptest.NewRecorder()

	req = admin.SetAuthContext(req, &admin.AuthContext{Role: "super_admin"})

	handler := &Handler{db: nil}
	handler.HandleGetUserRanking(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
