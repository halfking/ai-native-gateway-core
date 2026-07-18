package sensitive

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
	"github.com/kaixuan/llm-gateway-go/domains/security"
)

// SensitiveWordPlugin 基于 AC 自动机的敏感词治理插件。
//
// 实现 security.Plugin 接口，可注册到 V4 治理平台的 security.Registry，
// 在 PhaseGovernance 阶段执行。
type SensitiveWordPlugin struct {
	engine    *SensitiveWordEngine
	direction string
}

// NewSensitiveWordInputPlugin 输入侧插件：检查用户请求中的敏感词。
func NewSensitiveWordInputPlugin(engine *SensitiveWordEngine) *SensitiveWordPlugin {
	return &SensitiveWordPlugin{
		engine:    engine,
		direction: security.DirectionInput,
	}
}

// NewSensitiveWordOutputPlugin 输出侧插件：检查 LLM 响应中的敏感词。
func NewSensitiveWordOutputPlugin(engine *SensitiveWordEngine) *SensitiveWordPlugin {
	return &SensitiveWordPlugin{
		engine:    engine,
		direction: security.DirectionOutput,
	}
}

func (p *SensitiveWordPlugin) Name() string {
	msg := "sensitive_word"
	if p.direction == security.DirectionInput {
		return msg + "_input"
	}
	return msg + "_output"
}

func (p *SensitiveWordPlugin) Direction() string {
	return p.direction
}

func (p *SensitiveWordPlugin) Inspect(_ context.Context, env *domain.PipelineRequest) (*governance.Verdict, error) {
	if p.engine == nil {
		return pass(p.Name()), nil
	}
	var body string
	if p.direction == security.DirectionInput {
		if env.TransformedRequest == nil {
			return pass(p.Name()), nil
		}
		body = string(env.TransformedRequest)
	} else {
		if env.UpstreamResponse == nil {
			return pass(p.Name()), nil
		}
		body = string(env.UpstreamResponse)
	}
	matches := p.engine.Match(body)
	if len(matches) == 0 {
		return pass(p.Name()), nil
	}
	return p.buildVerdict(matches)
}

func (p *SensitiveWordPlugin) buildVerdict(matches []*MatchResult) (*governance.Verdict, error) {
	var worst AlertLevel
	words := make([]string, 0)
	seen := make(map[string]bool)
	for _, m := range matches {
		if seen[m.Word] {
			continue
		}
		seen[m.Word] = true
		words = append(words, m.Word)
		if m.Category.Level > worst {
			worst = m.Category.Level
		}
	}
	sev := worstLevelToSeverity(worst)

	ev := map[string]any{
		"matched_words": words,
		"count":         len(words),
		"level":         worst.String(),
	}

	switch worst {
	case LevelP0:
		return &governance.Verdict{
			PluginName: p.Name(),
			Allow:      false,
			Severity:   sev,
			Code:       "sensitive_word.P0",
			Reason:     "sensitive word P0 blocked",
			Evidence:   ev,
		}, nil
	case LevelP1:
		return &governance.Verdict{
			PluginName: p.Name(),
			Allow:      false,
			Severity:   sev,
			Code:       "sensitive_word.P1",
			Reason:     "sensitive word P1 warned",
			Evidence:   ev,
		}, nil
	default:
		return pass(p.Name()), nil
	}
}

func pass(name string) *governance.Verdict {
	return &governance.Verdict{
		PluginName: name,
		Allow:      true,
		Severity:   0,
		Code:       "ok",
		Reason:     "no sensitive word matched",
	}
}

func worstLevelToSeverity(l AlertLevel) int {
	switch l {
	case LevelP0:
		return 2 // governance block
	case LevelP1:
		return 1 // governance warn
	default:
		return 0 // info
	}
}
