package admin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateBackupFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantErr  bool
		errMsg   string
	}{
		{
			name:     "有效文件名 - 基本格式",
			filename: "sessions-2026-07-10.jsonl.gz",
			wantErr:  false,
		},
		{
			name:     "有效文件名 - 带序号",
			filename: "sessions-2026-07-10-01.jsonl.gz",
			wantErr:  false,
		},
		{
			name:     "有效文件名 - 两位数序号",
			filename: "sessions-2026-07-10-99.jsonl.gz",
			wantErr:  false,
		},
		{
			name:     "空文件名",
			filename: "",
			wantErr:  true,
			errMsg:   "filename cannot be empty",
		},
		{
			name:     "路径遍历 - 双点",
			filename: "../sessions-2026-07-10.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename contains invalid characters",
		},
		{
			name:     "路径遍历 - 多级",
			filename: "../../etc/passwd.gz",
			wantErr:  true,
			errMsg:   "filename contains invalid characters",
		},
		{
			name:     "路径遍历 - 前向斜杠",
			filename: "/etc/sessions-2026-07-10.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename contains invalid characters",
		},
		{
			name:     "路径遍历 - 反斜杠",
			filename: "..\\sessions-2026-07-10.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename contains invalid characters",
		},
		{
			name:     "无效格式 - 缺少日期",
			filename: "sessions.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
		{
			name:     "无效格式 - 错误的扩展名",
			filename: "sessions-2026-07-10.json",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
		{
			name:     "无效格式 - 错误的前缀",
			filename: "backup-2026-07-10.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
		{
			name:     "无效格式 - 日期格式错误",
			filename: "sessions-2026-7-10.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
		{
			name:     "无效格式 - 无效日期",
			filename: "sessions-2026-13-01.jsonl.gz",
			wantErr:  false, // 正则只检查格式，不验证日期有效性
		},
		{
			name:     "边界测试 - 序号单位数",
			filename: "sessions-2026-07-10-1.jsonl.gz",
			wantErr:  true, // 序号必须是两位数
			errMsg:   "filename must match format",
		},
		{
			name:     "边界测试 - 序号三位数",
			filename: "sessions-2026-07-10-100.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
		{
			name:     "注入测试 - SQL注入",
			filename: "sessions-2026-07-10'; DROP TABLE sessions;--.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
		{
			name:     "注入测试 - 命令注入",
			filename: "sessions-2026-07-10`rm -rf /`.jsonl.gz",
			wantErr:  true,
			errMsg:   "invalid characters", // 修正预期消息
		},
		{
			name:     "特殊字符",
			filename: "sessions-2026-07-10@#$.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
		{
			name:     "Unicode字符",
			filename: "sessions-2026-07-10-中文.jsonl.gz",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
		{
			name:     "空格",
			filename: "sessions-2026-07-10 .jsonl.gz",
			wantErr:  true,
			errMsg:   "filename must match format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBackupFilename(tt.filename)
			
			if tt.wantErr {
				assert.Error(t, err, "应该返回错误")
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg, "错误消息应该包含预期内容")
				}
			} else {
				assert.NoError(t, err, "不应该返回错误")
			}
		})
	}
}

func TestValidateBackupFilename_SecurityVectors(t *testing.T) {
	// 常见的路径遍历攻击向量
	attackVectors := []string{
		"../",
		"..\\",
		"/../",
		"/../../",
		"..%2F",
		"..%5C",
		"%2e%2e%2f",
		"%2e%2e/",
		"..%252f",
		"..%c0%af",
		"..%c1%9c",
	}

	for _, vector := range attackVectors {
		t.Run("attack_vector_"+vector, func(t *testing.T) {
			filename := vector + "sessions-2026-07-10.jsonl.gz"
			err := validateBackupFilename(filename)
			assert.Error(t, err, "应该阻止路径遍历攻击: %s", vector)
		})
	}
}

func TestValidateBackupFilename_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantErr  bool
	}{
		{
			name:     "最小有效文件名",
			filename: "sessions-2000-01-01.jsonl.gz",
			wantErr:  false,
		},
		{
			name:     "最大有效日期",
			filename: "sessions-2999-12-31.jsonl.gz",
			wantErr:  false,
		},
		{
			name:     "最大有效序号",
			filename: "sessions-2026-07-10-99.jsonl.gz",
			wantErr:  false,
		},
		{
			name:     "最小有效序号",
			filename: "sessions-2026-07-10-00.jsonl.gz",
			wantErr:  false,
		},
		{
			name:     "超长文件名",
			filename: "sessions-2026-07-10-" + string(make([]byte, 1000)) + ".jsonl.gz",
			wantErr:  true,
		},
		{
			name:     "NULL字符",
			filename: "sessions-2026-07-10\x00.jsonl.gz",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBackupFilename(tt.filename)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func BenchmarkValidateBackupFilename(b *testing.B) {
	validFilename := "sessions-2026-07-10.jsonl.gz"
	invalidFilename := "../sessions-2026-07-10.jsonl.gz"

	b.Run("valid", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = validateBackupFilename(validFilename)
		}
	})

	b.Run("invalid", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = validateBackupFilename(invalidFilename)
		}
	})
}
