package approval

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewSensitiveDetector(t *testing.T) {
	config := DetectorConfig{
		MinConfidence:   0.7,
		EnablePII:       true,
		EnableSecret:    true,
		EnableFinancial: true,
		EnableMedical:   true,
	}
	
	detector := NewSensitiveDetector(config)
	
	if detector == nil {
		t.Fatal("detector should not be nil")
	}
	
	if detector.config.MinConfidence != 0.7 {
		t.Errorf("expected MinConfidence 0.7, got %f", detector.config.MinConfidence)
	}
	
	if len(detector.patterns) == 0 {
		t.Error("patterns should not be empty")
	}
	
	if len(detector.keywords) == 0 {
		t.Error("keywords should not be empty")
	}
}

func TestDetectIDCard(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnablePII:     true,
	})
	
	tests := []struct {
		name      string
		content   string
		wantCount int
		wantType  string
	}{
		{
			name:      "有效身份证",
			content:   "我的身份证号是 110101199001011234",
			wantCount: 1,
			wantType:  "PII",
		},
		{
			name:      "多个身份证",
			content:   "身份证1: 110101199001011234, 身份证2: 320101199512125678",
			wantCount: 2,
			wantType:  "PII",
		},
		{
			name:      "无身份证",
			content:   "这是一段普通文本",
			wantCount: 0,
		},
	}
	
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detector.Detect(ctx, tt.content)
			if err != nil {
				t.Fatalf("Detect failed: %v", err)
			}
			
			idCardCount := 0
			for _, item := range result.RawItems {
				if item.Category == "id_card" {
					idCardCount++
					if item.Type != tt.wantType && tt.wantCount > 0 {
						t.Errorf("expected type %s, got %s", tt.wantType, item.Type)
					}
				}
			}
			
			if idCardCount != tt.wantCount {
				t.Errorf("expected %d id cards, got %d", tt.wantCount, idCardCount)
			}
		})
	}
}

func TestDetectPhone(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnablePII:     true,
	})
	
	tests := []struct {
		name      string
		content   string
		wantCount int
	}{
		{
			name:      "有效手机号",
			content:   "我的手机号是 13812345678",
			wantCount: 1,
		},
		{
			name:      "多个手机号",
			content:   "联系方式：13812345678, 18900001111",
			wantCount: 2,
		},
		{
			name:      "无效手机号",
			content:   "号码 12345678901 不是手机号",
			wantCount: 0,
		},
	}
	
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detector.Detect(ctx, tt.content)
			if err != nil {
				t.Fatalf("Detect failed: %v", err)
			}
			
			phoneCount := 0
			for _, item := range result.RawItems {
				if item.Category == "phone" {
					phoneCount++
				}
			}
			
			if phoneCount != tt.wantCount {
				t.Errorf("expected %d phones, got %d", tt.wantCount, phoneCount)
			}
		})
	}
}

func TestDetectEmail(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnablePII:     true,
	})
	
	tests := []struct {
		name      string
		content   string
		wantCount int
	}{
		{
			name:      "有效邮箱",
			content:   "我的邮箱是 user@example.com",
			wantCount: 1,
		},
		{
			name:      "多个邮箱",
			content:   "联系邮箱：user1@test.com, admin@company.org",
			wantCount: 2,
		},
		{
			name:      "无邮箱",
			content:   "这里没有邮箱地址",
			wantCount: 0,
		},
	}
	
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detector.Detect(ctx, tt.content)
			if err != nil {
				t.Fatalf("Detect failed: %v", err)
			}
			
			emailCount := 0
			for _, item := range result.RawItems {
				if item.Category == "email" {
					emailCount++
				}
			}
			
			if emailCount != tt.wantCount {
				t.Errorf("expected %d emails, got %d", tt.wantCount, emailCount)
			}
		})
	}
}

func TestDetectAPIKey(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnableSecret:  true,
	})
	
	tests := []struct {
		name      string
		content   string
		wantCount int
	}{
		{
			name:      "sk- 格式",
			content:   "API Key: sk-1234567890abcdef1234",
			wantCount: 1,
		},
		{
			name:      "token 格式",
			content:   "Authorization: token-abcdef123456789012345678",
			wantCount: 1,
		},
		{
			name:      "多个 key",
			content:   "key1: sk-abc123def456789012, key2: api-xyz789uvw012345678",
			wantCount: 2,
		},
	}
	
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detector.Detect(ctx, tt.content)
			if err != nil {
				t.Fatalf("Detect failed: %v", err)
			}
			
			keyCount := 0
			for _, item := range result.RawItems {
				if item.Category == "api_key" {
					keyCount++
					if item.Type != "SECRET" {
						t.Errorf("expected type SECRET, got %s", item.Type)
					}
				}
			}
			
			if keyCount != tt.wantCount {
				t.Errorf("expected %d api keys, got %d", tt.wantCount, keyCount)
			}
		})
	}
}

func TestDetectBankCard(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence:   0.7,
		EnableFinancial: true,
	})
	
	tests := []struct {
		name      string
		content   string
		wantCount int
	}{
		{
			name:      "有效银行卡号",
			content:   "银行卡号: 6222 0012 3456 7890",
			wantCount: 1,
		},
		{
			name:      "无空格格式",
			content:   "卡号 6222001234567890123",
			wantCount: 1,
		},
		{
			name:      "多个卡号",
			content:   "卡1: 6222001234567890, 卡2: 6230001234567890123",
			wantCount: 2,
		},
	}
	
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detector.Detect(ctx, tt.content)
			if err != nil {
				t.Fatalf("Detect failed: %v", err)
			}
			
			cardCount := 0
			for _, item := range result.RawItems {
				if item.Category == "bank_card" {
					cardCount++
					if item.Type != "FINANCIAL" {
						t.Errorf("expected type FINANCIAL, got %s", item.Type)
					}
				}
			}
			
			if cardCount != tt.wantCount {
				t.Errorf("expected %d bank cards, got %d", tt.wantCount, cardCount)
			}
		})
	}
}

