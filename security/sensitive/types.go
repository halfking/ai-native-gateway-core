package sensitive

import "fmt"

// AlertLevel 敏感词告警等级
type AlertLevel int

const (
	LevelP2 AlertLevel = iota // PASS: 仅记录，不干预
	LevelP1                   // WARN: 记录 + 告警
	LevelP0                   // BLOCK: 阻断
)

func (l AlertLevel) String() string {
	switch l {
	case LevelP0:
		return "P0(BLOCK)"
	case LevelP1:
		return "P1(WARN)"
	case LevelP2:
		return "P2(PASS)"
	}
	return fmt.Sprintf("Level(%d)", l)
}

// WordCategory 敏感词分类
type WordCategory struct {
	Name  string     `json:"name"`  // 中文描述，如"政治敏感词"
	Key   string     `json:"key"`   // 内部 key，如 "political"
	Level AlertLevel `json:"level"` // 告警等级
}

// MatchResult 单次命中结果
type MatchResult struct {
	Word     string        `json:"word"`     // 命中的敏感词
	Begin    int           `json:"begin"`    // 起始位置（字节）
	End      int           `json:"end"`      // 结束位置（字节，不包含）
	Category *WordCategory `json:"category"` // 所属分类
}

// SensitiveWordConfig 配置文件顶层结构
type SensitiveWordConfig struct {
	Version     string                  `json:"version"`
	Description string                  `json:"description"`
	Categories  map[string]CategoryConf `json:"categories"`
}

// CategoryConf 配置中的分类定义
type CategoryConf struct {
	Name  string   `json:"name"`
	Words []string `json:"words"`
}

// Action 安全检测后的动作
type Action string

const (
	ActionAllow Action = "allow" // 允许通过
	ActionWarn  Action = "warn"  // 警告但允许
	ActionBlock Action = "block" // 阻止
)

// SafetyResult 内容安全评估结果
type SafetyResult struct {
	Score        float64        `json:"score"`         // 安全评分 0.0 (安全) - 1.0 (危险)
	MatchedWords []string       `json:"matched_words"` // 命中的敏感词列表
	Matches      []*MatchResult `json:"matches"`       // 详细匹配信息
	Category     string         `json:"category"`      // 主要类别
	Action       Action         `json:"action"`        // 建议动作
	Reason       string         `json:"reason"`        // 决策原因
}
