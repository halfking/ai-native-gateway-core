package admin

import (
	"fmt"
	"strings"
)

// SQL 白名单验证器 - 防止 SQL 注入攻击

// AllowedColumns 定义了允许在查询中使用的列名白名单
type AllowedColumns struct {
	OrderBy map[string]bool
	Filter  map[string]bool
	Select  map[string]bool
}

var (
	// RequestLogsColumns - request_logs 表的允许列
	RequestLogsColumns = AllowedColumns{
		OrderBy: map[string]bool{
			"created_at":     true,
			"updated_at":     true,
			"total_cost":     true,
			"latency_ms":     true,
			"status":         true,
			"client_model":   true,
			"upstream_model": true,
			"session_key":    true,
			"work_type":      true,
		},
		Filter: map[string]bool{
			"tenant_id":      true,
			"session_key":    true,
			"status":         true,
			"work_type":      true,
			"provider":       true,
			"client_model":   true,
			"upstream_model": true,
			"created_at":     true,
		},
		Select: map[string]bool{
			"id":             true,
			"tenant_id":      true,
			"session_key":    true,
			"request_id":     true,
			"status":         true,
			"total_cost":     true,
			"latency_ms":     true,
			"created_at":     true,
			"client_model":   true,
			"upstream_model": true,
			"work_type":      true,
		},
	}

	// 表名到列白名单的映射
	tableColumns = map[string]*AllowedColumns{
		"request_logs": &RequestLogsColumns,
	}
)

// ValidateOrderByColumn 验证 ORDER BY 列名
func ValidateOrderByColumn(table, column string) error {
	column = strings.TrimSpace(strings.ToLower(column))
	table = strings.TrimSpace(strings.ToLower(table))

	cols, exists := tableColumns[table]
	if !exists {
		return fmt.Errorf("unknown table: %s", table)
	}

	if !cols.OrderBy[column] {
		return fmt.Errorf("invalid order by column '%s' for table '%s'", column, table)
	}

	return nil
}

// ValidateFilterColumn 验证 WHERE 子句过滤列名
func ValidateFilterColumn(table, column string) error {
	column = strings.TrimSpace(strings.ToLower(column))
	table = strings.TrimSpace(strings.ToLower(table))

	cols, exists := tableColumns[table]
	if !exists {
		return fmt.Errorf("unknown table: %s", table)
	}

	if !cols.Filter[column] {
		return fmt.Errorf("invalid filter column '%s' for table '%s'", column, table)
	}

	return nil
}

// SanitizeOrderBy 清洗并验证 ORDER BY 子句
func SanitizeOrderBy(table, orderBy string) (column string, direction string, err error) {
	orderBy = strings.TrimSpace(orderBy)
	if orderBy == "" {
		return "", "", fmt.Errorf("empty order by clause")
	}

	parts := strings.Fields(orderBy)
	column = strings.ToLower(parts[0])
	direction = "ASC"

	if len(parts) > 1 {
		dir := strings.ToUpper(parts[1])
		if dir != "ASC" && dir != "DESC" {
			return "", "", fmt.Errorf("invalid sort direction: %s", parts[1])
		}
		direction = dir
	}

	if err := ValidateOrderByColumn(table, column); err != nil {
		return "", "", err
	}

	return column, direction, nil
}

// IsSQLInjectionAttempt 检测是否为 SQL 注入尝试
func IsSQLInjectionAttempt(input string) bool {
	inputLower := strings.ToLower(input)

	dangerousPatterns := []string{
		"'", "\"", "--", "/*", "*/", ";", "#",
		"union", "select", "insert", "update", "delete", "drop",
		"exec", "execute", "script", "javascript:",
		"xp_", "sp_", "0x", "char(", "or 1=1", "or '1'='1", "and 1=1",
		"%20or%20", "%27", "%22",
	}

	for _, pattern := range dangerousPatterns {
		if strings.Contains(inputLower, pattern) {
			return true
		}
	}

	if strings.Contains(input, "#") {
		return true
	}

	return false
}
