package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// defaultExchangeRates 是默认的汇率表（相对于USD）
// 汇率数据来源：2026-11-26 市场中间价
var defaultExchangeRates = map[string]float64{
	"USD": 1.0,
	"CNY": 7.2,    // 人民币
	"EUR": 0.92,   // 欧元
	"JPY": 150.0,  // 日元
	"GBP": 0.79,   // 英镑
	"KRW": 1300.0, // 韩元
	"HKD": 7.8,    // 港币
	"SGD": 1.35,   // 新加坡元
}

var (
	exchangeRatesMu    sync.RWMutex
	exchangeRatesCache map[string]float64
)

func init() {
	exchangeRatesCache = make(map[string]float64)
	for currency, rate := range defaultExchangeRates {
		exchangeRatesCache[currency] = rate
	}
}

// GetExchangeRateToUSD 返回指定币种到USD的汇率
//
// 优先级：环境变量 > 默认值
//
// 环境变量格式：LLM_GATEWAY_{CURRENCY}_TO_USD_FX
// 例如：LLM_GATEWAY_CNY_TO_USD_FX=7.15
func GetExchangeRateToUSD(currency string) (float64, error) {
	if currency == "" {
		return 0, fmt.Errorf("currency is empty")
	}

	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "USD" {
		return 1.0, nil
	}

	// 检查环境变量覆盖
	envKey := fmt.Sprintf("LLM_GATEWAY_%s_TO_USD_FX", currency)
	if override := os.Getenv(envKey); override != "" {
		if v, err := strconv.ParseFloat(override, 64); err == nil && v > 0 {
			return v, nil
		}
	}

	// 使用默认汇率
	exchangeRatesMu.RLock()
	rate, ok := exchangeRatesCache[currency]
	exchangeRatesMu.RUnlock()

	if !ok {
		return 0, fmt.Errorf("unsupported currency: %s (supported: %v)", currency, SupportedCurrencies())
	}

	return rate, nil
}

// ConvertToUSD 将指���币种的金额转换为USD
func ConvertToUSD(amount float64, fromCurrency string) (float64, error) {
	if fromCurrency == "" || strings.EqualFold(fromCurrency, "USD") {
		return amount, nil
	}

	rate, err := GetExchangeRateToUSD(fromCurrency)
	if err != nil {
		return 0, err
	}

	return amount / rate, nil
}

// SupportedCurrencies 返回支持的币种列表
func SupportedCurrencies() []string {
	exchangeRatesMu.RLock()
	defer exchangeRatesMu.RUnlock()

	currencies := make([]string, 0, len(exchangeRatesCache))
	for currency := range exchangeRatesCache {
		currencies = append(currencies, currency)
	}
	return currencies
}

// CNYScoreFXCompat 是兼容旧代码的CNY汇率函数
// 优先使用 GetExchangeRateToUSD("CNY")，仅在需要向后兼容时使用此函数
func CNYScoreFXCompat() float64 {
	rate, err := GetExchangeRateToUSD("CNY")
	if err != nil {
		return CNYToUSDFX // fallback to old constant
	}
	return rate
}
