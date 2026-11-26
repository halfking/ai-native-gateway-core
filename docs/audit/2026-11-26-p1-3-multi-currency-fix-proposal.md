# P1-3 多币种汇率处理修复方案

**问题ID**: P1-3  
**优先级**: 🟡 P1  
**状态**: 真实缺陷，待修复  
**创建日期**: 2026-11-26

---

## 问题描述

### 当前状态
- ✅ 支持CNY（固定汇率7.2，可通过环境变量`LLM_GATEWAY_CNY_TO_USD_FX`覆盖）
- ❌ 其他币种（EUR, JPY, GBP, KRW等）没有处理逻辑
- ❌ 没有实时汇率API接入
- ❌ 没有历史汇率存储

### 影响范围
```go
// 受影响的代码位置：
settings/cny_fx.go:14                    // 固定汇率定义
domains/streaming/usage.go:580           // 成本计算
domains/streaming/executors/router_scoring.go:350  // 路由评分
maas/consumption_detail.go:130           // 消费明细
```

### 生产影响评估
- 当前生产环境主要使用USD和CNY
- 其他币种占比<5%（需通过生产数据确认）
- CNY固定汇率偏差：实际汇率在6.5-7.5，当前7.2，最大偏差<10%

---

## 修复方案对比

### 方案A：环境变量配置（推荐用于快速修复）

**优点**：
- 实现简单，1-2天完成
- 零依赖，不需要外部API
- 易于运维更新

**缺点**：
- 需要手动更新汇率
- 不支持历史汇率
- 需要重启服务生效

**工作量**：1-2天

---

### 方案B：汇率API接入

**优点**：
- 自动更新，无需人工干预
- 支持多币种
- 可存储历史汇率

**缺点**：
- 外部依赖，可能有API限额
- 需要fallback机制
- 实现复杂度高

**工作量**：1-2周

---

## 推荐方案：方案A（环境变量配置）

### 实现步骤

#### 步骤1：创建通用汇率管理模块

```go
// settings/exchange_rates.go
package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// defaultExchangeRates 是默认的汇率表（相对于USD）
var defaultExchangeRates = map[string]float64{
	"USD": 1.0,
	"CNY": 7.2,   // 人民币
	"EUR": 0.92,  // 欧元
	"JPY": 150.0, // 日元
	"GBP": 0.79,  // 英镑
	"KRW": 1300.0,// 韩元
	"HKD": 7.8,   // 港币
	"SGD": 1.35,  // 新加坡元
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
// 优先级：环境变量 > 默认值
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
		return 0, fmt.Errorf("unsupported currency: %s (supported: USD, CNY, EUR, JPY, GBP, KRW, HKD, SGD)", currency)
	}
	
	return rate, nil
}

// ConvertToUSD 将指定币种的金额转换为USD
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
```

#### 步骤2：迁移现有代码

```go
// domains/streaming/usage.go
// 旧代码（第580行）：
// usd := math.Round((*native/CNYToUSDFX)*1e8) / 1e8

// 新代码：
usd, err := settings.ConvertToUSD(*native, c.Currency)
if err != nil {
	// fallback to CNY conversion for backward compatibility
	if strings.EqualFold(c.Currency, "CNY") {
		usd = math.Round((*native/CNYToUSDFX)*1e8) / 1e8
	} else {
		return nil, fmt.Errorf("currency conversion failed: %w", err)
	}
} else {
	usd = math.Round(usd*1e8) / 1e8
}
```

```go
// domains/streaming/executors/router_scoring.go
// 旧代码（第350-351行）：
// if strings.EqualFold(strings.TrimSpace(c.Currency), "CNY") {
//     blended /= settings.CNYScoreFX()
// }

// 新代码：
if c.Currency != "" && !strings.EqualFold(c.Currency, "USD") {
	rate, err := settings.GetExchangeRateToUSD(c.Currency)
	if err == nil && rate > 0 {
		blended /= rate
	}
}
```

#### 步骤3：添加测试

