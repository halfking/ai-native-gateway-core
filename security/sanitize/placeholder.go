// Package sanitize 实现可逆脱敏还原核心——智能占位符模式。
//
// 输入侧将敏感信息（手机号/身份证/邮箱等）替换为 {SENSITIVE:type:index}
// 占位符，LLM 只看到脱敏文本；输出侧检测占位符精确还原。
// 实现"敏感信息不出网关，用户体验无损"。
package sanitize

import (
	"fmt"
	"regexp"
	"strconv"
)

// SensitiveType 敏感信息类型
type SensitiveType string

const (
	TypePhone      SensitiveType = "phone"
	TypeIDCard     SensitiveType = "id_card"
	TypeEmail      SensitiveType = "email"
	TypeCreditCard SensitiveType = "credit_card"
	TypeSecret     SensitiveType = "secret"
	TypeInternalIP SensitiveType = "internal_ip"
	TypeName       SensitiveType = "name"
	TypeCustom     SensitiveType = "custom"
)

// PlaceholderPattern 匹配 {SENSITIVE:type:index} 格式占位符
var PlaceholderPattern = regexp.MustCompile(`\{SENSITIVE:([a-z_]+):(\d+)\}`)

// Placeholder 表示一个占位符
type Placeholder struct {
	Type  SensitiveType
	Index int
}

func (p Placeholder) String() string {
	return fmt.Sprintf("{SENSITIVE:%s:%d}", p.Type, p.Index)
}

// SanitizeMap 占位符→原始值的映射表
// 存储在 PipelineRequest.Metadata["sanitize_map"] 中
type SanitizeMap map[string]string

// ParsePlaceholder 从字符串解析占位符
func ParsePlaceholder(s string) (Placeholder, bool) {
	m := PlaceholderPattern.FindStringSubmatch(s)
	if len(m) != 3 {
		return Placeholder{}, false
	}
	idx, err := strconv.Atoi(m[2])
	if err != nil {
		return Placeholder{}, false
	}
	return Placeholder{Type: SensitiveType(m[1]), Index: idx}, true
}

// ValidateSensitiveType 校验敏感类型是否合法
func ValidateSensitiveType(t SensitiveType) bool {
	switch t {
	case TypePhone, TypeIDCard, TypeEmail, TypeCreditCard,
		TypeSecret, TypeInternalIP, TypeName, TypeCustom:
		return true
	}
	return false
}
