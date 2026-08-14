package streaming

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CO-2 (doc 19 轨道 COST+OBS): when real usage arrives after an estimated
// anomaly row was recorded (actual_tokens=NULL), backfill actual_tokens and
// correct usage_source so admin summaries (AVG(actual_tokens)) stop reading
// NULL for every estimated row.

func TestFormatAnomalyRecorder_BackfillActualTokens(t *testing.T) {
	db := &mockDB{}
	recorder := NewFormatAnomalyRecorder(db)

	err := recorder.BackfillActualTokens(context.Background(), "req-1", 137)
	require.NoError(t, err)
	assert.True(t, db.execCalled, "expected Exec to be called")
	assert.Contains(t, db.execQuery, "UPDATE response_format_anomalies")
	assert.Contains(t, db.execQuery, "actual_tokens IS NULL")
	require.Len(t, db.execArgs, 3)
	assert.Equal(t, "req-1", db.execArgs[0])
	assert.Equal(t, 137, db.execArgs[1])
	assert.Equal(t, UsageSourceLLM, db.execArgs[2], "usage_source must be corrected to llm")
}

func TestFormatAnomalyRecorder_BackfillActualTokensGuards(t *testing.T) {
	// nil / non-positive inputs are no-ops: backfilling 0 tokens would lie
	// about real usage.
	recorder := NewFormatAnomalyRecorder(&mockDB{})
	assert.NoError(t, recorder.BackfillActualTokens(context.Background(), "", 100))
	assert.NoError(t, recorder.BackfillActualTokens(context.Background(), "req-1", 0))
	assert.NoError(t, recorder.BackfillActualTokens(context.Background(), "req-1", -5))

	// nil recorder must not panic (mirrors RecordAnomaly nil-guard style).
	var nilRecorder *FormatAnomalyRecorder
	assert.NoError(t, nilRecorder.BackfillActualTokens(context.Background(), "req-1", 100))
}
