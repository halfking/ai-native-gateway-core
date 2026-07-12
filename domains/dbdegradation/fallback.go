package dbdegradation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// DegradationController coordinates manual and automatic persistence fallback.
type DegradationController struct {
	manual atomic.Bool
	active atomic.Bool
	enter  func(context.Context) error
	exit   func(context.Context) error
}

func NewDegradationController(enter, exit func(context.Context) error) *DegradationController {
	return &DegradationController{enter: enter, exit: exit}
}

func (c *DegradationController) ForceEnter(ctx context.Context) error {
	if c == nil || c.enter == nil {
		return fmt.Errorf("degradation controller not configured")
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	c.manual.Store(true)
	c.active.Store(true)
	return nil
}

func (c *DegradationController) ForceExit(ctx context.Context) error {
	if c == nil || c.exit == nil {
		return fmt.Errorf("degradation controller not configured")
	}
	if err := c.exit(ctx); err != nil {
		return err
	}
	c.manual.Store(false)
	c.active.Store(false)
	return nil
}

func (c *DegradationController) SetAutomaticActive(active bool) {
	if c != nil {
		c.active.Store(active)
	}
}

func (c *DegradationController) IsActive() bool { return c != nil && c.active.Load() }
func (c *DegradationController) IsManual() bool { return c != nil && c.manual.Load() }

// WriteGeneric writes a non-session record using the existing JSONL spool.
func (fw *FileWriter) WriteGeneric(ctx context.Context, recordType, key string, payload any) error {
	if fw == nil {
		return fmt.Errorf("file writer not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal fallback payload: %w", err)
	}
	return fw.writeRecord(BackupRecord{
		Type:      recordType,
		Timestamp: time.Now().UTC(),
		RecordKey: key,
		Payload:   raw,
	})
}

func (fw *FileWriter) WriteRequestLog(ctx context.Context, key string, payload any) error {
	return fw.WriteGeneric(ctx, "request_log", key, payload)
}

func (fw *FileWriter) WriteRequestWAL(ctx context.Context, key string, payload any) error {
	return fw.WriteGeneric(ctx, "request_wal", key, payload)
}

// BackupWriter is the small interface used by request persistence paths.
type BackupWriter interface {
	WriteRequestLog(context.Context, string, any) error
	WriteRequestWAL(context.Context, string, any) error
}