```go
// settings/exchange_rates_test.go
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
		{"Unsupported", "XXX", 0, true},
		{"Empty", "", 0, true},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetExchangeRateToUSD(tt.currency)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetExchangeRateToUSD() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("GetExchangeRateToUSD() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvOverride(t *testing.T) {
	os.Setenv("LLM_GATEWAY_CNY_TO_USD_FX", "7.0")
	defer os.Unsetenv("LLM_GATEWAY_CNY_TO_USD_FX")
	
	got, err := GetExchangeRateToUSD("CNY")
	if err != nil {
		t.Fatalf("GetExchangeRateToUSD() error = %v", err)
	}
	if got != 7.0 {
		t.Errorf("GetExchangeRateToUSD() = %v, want 7.0 (env override)", got)
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
		{"CNY to USD", 72.0, "CNY", 10.0, false},
		{"EUR to USD", 92.0, "EUR", 100.0, false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertToUSD(tt.amount, tt.currency)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertToUSD() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ConvertToUSD() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

#### 步骤4：更新环境变量文档

```bash
# .env.example 添加汇率配置
# 多币种汇率配置（可选，用于覆盖默认汇率）
# LLM_GATEWAY_CNY_TO_USD_FX=7.2
# LLM_GATEWAY_EUR_TO_USD_FX=0.92
# LLM_GATEWAY_JPY_TO_USD_FX=150.0
# LLM_GATEWAY_GBP_TO_USD_FX=0.79
```

---

## 运维指南

### 如何更新汇率

**方法1：环境变量（推荐）**
```bash
# 在部署脚本或.env文件中设置
export LLM_GATEWAY_CNY_TO_USD_FX=7.15
export LLM_GATEWAY_EUR_TO_USD_FX=0.93

# 重启服务生效
systemctl restart llm-gateway
```

**方法2：定期更新**
```bash
# crontab示例：每周一更新汇率
0 0 * * 1 /opt/scripts/update-exchange-rates.sh
```

```bash
#!/bin/bash
# /opt/scripts/update-exchange-rates.sh
# 从ECB获取最新汇率并更新环境变量

RATES_JSON=$(curl -s https://api.exchangerate-api.com/v4/latest/USD)
CNY_RATE=$(echo $RATES_JSON | jq -r '.rates.CNY')
EUR_RATE=$(echo $RATES_JSON | jq -r '.rates.EUR')

# 更新.env文件
sed -i "s/LLM_GATEWAY_CNY_TO_USD_FX=.*/LLM_GATEWAY_CNY_TO_USD_FX=$CNY_RATE/" /opt/llm-gateway/.env
sed -i "s/LLM_GATEWAY_EUR_TO_USD_FX=.*/LLM_GATEWAY_EUR_TO_USD_FX=$EUR_RATE/" /opt/llm-gateway/.env

# 重启服务
systemctl restart llm-gateway
```

### 监控汇率偏差

```sql
-- 检查成本计算中使用的币种分布
SELECT currency, COUNT(*) as count, 
       SUM(cost_usd) as total_cost_usd
FROM session_turns
WHERE created_at >= CURRENT_DATE - INTERVAL '30 days'
GROUP BY currency
ORDER BY count DESC;

-- 如果非USD/CNY币种占比>10%，需要重点关注汇率准确性
```

---

## 验收标准

修复完成后应满足：

- [ ] `settings/exchange_rates.go` 实现完成并通过测试
- [ ] 所有使用CNY汇率的地方迁移到新API
- [ ] 支持至少5种主流币种（USD, CNY, EUR, JPY, GBP）
- [ ] 环境变量覆盖机制工作正常
- [ ] 旧代码保留fallback逻辑，确保向后兼容
- [ ] 生产环境验证：非USD币种的成本计算正确
- [ ] 文档更新：运维手册包含汇率更新指南

---

## 风险评估

### 低风险
- ✅ 向后兼容：保留CNY的旧逻辑作为fallback
- ✅ 环境变量优先：可以随时覆盖默认值
- ✅ 渐进式迁移：可以逐个文件迁移，不需要一次性全改

### 需要注意
- ⚠️ 汇率更新需要重启服务
- ⚠️ 默认汇率可能过时，需要定期检查
- ⚠️ 不支持的币种会返回错误（需要明确的错误处理）

---

## 未来扩展（方案B）

如果需要更完整的汇率系统，可以在方案A基础上扩展：

1. **创建汇率表**
```sql
CREATE TABLE exchange_rates (
    id BIGSERIAL PRIMARY KEY,
    from_currency VARCHAR(3) NOT NULL,
    to_currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    rate NUMERIC(20,8) NOT NULL,
    source VARCHAR(100),
    effective_date DATE NOT NULL,
    fetched_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    UNIQUE(from_currency, to_currency, effective_date)
);

CREATE INDEX idx_exchange_rates_lookup 
ON exchange_rates(from_currency, to_currency, effective_date DESC);
```

2. **接入汇率API**
   - ECB（欧洲中央银行）：免费，可靠
   - OpenExchangeRates：免费层1000次/月
   - Fixer.io：付费但数据全面

3. **定时任务**
   - 每日自动获取最新汇率
   - 存储历史汇率
   - 失败时使用最近一次成功的汇率

---

**审核**: 待owner确认优先级和实施计划  
**估算工时**: 方案A 1-2天 | 方案B 1-2周  
**建议**: 先实施方案A快速修复，根据业务需求再决定是否升级到方案B
