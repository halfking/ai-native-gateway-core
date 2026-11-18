package commercial

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/internal/maasidem"
)

// Service implements the QuotePort interface for price estimation.
// Quoting is a pure function operation with no side effects or state changes.
//
// ★ D7（2026-11-19 审计轮）：pool 由具体类型放宽为窄接口 maasidem.Pool
// —— 「跨租户请求被拒且不产生任何写入」才能在 pgxmock 下做行为级钉死，
// 见 d7_tenant_scope_test.go。*pgxpool.Pool 天然满足该接口。
type Service struct {
	pool maasidem.Pool
}

// NewService creates a new commercial/quoting service.
func NewService(pool maasidem.Pool) *Service {
	return &Service{pool: pool}
}

// EstimateQuote produces a non-binding price quote.
// This operation does NOT create holds or modify any state.
// Per contract v0.1: EstimateQuote MUST NOT create an inventory hold.
func (s *Service) EstimateQuote(
	ctx context.Context,
	auth maascontracts.AuthContext,
	req maascontracts.EstimateQuoteRequest,
) (maascontracts.EstimateQuoteResponse, error) {
	// Get active price version or use requested version
	priceVersionID := ""
	if req.PriceVersionID != nil && *req.PriceVersionID != "" {
		priceVersionID = *req.PriceVersionID
	} else {
		// Get currently active price version
		err := s.pool.QueryRow(ctx, `
			SELECT maas_get_active_price_version(NOW())
		`).Scan(&priceVersionID)
		if err != nil || priceVersionID == "" {
			return maascontracts.EstimateQuoteResponse{}, &maascontracts.Error{
				Code:      "MAAS_PRICE_VERSION_NOT_FOUND",
				Message:   "no active price version found",
				Retryable: false,
			}
		}
	}

	// Calculate price using the pricing function
	var unitPriceMinor, totalMinor, currency, ruleID string
	err := s.pool.QueryRow(ctx, `
		SELECT unit_price_minor, total_minor, currency, rule_id
		FROM maas_calculate_price($1, $2, $3, NULL, 'on_demand')
	`, priceVersionID, req.ModelSKU, req.Quantity.Amount).Scan(
		&unitPriceMinor, &totalMinor, &currency, &ruleID,
	)

	if err == pgx.ErrNoRows {
		return maascontracts.EstimateQuoteResponse{}, &maascontracts.Error{
			Code:      "MAAS_PRICE_RULE_NOT_FOUND",
			Message:   fmt.Sprintf("no price rule found for model %s in version %s", req.ModelSKU, priceVersionID),
			Retryable: false,
		}
	}
	if err != nil {
		return maascontracts.EstimateQuoteResponse{}, fmt.Errorf("calculate price: %w", err)
	}

	// TODO: Apply price modifiers (discounts, markups, campaigns)
	// For now, use the base price without modifiers

	// Generate quote IDs
	quoteID := fmt.Sprintf("quo_%d_%s", time.Now().UnixNano(), randomHex(8))
	quoteSnapshotID := fmt.Sprintf("qsnap_%d_%s", time.Now().UnixNano(), randomHex(8))
	expiresAt := time.Now().UTC().Add(24 * time.Hour)

	// Insert quote record (for audit trail, even though it's non-binding)
	//
	// ★ D7（2026-11-19 审计轮）三点修正：
	//
	//	① **租户一致性校验**：`req.TenantID` 原先直接覆盖 `auth.TenantID`
	//	   且**零校验** ⇒ 可为**其它租户**落 maas_quotes 行。对比同层的
	//	   inventory / ledger / trading 三处都做了校验，唯独 commercial 没有。
	//	② **双写并入单事务**：quote 行与 quote_lines 行原是两次独立 Exec，
	//	   第二次失败会留下**孤儿 quote 行**（报价有记录、明细没有）。
	//	③ 契约（contracts/maascontracts/ports.go）声明 QuotePort 是
	//	   "pure function operation with no side effects"——但实现要落库
	//	   留痕。此处按「非绑定报价落库留痕」的**实际语义**实现（契约文字
	//	   的漂移登记在审计主档 D7），因为「报价快照」是下游库存/结算的锚点，
	//	   取消落库会让 hold↔quote 的关联失去依据。
	tenantID := auth.TenantID
	if req.TenantID != nil && *req.TenantID != "" {
		if *req.TenantID != auth.TenantID {
			return maascontracts.EstimateQuoteResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorUnauthorizedScope,
				Message:   "tenant_id in request does not match authenticated tenant",
				Retryable: false,
			}
		}
		tenantID = *req.TenantID
	}

	// ★ D7：两次写并入同一事务 —— 第二次失败时整体回滚，不留孤儿行。
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return maascontracts.EstimateQuoteResponse{}, fmt.Errorf("begin quote transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO maas_quotes (
			quote_id, tenant_id, quote_snapshot_id, price_version_id,
			resource_snapshot_id, status, money_amount_minor, money_currency,
			expires_at, hold_created, trace_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
	`,
		quoteID,
		tenantID,
		quoteSnapshotID,
		priceVersionID,
		req.ResourceSnapshotID,
		string(maascontracts.QuoteStatusReference),
		totalMinor,
		currency,
		expiresAt,
		false, // MUST be false per contract v0.1
		auth.TraceID,
	)
	if err != nil {
		return maascontracts.EstimateQuoteResponse{}, fmt.Errorf("insert quote: %w", err)
	}

	// Insert quote line items (for detailed breakdown)
	_, err = tx.Exec(ctx, `
		INSERT INTO maas_quote_lines (
			quote_id, line_number, model_sku, quantity_amount, quantity_unit,
			unit_price_minor, subtotal_minor, total_minor, currency,
			price_version_id, price_rule_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
	`,
		quoteID,
		1, // Line number
		req.ModelSKU,
		req.Quantity.Amount,
		req.Quantity.Unit,
		unitPriceMinor,
		totalMinor,
		totalMinor, // No discounts applied yet
		currency,
		priceVersionID,
		ruleID,
	)
	if err != nil {
		return maascontracts.EstimateQuoteResponse{}, fmt.Errorf("insert quote line: %w", err)
	}

	// ★ D7：两次写同事务 ⇒ 必须一起提交。
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.EstimateQuoteResponse{}, fmt.Errorf("commit quote transaction: %w", err)
	}

	return maascontracts.EstimateQuoteResponse{
		QuoteID:            quoteID,
		QuoteSnapshotID:    quoteSnapshotID,
		PriceVersionID:     priceVersionID,
		ResourceSnapshotID: req.ResourceSnapshotID,
		Status:             maascontracts.QuoteStatusReference,
		Money: maascontracts.Money{
			AmountMinor: totalMinor,
			Currency:    currency,
		},
		ExpiresAt:   &expiresAt,
		HoldCreated: false, // MUST be false per contract v0.1
	}, nil
}

// randomHex generates a random hex string of the specified length.
func randomHex(n int) string {
	// Simple implementation - in production use crypto/rand
	return fmt.Sprintf("%x", time.Now().UnixNano())[:n]
}

// applyModifiers applies price modifiers (discounts, markups) to a base price.
// Returns the adjusted price in minor currency units.
func (s *Service) applyModifiers(
	ctx context.Context,
	priceVersionID string,
	modelSKU string,
	tenantID string,
	basePrice decimal.Decimal,
	quantity decimal.Decimal,
) (decimal.Decimal, error) {
	// Query applicable modifiers in priority order
	rows, err := s.pool.Query(ctx, `
		SELECT modifier_id, modifier_type, modifier_value_type, modifier_value, modifier_currency
		FROM maas_price_modifiers
		WHERE price_version_id = $1
		  AND (target_type = 'global' 
		       OR (target_type = 'model' AND model_sku = $2)
		       OR (target_type = 'tenant' AND tenant_id = $3)
		       OR (target_type = 'tenant_model' AND model_sku = $2 AND tenant_id = $3))
		  AND valid_from <= NOW()
		  AND (valid_until IS NULL OR valid_until > NOW())
		  AND (min_quantity IS NULL OR $4 >= min_quantity::DECIMAL)
		  AND (max_uses IS NULL OR current_uses < max_uses)
		ORDER BY priority DESC, created_at ASC
	`, priceVersionID, modelSKU, tenantID, quantity.String())
	if err != nil {
		return basePrice, fmt.Errorf("query modifiers: %w", err)
	}
	defer rows.Close()

	adjustedPrice := basePrice
	for rows.Next() {
		var modifierID, modifierType, valueType, value string
		var currency *string

		err := rows.Scan(&modifierID, &modifierType, &valueType, &value, &currency)
		if err != nil {
			return basePrice, fmt.Errorf("scan modifier: %w", err)
		}

		modValue, err := decimal.NewFromString(value)
		if err != nil {
			continue // Skip invalid modifier values
		}

		// Apply modifier based on type
		switch valueType {
		case "percentage":
			// Apply percentage discount/markup
			if modifierType == "discount" {
				adjustedPrice = adjustedPrice.Sub(adjustedPrice.Mul(modValue).Div(decimal.NewFromInt(100)))
			} else if modifierType == "markup" {
				adjustedPrice = adjustedPrice.Add(adjustedPrice.Mul(modValue).Div(decimal.NewFromInt(100)))
			}
		case "fixed_amount":
			// Apply fixed amount discount/markup
			if modifierType == "discount" {
				adjustedPrice = adjustedPrice.Sub(modValue)
			} else if modifierType == "markup" {
				adjustedPrice = adjustedPrice.Add(modValue)
			}
		}

		// Increment usage count for campaign modifiers
		// TODO: This should be in a transaction if we track usage
	}

	if err := rows.Err(); err != nil {
		return basePrice, fmt.Errorf("iterate modifiers: %w", err)
	}

	// Ensure price never goes negative
	if adjustedPrice.LessThan(decimal.Zero) {
		adjustedPrice = decimal.Zero
	}

	return adjustedPrice, nil
}

// Verify that Service implements QuotePort interface at compile time.
var _ maascontracts.QuotePort = (*Service)(nil)
