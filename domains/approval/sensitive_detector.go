// Package approval 实现审批流程领域
package approval

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// detectionItem 内部检测项（包含位置信息用于脱敏）
type detectionItem struct {
	Type       string
	Category   string
	Value      string
	StartPos   int
	EndPos     int
	Confidence float64
	Location   string
}

// DetectionResult 检测结果
type DetectionResult struct {
	Items        []SensitiveItemSummary
	RawItems     []detectionItem
	HasSensitive bool
	TotalCount   int
	TypeCounts   map[string]int
}

// SensitiveDetector 敏感信息检测器
type SensitiveDetector struct {
	mu       sync.RWMutex
	patterns map[string]*regexp.Regexp
	keywords map[string][]string
	config   DetectorConfig
}

// DetectorConfig 检测器配置
type DetectorConfig struct {
	MinConfidence   float64
	EnablePII       bool
	EnableSecret    bool
	EnableFinancial bool
	EnableMedical   bool
}

// NewSensitiveDetector 创建敏感信息检测器
func NewSensitiveDetector(config DetectorConfig) *SensitiveDetector {
	if config.MinConfidence == 0 {
		config.MinConfidence = 0.7
	}
	
	d := &SensitiveDetector{
		patterns: make(map[string]*regexp.Regexp),
		keywords: make(map[string][]string),
		config:   config,
	}
	
	d.initPatterns()
	d.initKeywords()
	
	return d
}

// initPatterns 初始化正则表达式模式
func (d *SensitiveDetector) initPatterns() {
	if d.config.EnablePII {
		d.patterns["id_card"] = regexp.MustCompile(`\b[1-9]\d{5}(18|19|20)\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`)
		d.patterns["phone"] = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
		d.patterns["email"] = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`)
		d.patterns["address"] = regexp.MustCompile(`[^\s]{2,}[省市区县镇乡村路街道巷弄号栋单元室]\s*[\d\-号栋单元室]+`)
	}
	
	if d.config.EnableSecret {
		d.patterns["api_key"] = regexp.MustCompile(`\b(sk|key|token|api|bearer)[-_]?[A-Za-z0-9]{16,}\b`)
		d.patterns["password"] = regexp.MustCompile(`(password|passwd|pwd|secret)\s*[:=]\s*[^\s]{6,}`)
		d.patterns["jwt"] = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
		d.patterns["aws_key"] = regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`)
	}
	
	if d.config.EnableFinancial {
		d.patterns["bank_card"] = regexp.MustCompile(`\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4,7}\b`)
		d.patterns["cvv"] = regexp.MustCompile(`\b(cvv|安全码)\s*[:=]?\s*\d{3,4}\b`)
		d.patterns["alipay"] = regexp.MustCompile(`(支付宝|alipay)\s*[:：]\s*([1]\d{10}|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,})`)
		d.patterns["wechat"] = regexp.MustCompile(`(微信|wechat|wx)\s*[:：]\s*[A-Za-z0-9_-]{6,20}`)
	}
	
	if d.config.EnableMedical {
		d.patterns["medical_record"] = regexp.MustCompile(`(病历号|就诊号|住院号)\s*[:：]?\s*[A-Z0-9]{6,20}`)
		d.patterns["diagnosis"] = regexp.MustCompile(`(诊断|确诊|患有|病情)\s*[:：]?\s*[\p{Han}]{2,20}`)
	}
}

// initKeywords 初始化关键词列表
func (d *SensitiveDetector) initKeywords() {
	if d.config.EnablePII {
		d.keywords["pii"] = []string{"身份证", "护照", "驾驶证", "户口", "姓名", "家庭住址", "详细地址"}
	}
	if d.config.EnableSecret {
		d.keywords["secret"] = []string{"密码", "password", "secret", "token", "api key", "私钥", "private key"}
	}
	if d.config.EnableFinancial {
		d.keywords["financial"] = []string{"银行卡", "信用卡", "储蓄卡", "账户余额", "转账", "支付宝", "微信支付"}
	}
	if d.config.EnableMedical {
		d.keywords["medical"] = []string{"病历", "诊断", "处方", "病情", "症状", "治疗方案", "手术"}
	}
}

// Detect 检测敏感信息
func (d *SensitiveDetector) Detect(ctx context.Context, content string) (*DetectionResult, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	
	rawItems := make([]detectionItem, 0)
	
	for category, pattern := range d.patterns {
		matches := pattern.FindAllStringIndex(content, -1)
		for _, match := range matches {
			startPos, endPos := match[0], match[1]
			value := content[startPos:endPos]
			
			item := detectionItem{
				Type:       d.getCategoryType(category),
				Category:   category,
				Value:      value,
				StartPos:   startPos,
				EndPos:     endPos,
				Confidence: d.calculateConfidence(category, value),
				Location:   fmt.Sprintf("pos_%d_%d", startPos, endPos),
			}
			
			if item.Confidence >= d.config.MinConfidence {
				rawItems = append(rawItems, item)
			}
		}
	}
	
	rawItems = append(rawItems, d.detectByKeywords(content)...)
	
	items := make([]SensitiveItemSummary, 0, len(rawItems))
	typeCounts := make(map[string]int)
	
	for _, raw := range rawItems {
		item := SensitiveItemSummary{
			Type:       raw.Type,
			Content:    d.redactSingleValue(raw.Category, raw.Value),
			Location:   raw.Location,
			Confidence: raw.Confidence,
		}
		items = append(items, item)
		typeCounts[raw.Type]++
	}
	
	result := &DetectionResult{
		Items:        items,
		RawItems:     rawItems,
		HasSensitive: len(items) > 0,
		TotalCount:   len(items),
		TypeCounts:   typeCounts,
	}
	
	return result, nil
}

