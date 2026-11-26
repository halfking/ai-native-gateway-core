package maas

import (
	"context"
)

// CalculateCreditsInput 积分计算输入
type CalculateCreditsInput struct {
	TenantID      string
	CanonicalName string
	Usage         TokenUsage
	RateMultiplier float64 // 峰谷倍率，<=0 时使用 1.0
}

// CalculateCreditsResult 积分计算结果
type CalculateCreditsResult struct {
	Credits        int64   // 计算出的积分
	RateMultiplier float64 // 实际使用的倍率（规范化后）
	RateIn         int64   // 输入token费率（credits/1M）
	RateOut        int64   // 输出token费率（credits/1M）
	GlobalDiscount float64 // 全局折扣
}

// CalculateCredits 计算请求的积分成本（不扣减钱包）
//
// P0-3方案C：分离"计算积分"和"扣减钱包"两个步骤。
// 此方法仅计算积分，供handler在完成态立即调用，计算结果写入telemetry。
// 钱包扣减由异步流程完成。
//
// 参数：
//   - ctx: 上下文
//   - input: 计算输入（租户、模型、用量、倍率）
//
// 返回：
//   - result: 计算结果（积分数、费率、折扣等）
//   - error: 计算失败时的错误
//
// 失败策略：
//   - 如果MaaS未启用，返回0积分（nil error）
//   - 如果查询费率失败，返回error
//   - 如果用量为0，返回0积分（nil error）
func (s *Service) CalculateCredits(ctx context.Context, input CalculateCreditsInput) (*CalculateCreditsResult, error) {
	// 1. 规范化倍率（无论是否启用MaaS都要规范化）
	rateMultiplier := input.RateMultiplier
	if rateMultiplier <= 0 {
		rateMultiplier = 1.0
	}

	// 2. MaaS未启用：返回0
	if !s.Enabled() {
		return &CalculateCreditsResult{
			Credits:        0,
			RateMultiplier: rateMultiplier,
		}, nil
	}

	// 3. 用量为空：返回0
	if !input.Usage.Any() {
		return &CalculateCreditsResult{
			Credits:        0,
			RateMultiplier: rateMultiplier,
		}, nil
	}

	// 4. 查询费率
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}

	rates, err := s.modelRateValues(ctx, input.CanonicalName, settings)
	if err != nil {
		return nil, err
	}

	// 5. 计算积分（应用倍率）
	credits := CalcCreditsMultimodalWithMultiplier(input.Usage, rates, rateMultiplier)

	// 6. 返回结果（包含费率信息，用于审计）
	return &CalculateCreditsResult{
		Credits:        credits,
		RateMultiplier: rateMultiplier,
		RateIn:         rates.In,
		RateOut:        rates.Out,
		GlobalDiscount: settings.GlobalDiscount,
	}, nil
}
