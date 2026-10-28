package maas

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// InvoiceRequest is a placeholder row — fulfillment is deferred to 发票一期.
type InvoiceRequest struct {
	ID        int64     `json:"id"`
	TenantID  string    `json:"tenant_id"`
	BillID    *int64    `json:"bill_id,omitempty"`
	Status    string    `json:"status"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const invoicePlaceholderNote = "invoice API placeholder — not implemented"

// CreateInvoicePlaceholder records an invoice intent without issuing a tax invoice.
func (s *Service) CreateInvoicePlaceholder(ctx context.Context, tenantID string, billID *int64) (InvoiceRequest, error) {
	if !s.Enabled() {
		return InvoiceRequest{}, errors.New("maas service disabled")
	}
	if tenantID == "" {
		return InvoiceRequest{}, fmt.Errorf("tenant_id required")
	}
	if billID != nil {
		if _, err := s.GetMonthlyBill(ctx, tenantID, *billID); err != nil {
			return InvoiceRequest{}, fmt.Errorf("bill not found for tenant")
		}
	}
	var inv InvoiceRequest
	err := s.pool.QueryRow(ctx, `
		INSERT INTO maas_invoice_requests (tenant_id, bill_id, status, note)
		VALUES ($1, $2, 'placeholder', $3)
		RETURNING id, tenant_id, bill_id, status, note, created_at, updated_at
	`, tenantID, billID, invoicePlaceholderNote).Scan(
		&inv.ID, &inv.TenantID, &inv.BillID, &inv.Status, &inv.Note, &inv.CreatedAt, &inv.UpdatedAt,
	)
	return inv, err
}