func TestRedactIDCard(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnablePII:     true,
	})
	
	content := "我的身份证号是 110101199001011234"
	ctx := context.Background()
	
	result, err := detector.Detect(ctx, content)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	
	redacted := detector.Redact(content, result)
	
	if !strings.Contains(redacted, "110***********1234") {
		t.Errorf("expected redacted format 110***********1234, got: %s", redacted)
	}
	
	if strings.Contains(redacted, "110101199001011234") {
		t.Error("redacted content should not contain full ID card number")
	}
}

func TestRedactPhone(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnablePII:     true,
	})
	
	content := "手机号: 13812345678"
	ctx := context.Background()
	
	result, err := detector.Detect(ctx, content)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	
	redacted := detector.Redact(content, result)
	
	if !strings.Contains(redacted, "****5678") {
		t.Errorf("expected redacted format ****5678, got: %s", redacted)
	}
	
	if strings.Contains(redacted, "13812345678") {
		t.Error("redacted content should not contain full phone number")
	}
}

func TestRedactEmail(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnablePII:     true,
	})
	
	content := "邮箱: user@example.com"
	ctx := context.Background()
	
	result, err := detector.Detect(ctx, content)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	
	redacted := detector.Redact(content, result)
	
	if !strings.Contains(redacted, "u***@example.com") {
		t.Errorf("expected redacted format u***@example.com, got: %s", redacted)
	}
	
	if strings.Contains(redacted, "user@example.com") {
		t.Error("redacted content should not contain full email")
	}
}

func TestMultipleSensitiveItems(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence:   0.7,
		EnablePII:       true,
		EnableSecret:    true,
		EnableFinancial: true,
	})
	
	content := "身份证：110101199001011234 手机号：13812345678 邮箱：zhangsan@example.com 银行卡：6222001234567890 API Key: sk-abcdef1234567890"
	
	ctx := context.Background()
	result, err := detector.Detect(ctx, content)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	
	if !result.HasSensitive {
		t.Error("should detect sensitive information")
	}
	
	if len(result.TypeCounts) < 2 {
		t.Errorf("expected at least 2 types, got %d", len(result.TypeCounts))
	}
	
	redacted := detector.Redact(content, result)
	
	sensitiveValues := []string{
		"110101199001011234",
		"13812345678",
		"zhangsan@example.com",
		"6222001234567890",
		"sk-abcdef1234567890",
	}
	
	for _, value := range sensitiveValues {
		if strings.Contains(redacted, value) {
			t.Errorf("redacted content should not contain %s", value)
		}
	}
}

func TestPerformance(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence:   0.7,
		EnablePII:       true,
		EnableSecret:    true,
		EnableFinancial: true,
		EnableMedical:   true,
	})
	
	messages := make([]string, 100)
	for i := 0; i < 100; i++ {
		messages[i] = "用户信息：身份证 110101199001011234，手机 13812345678，邮箱 user@test.com"
	}
	
	ctx := context.Background()
	start := time.Now()
	
	for _, msg := range messages {
		_, err := detector.Detect(ctx, msg)
		if err != nil {
			t.Fatalf("Detect failed: %v", err)
		}
	}
	
	elapsed := time.Since(start)
	
	if elapsed > 100*time.Millisecond {
		t.Errorf("performance test failed: took %v, expected < 100ms", elapsed)
	} else {
		t.Logf("performance test passed: %v for 100 messages", elapsed)
	}
}

func TestNoSensitiveContent(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence:   0.7,
		EnablePII:       true,
		EnableSecret:    true,
		EnableFinancial: true,
		EnableMedical:   true,
	})
	
	content := "这是一段普通的文本，没有任何敏感信息。"
	ctx := context.Background()
	
	result, err := detector.Detect(ctx, content)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	
	if result.HasSensitive {
		t.Error("should not detect sensitive information")
	}
	
	if result.TotalCount != 0 {
		t.Errorf("expected 0 items, got %d", result.TotalCount)
	}
}

func TestDisableCategories(t *testing.T) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnablePII:     true,
		EnableSecret:  false,
	})
	
	content := "手机号: 13812345678, API Key: sk-1234567890abcdef"
	ctx := context.Background()
	
	result, err := detector.Detect(ctx, content)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	
	hasPhone := false
	hasAPIKey := false
	
	for _, item := range result.RawItems {
		if item.Category == "phone" {
			hasPhone = true
		}
		if item.Category == "api_key" {
			hasAPIKey = true
		}
	}
	
	if !hasPhone {
		t.Error("should detect phone number")
	}
	
	if hasAPIKey {
		t.Error("should not detect API key when secret detection is disabled")
	}
}

func BenchmarkDetect(b *testing.B) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence:   0.7,
		EnablePII:       true,
		EnableSecret:    true,
		EnableFinancial: true,
		EnableMedical:   true,
	})
	
	content := "身份证: 110101199001011234, 手机: 13812345678, 邮箱: user@test.com"
	ctx := context.Background()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = detector.Detect(ctx, content)
	}
}

func BenchmarkRedact(b *testing.B) {
	detector := NewSensitiveDetector(DetectorConfig{
		MinConfidence: 0.7,
		EnablePII:     true,
	})
	
	content := "身份证: 110101199001011234, 手机: 13812345678"
	ctx := context.Background()
	result, _ := detector.Detect(ctx, content)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = detector.Redact(content, result)
	}
}
