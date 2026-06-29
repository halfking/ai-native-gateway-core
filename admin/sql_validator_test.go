package admin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateOrderByColumn(t *testing.T) {
	tests := []struct {
		name      string
		table     string
		column    string
		shouldErr bool
	}{
		{"合法列", "request_logs", "created_at", false},
		{"大小写不敏感", "request_logs", "CREATED_AT", false},
		{"不存在的表", "unknown_table", "id", true},
		{"不在白名单的列", "request_logs", "password", true},
		{"SQL注入尝试", "request_logs", "id; DROP TABLE users--", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOrderByColumn(tt.table, tt.column)
			if tt.shouldErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSQLInjectionVectors(t *testing.T) {
	vectors := []struct {
		name  string
		input string
	}{
		{"经典单引号", "created_at' OR '1'='1"},
		{"UNION注入", "created_at UNION SELECT * FROM api_keys--"},
		{"堆叠查询", "created_at; DROP TABLE users--"},
		{"时间盲注", "created_at AND SLEEP(5)--"},
		{"布尔盲注", "created_at AND 1=1--"},
		{"注释绕过", "created_at/**/OR/**/1=1"},
		{"编码绕过", "created_at%20OR%201=1"},
		{"OR 1=1", "id OR 1=1--"},
		{"注释符", "id--"},
	}

	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			err := ValidateOrderByColumn("request_logs", v.input)
			assert.Error(t, err, "向量应该被拒绝: %s", v.input)

			isInjection := IsSQLInjectionAttempt(v.input)
			assert.True(t, isInjection, "应该被识别为注入尝试: %s", v.input)
		})
	}
}

func TestIsSQLInjectionAttempt(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"单引号", "value'", true},
		{"UNION", "value UNION SELECT", true},
		{"DROP", "DROP TABLE users", true},
		{"普通列名", "created_at", false},
		{"下划线", "total_cost", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsSQLInjectionAttempt(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
