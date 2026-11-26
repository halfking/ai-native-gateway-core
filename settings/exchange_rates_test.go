package settings

import (
	"os"
	"testing"
)

func TestGetExchangeRateToUSD(t *testing.T) {
	tests := []struct {
		name     string
		currency string
		want     float64
		wantErr  bool
	}{
		{"USD", "USD", 1.0, false},
		{"CNY", "CNY", 7.2, false},
		{"EUR", "EUR", 0.92, false},
		{"JPY", "JPY", 150.0, false},
		{"GBP", "GBP", 0.79, false},
		{"KRW", "KRW", 1300.0, false},
		{"HKD", "HKD", 7.8, false},
		{"SGD", "SGD", 1.35, false},
		{"Lowercase", "cny", 7.2, false}, // 应该不区分大小写
		{"WithSpaces", " EUR ", 0.92, false},
		{"Unsupported", "XXX", 0, true},
		{"Empty", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetExchangeRateToUSD(tt.currency)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetExchangeRateToUSD(%q) error = %v, wantErr %v", tt.currency, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("GetExchangeRateToUSD(%q) = %v, want %v", tt.currency, got, tt.want)
			}
		})
	}
}

func TestEnvOverride(t *testing.T) {
	// 测试环境变量覆盖
	os.Setenv("LLM_GATEWAY_CNY_TO_USD_FX", "7.0")
	defer os.Unsetenv("LLM_GATEWAY_CNY_TO_USD_FX")

	got, err := GetExchangeRateToUSD("CNY")
	if err != nil {
		t.Fatalf("GetExchangeRateToUSD(CNY) error = %v", err)
	}
	if got != 7.0 {
		t.Errorf("GetExchangeRateToUSD(CNY) = %v, want 7.0 (env override)", got)
	}
}

func TestEnvOverrideMultipleCurrencies(t *testing.T) {
	os.Setenv("LLM_GATEWAY_EUR_TO_USD_FX", "0.95")
	os.Setenv("LLM_GATEWAY_JPY_TO_USD_FX", "145.0")
	defer func() {
		os.Unsetenv("LLM_GATEWAY_EUR_TO_USD_FX")
		os.Unsetenv("LLM_GATEWAY_JPY_TO_USD_FX")
	}()

	eurRate, err := GetExchangeRateToUSD("EUR")
	if err != nil || eurRate != 0.95 {
		t.Errorf("EUR rate = %v (err=%v), want 0.95", eurRate, err)
	}

	jpyRate, err := GetExchangeRateToUSD("JPY")
	if err != nil || jpyRate != 145.0 {
		t.Errorf("JPY rate = %v (err=%v), want 145.0", jpyRate, err)
	}
}

func TestEnvOverrideInvalidValue(t *testing.T) {
	os.Setenv("LLM_GATEWAY_CNY_TO_USD_FX", "invalid")
	defer os.Unsetenv("LLM_GATEWAY_CNY_TO_USD_FX")

	// 应该fallback到默认值
	got, err := GetExchangeRateToUSD("CNY")
	if err != nil {
		t.Fatalf("GetExchangeRateToUSD(CNY) error = %v", err)
	}
	if got != 7.2 {
		t.Errorf("GetExchangeRateToUSD(CNY) = %v, want 7.2 (fallback to default)", got)
	}
}

func TestConvertToUSD(t *testing.T) {
	tests := []struct {
		name     string
		amount   float64
		currency string
		want     float64
		wantErr  bool
	}{
		{"USD to USD", 100.0, "USD", 100.0, false},
		{"Empty currency (USD)", 100.0, "", 100.0, false},
		{"CNY to USD", 72.0, "CNY", 10.0, false},
		{"EUR to USD", 92.0, "EUR", 100.0, false},
		{"JPY to USD", 15000.0, "JPY", 100.0, false},
		{"Zero amount", 0.0, "CNY", 0.0, false},
		{"Negative amount", -72.0, "CNY", -10.0, false},
		{"Unsupported currency", 100.0, "XXX", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertToUSD(tt.amount, tt.currency)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertToUSD(%v, %q) error = %v, wantErr %v", tt.amount, tt.currency, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ConvertToUSD(%v, %q) = %v, want %v", tt.amount, tt.currency, got, tt.want)
			}
		})
	}
}

func TestSupportedCurrencies(t *testing.T) {
	currencies := SupportedCurrencies()
	if len(currencies) < 8 {
		t.Errorf("SupportedCurrencies() returned %d currencies, want at least 8", len(currencies))
	}

	// 检查必须支持的币种
	required := []string{"USD", "CNY", "EUR", "JPY", "GBP"}
	currencyMap := make(map[string]bool)
	for _, c := range currencies {
		currencyMap[c] = true
	}

	for _, req := range required {
		if !currencyMap[req] {
			t.Errorf("SupportedCurrencies() missing required currency: %s", req)
		}
	}
}

func TestCNYScoreFXCompat(t *testing.T) {
	// 测试向后兼容函数
	rate := CNYScoreFXCompat()
	if rate != 7.2 {
		t.Errorf("CNYScoreFXCompat() = %v, want 7.2", rate)
	}

	// 测试环境变量覆盖也能工作
	os.Setenv("LLM_GATEWAY_CNY_TO_USD_FX", "7.5")
	defer os.Unsetenv("LLM_GATEWAY_CNY_TO_USD_FX")

	rate = CNYScoreFXCompat()
	if rate != 7.5 {
		t.Errorf("CNYScoreFXCompat() with env override = %v, want 7.5", rate)
	}
}
