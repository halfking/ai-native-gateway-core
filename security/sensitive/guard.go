package sensitive

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/security/guardian"
)

type LevelAlertPolicy int

const (
	AlertOnP0Only  LevelAlertPolicy = iota // 仅 P0 阻断
	AlertOnP0AndP1                         // P0 阻断, P1 告警
	AlertOnAll                             // 全部阻断
)

// SensitiveInputGuard 敏感词输入侧守卫
//
// 使用 AC 自动机引擎检测输入文本中的敏感词。
// 根据 AlertLevel 决定阻断等级：
//   - LevelP0 → ActionBlock
//   - LevelP1 → ActionWarn
//   - LevelP2 → ActionPass（仅记录）
type SensitiveInputGuard struct {
	engine *SensitiveWordEngine
	name   string
}

func NewSensitiveInputGuard(engine *SensitiveWordEngine) *SensitiveInputGuard {
	return &SensitiveInputGuard{
		engine: engine,
		name:   "sensitive_word_in",
	}
}

func (g *SensitiveInputGuard) Name() string { return g.name }

func (g *SensitiveInputGuard) CheckInput(_ context.Context, reqBody string) (*guardian.GuardVerdict, error) {
	matches := g.engine.Match(reqBody)
	if len(matches) == 0 {
		return &guardian.GuardVerdict{Action: guardian.ActionPass, GuardName: g.name}, nil
	}

	blockAt := make(map[string]*MatchResult)
	warnAt := make(map[string]*MatchResult)
	for _, m := range matches {
		switch m.Category.Level {
		case LevelP0:
			blockAt[m.Word] = m
		case LevelP1:
			warnAt[m.Word] = m
		}
	}

	if len(blockAt) > 0 {
		words := keys(blockAt)
		return &guardian.GuardVerdict{
			Action:    guardian.ActionBlock,
			GuardName: g.name,
			Message:   fmt.Sprintf("sensitive word P0: %v", words),
		}, nil
	}

	if len(warnAt) > 0 {
		words := keys(warnAt)
		return &guardian.GuardVerdict{
			Action:    guardian.ActionWarn,
			GuardName: g.name,
			Message:   fmt.Sprintf("sensitive word P1: %v", words),
		}, nil
	}

	return &guardian.GuardVerdict{Action: guardian.ActionPass, GuardName: g.name}, nil
}

// SensitiveOutputGuard 敏感词输出侧守卫
//
// 检测 LLM 输出中是否含有敏感词（防止 LLM 生成违规内容）。
type SensitiveOutputGuard struct {
	engine *SensitiveWordEngine
	name   string
}

func NewSensitiveOutputGuard(engine *SensitiveWordEngine) *SensitiveOutputGuard {
	return &SensitiveOutputGuard{
		engine: engine,
		name:   "sensitive_word_out",
	}
}

func (g *SensitiveOutputGuard) Name() string { return g.name }

func (g *SensitiveOutputGuard) CheckOutput(_ context.Context, _, respBody string) (*guardian.GuardVerdict, error) {
	matches := g.engine.Match(respBody)
	if len(matches) == 0 {
		return &guardian.GuardVerdict{Action: guardian.ActionPass, GuardName: g.name}, nil
	}

	blockAt := make(map[string]*MatchResult)
	for _, m := range matches {
		if m.Category.Level == LevelP0 {
			blockAt[m.Word] = m
		}
	}

	if len(blockAt) > 0 {
		words := keys(blockAt)
		return &guardian.GuardVerdict{
			Action:    guardian.ActionBlock,
			GuardName: g.name,
			Message:   fmt.Sprintf("sensitive word in output: %v", words),
		}, nil
	}

	return &guardian.GuardVerdict{Action: guardian.ActionPass, GuardName: g.name}, nil
}

func keys(m map[string]*MatchResult) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
