package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ModelConfigValidator periodically validates and auto-fixes provider model configurations.
// It ensures that outbound_model_name is properly set for all models, especially for
// volcano-ark and other providers that require explicit endpoint names.
type ModelConfigValidator struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
}

// NewModelConfigValidator creates a new validator that runs every 30 minutes.
func NewModelConfigValidator(db *pgxpool.Pool) *ModelConfigValidator {
	return &ModelConfigValidator{
		db:   db,
		done: make(chan struct{}),
	}
}

// Start begins the validation loop in the background.
func (v *ModelConfigValidator) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	v.cancel = cancel

	go func() {
		defer close(v.done)
		
		// Run immediately on startup
		v.runOnce(ctx)
		
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				v.runOnce(ctx)
			}
		}
	}()
	
	slog.Info("model_config_validator started", "interval", "30m")
}

// Stop stops the validator.
func (v *ModelConfigValidator) Stop() {
	if v.cancel != nil {
		v.cancel()
	}
	<-v.done
	slog.Info("model_config_validator stopped")
}

// runOnce performs a single validation and auto-fix pass.
func (v *ModelConfigValidator) runOnce(ctx context.Context) {
	slog.Debug("model_config_validator: starting validation pass")
	
	// Fix 1: Auto-fill outbound_model_name for volcano-ark providers
	if err := v.autoFillVolcanoOutboundNames(ctx); err != nil {
		slog.Warn("model_config_validator: autoFillVolcanoOutboundNames failed", "error", err)
	}
	
	// Fix 2: Detect and log mismatched endpoint names
	if err := v.detectMismatchedEndpoints(ctx); err != nil {
		slog.Warn("model_config_validator: detectMismatchedEndpoints failed", "error", err)
	}
	
	slog.Debug("model_config_validator: validation pass completed")
}

// autoFillVolcanoOutboundNames automatically fills outbound_model_name for volcano-ark
// providers where it's NULL or empty, using standardized_name or raw_model_name as fallback.
func (v *ModelConfigValidator) autoFillVolcanoOutboundNames(ctx context.Context) error {
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	
	result, err := v.db.Exec(timeout, `
		UPDATE provider_models pm
		SET outbound_model_name = COALESCE(pm.standardized_name, pm.raw_model_name),
		    updated_at = NOW()
		FROM providers p
		WHERE pm.provider_id = p.id
		  AND p.code LIKE 'volcano%'
		  AND (pm.outbound_model_name IS NULL OR pm.outbound_model_name = '')
	`)
	
	if err != nil {
		return err
	}
	
	rowsAffected := result.RowsAffected()
	if rowsAffected > 0 {
		slog.Info("model_config_validator: auto-filled volcano outbound_model_name",
			"rows_updated", rowsAffected)
	}
	
	return nil
}

// detectMismatchedEndpoints detects provider_models where outbound_model_name differs
// significantly from standardized_name, which might indicate configuration errors.
func (v *ModelConfigValidator) detectMismatchedEndpoints(ctx context.Context) error {
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	
	rows, err := v.db.Query(timeout, `
		SELECT pm.id, p.display_name, pm.raw_model_name, 
		       pm.standardized_name, pm.outbound_model_name
		FROM provider_models pm
		JOIN providers p ON pm.provider_id = p.id
		WHERE pm.outbound_model_name IS NOT NULL
		  AND pm.standardized_name IS NOT NULL
		  AND pm.outbound_model_name != pm.standardized_name
		  AND pm.outbound_model_name != pm.raw_model_name
		  -- Only warn about active models
		  AND pm.lifecycle_status = 'active'
		LIMIT 10
	`)
	
	if err != nil {
		return err
	}
	defer rows.Close()
	
	var mismatchCount int
	for rows.Next() {
		var id int
		var providerName, rawName, stdName, outboundName string
		if err := rows.Scan(&id, &providerName, &rawName, &stdName, &outboundName); err != nil {
			continue
		}
		
		mismatchCount++
		slog.Warn("model_config_validator: potential endpoint mismatch detected",
			"provider_model_id", id,
			"provider", providerName,
			"raw_model_name", rawName,
			"standardized_name", stdName,
			"outbound_model_name", outboundName,
			"hint", "verify that outbound_model_name matches the actual upstream API endpoint")
	}
	
	if mismatchCount > 0 {
		slog.Info("model_config_validator: detected endpoint mismatches",
			"count", mismatchCount,
			"action", "review logs above for details")
	}
	
	return nil
}