// detectByKeywords 基于关键词检测
func (d *SensitiveDetector) detectByKeywords(content string) []detectionItem {
	items := make([]detectionItem, 0)
	
	for category, keywords := range d.keywords {
		for _, keyword := range keywords {
			if strings.Contains(strings.ToLower(content), strings.ToLower(keyword)) {
				idx := strings.Index(strings.ToLower(content), strings.ToLower(keyword))
				if idx >= 0 {
					item := detectionItem{
						Type:       d.getKeywordType(category),
						Category:   category + "_keyword",
						Value:      keyword,
						StartPos:   idx,
						EndPos:     idx + len(keyword),
						Confidence: 0.6,
						Location:   fmt.Sprintf("keyword_pos_%d_%d", idx, idx+len(keyword)),
					}
					
					if item.Confidence >= d.config.MinConfidence {
						items = append(items, item)
					}
				}
			}
		}
	}
	
	return items
}

// getCategoryType 获取类别对应的类型
func (d *SensitiveDetector) getCategoryType(category string) string {
	switch category {
	case "id_card", "phone", "email", "address":
		return "PII"
	case "api_key", "password", "jwt", "aws_key":
		return "SECRET"
	case "bank_card", "cvv", "alipay", "wechat":
		return "FINANCIAL"
	case "medical_record", "diagnosis":
		return "MEDICAL"
	default:
		return "PII"
	}
}

// getKeywordType 获取关键词类别对应的类型
func (d *SensitiveDetector) getKeywordType(category string) string {
	switch category {
	case "pii":
		return "PII"
	case "secret":
		return "SECRET"
	case "financial":
		return "FINANCIAL"
	case "medical":
		return "MEDICAL"
	default:
		return "PII"
	}
}

// calculateConfidence 计算置信度
func (d *SensitiveDetector) calculateConfidence(category, value string) float64 {
	switch category {
	case "id_card":
		return d.validateIDCard(value)
	case "phone":
		return 0.95
	case "email":
		return 0.95
	case "api_key", "jwt", "aws_key":
		return 0.9
	case "bank_card":
		return d.validateBankCard(value)
	case "password":
		return 0.8
	default:
		return 0.7
	}
}

// validateIDCard 验证身份证号
func (d *SensitiveDetector) validateIDCard(idCard string) float64 {
	if len(idCard) != 18 {
		return 0.5
	}
	
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checkCodes := []byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}
	
	sum := 0
	for i := 0; i < 17; i++ {
		digit := int(idCard[i] - '0')
		sum += digit * weights[i]
	}
	
	checkBit := idCard[17]
	if checkBit >= 'a' && checkBit <= 'z' {
		checkBit = checkBit - 'a' + 'A'
	}
	
	if checkCodes[sum%11] == checkBit {
		return 0.95
	}
	
	return 0.7
}

// validateBankCard 验证银行卡号（Luhn 算法）
func (d *SensitiveDetector) validateBankCard(cardNum string) float64 {
	cardNum = strings.ReplaceAll(cardNum, " ", "")
	cardNum = strings.ReplaceAll(cardNum, "-", "")
	
	if len(cardNum) < 16 || len(cardNum) > 19 {
		return 0.6
	}
	
	sum := 0
	isSecond := false
	
	for i := len(cardNum) - 1; i >= 0; i-- {
		digit := int(cardNum[i] - '0')
		
		if isSecond {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		
		sum += digit
		isSecond = !isSecond
	}
	
	if sum%10 == 0 {
		return 0.9
	}
	
	return 0.7
}

// Redact 脱敏处理
func (d *SensitiveDetector) Redact(content string, result *DetectionResult) string {
	if result == nil || len(result.RawItems) == 0 {
		return content
	}
	
	sortedItems := make([]detectionItem, len(result.RawItems))
	copy(sortedItems, result.RawItems)
	
	for i := 0; i < len(sortedItems)-1; i++ {
		for j := i + 1; j < len(sortedItems); j++ {
			if sortedItems[i].StartPos < sortedItems[j].StartPos {
				sortedItems[i], sortedItems[j] = sortedItems[j], sortedItems[i]
			}
		}
	}
	
	redacted := content
	for _, item := range sortedItems {
		replacement := d.redactSingleValue(item.Category, item.Value)
		redacted = redacted[:item.StartPos] + replacement + redacted[item.EndPos:]
	}
	
	return redacted
}

// redactSingleValue 脱敏单个值
func (d *SensitiveDetector) redactSingleValue(category, value string) string {
	switch category {
	case "id_card":
		if len(value) >= 7 {
			return value[:3] + "***********" + value[len(value)-4:]
		}
		return "******"
	case "phone":
		if len(value) >= 4 {
			return "****" + value[len(value)-4:]
		}
		return "****"
	case "email":
		parts := strings.Split(value, "@")
		if len(parts) == 2 && len(parts[0]) > 0 {
			return string(parts[0][0]) + "***@" + parts[1]
		}
		return "***@***.com"
	case "api_key", "jwt", "aws_key":
		if len(value) >= 8 {
			return value[:4] + "****" + value[len(value)-4:]
		}
		return "****"
	case "password":
		return "******"
	case "bank_card":
		cleaned := strings.ReplaceAll(value, " ", "")
		cleaned = strings.ReplaceAll(cleaned, "-", "")
		if len(cleaned) >= 4 {
			return "**** **** **** " + cleaned[len(cleaned)-4:]
		}
		return "**** ****"
	case "address":
		if len(value) > 6 {
			return value[:6] + "***"
		}
		return "***"
	default:
		return "***"
	}
}

// LoadPatternsFromConfig 从配置文件加载模式（预留接口）
func (d *SensitiveDetector) LoadPatternsFromConfig(configPath string) error {
	return fmt.Errorf("not implemented")
}
