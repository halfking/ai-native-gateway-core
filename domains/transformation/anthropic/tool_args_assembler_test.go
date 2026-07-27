package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateStreamingToolArgs guards the 2026-07-27 fix (F-5): the legacy
// stream converter concatenated input_json_delta fragments and emitted them at
// content_block_stop without validating the result. A truncated stream
// forwarded invalid JSON to the OpenAI client — silently.
//
// Contract:
//   - valid JSON passes through unchanged;
//   - a truncation that leaves an OPEN CONTAINER (complete key:value pairs
//     up to the cut) is repaired by closing the containers;
//   - a truncation that leaves a DANGLING KEY / incomplete value (which
//     bracket-balancing cannot repair) is forwarded unchanged with a non-nil
//     error so the anomaly is observable;
//   - the function never returns a non-empty invalid string as "repaired".
func TestValidateStreamingToolArgs(t *testing.T) {
	t.Run("valid json passes through unchanged", func(t *testing.T) {
		in := `{"city":"Tokyo","temp":21}`
		out, repaired, err := validateStreamingToolArgs(in)
		assert.NoError(t, err)
		assert.False(t, repaired)
		assert.Equal(t, in, out)
	})

	t.Run("truncated object with complete pairs is repaired", func(t *testing.T) {
		// {"city":"Tokyo","temp":21   — open container, last value complete
		in := `{"city":"Tokyo","temp":21`
		out, repaired, err := validateStreamingToolArgs(in)
		assert.NoError(t, err, "repair must succeed")
		assert.True(t, repaired)
		require.True(t, json.Valid([]byte(out)), "repaired output must be valid JSON: %s", out)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &m))
		assert.Equal(t, "Tokyo", m["city"])
	})

	t.Run("unterminated string then balanced is repaired", func(t *testing.T) {
		// {"q":"hello world   — string never closed, object never closed,
		// but the key:value is structurally complete up to the cut.
		in := `{"q":"hello world`
		out, repaired, err := validateStreamingToolArgs(in)
		assert.NoError(t, err)
		assert.True(t, repaired)
		require.True(t, json.Valid([]byte(out)), "%s", out)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &m))
		assert.Equal(t, "hello world", m["q"])
	})

	t.Run("escaped quote is not mistaken for string close", func(t *testing.T) {
		in := `{"q":"a\"b`
		out, repaired, err := validateStreamingToolArgs(in)
		assert.NoError(t, err)
		assert.True(t, repaired)
		require.True(t, json.Valid([]byte(out)), "%s", out)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &m))
		assert.Equal(t, "a\"b", m["q"])
	})

	t.Run("nested open containers balanced", func(t *testing.T) {
		// {"items":[{"id":1   — two open containers, complete value up to cut
		in := `{"items":[{"id":1`
		out, repaired, err := validateStreamingToolArgs(in)
		assert.NoError(t, err)
		assert.True(t, repaired)
		require.True(t, json.Valid([]byte(out)), "%s", out)
	})

	t.Run("empty input is a no-op", func(t *testing.T) {
		out, repaired, err := validateStreamingToolArgs("")
		assert.NoError(t, err)
		assert.False(t, repaired)
		assert.Equal(t, "", out)
	})

	t.Run("dangling key cannot be repaired → original + error (observable)", func(t *testing.T) {
		// {"city":"Tokyo","temp"   — key string closed but no colon/value.
		// Bracket-balancing cannot repair this; must forward original + err.
		in := `{"city":"Tokyo","temp"`
		out, repaired, err := validateStreamingToolArgs(in)
		assert.Error(t, err, "must surface an error for unrepairable input (the anomaly)")
		assert.False(t, repaired)
		assert.Equal(t, in, out, "no-worse-than-before: return original when repair fails")
		assert.Contains(t, err.Error(), "not valid JSON")
	})

	t.Run("garbage cannot be repaired → original + error", func(t *testing.T) {
		in := `not even close to json {{{{`
		out, repaired, err := validateStreamingToolArgs(in)
		assert.Error(t, err)
		assert.False(t, repaired)
		assert.Equal(t, in, out)
	})
}

// TestBalanceJSONContainers covers the bracket balancer on edge cases.
func TestBalanceJSONContainers(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantValid bool
	}{
		{"already balanced object", `{"a":1}`, true},
		{"already balanced array", `[1,2,3]`, true},
		{"unclosed nested object", `{"a":{"b":1`, true},
		{"string containing brace", `{"a":"}"`, true},
		{"empty object already closed", `{}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := balanceJSONContainers(c.in)
			if c.wantValid && !json.Valid([]byte(got)) {
				t.Errorf("balanceJSONContainers(%q) = %q, want valid JSON", c.in, got)
			}
		})
	}
}

// TestValidateStreamingToolArgs_RealisticFragment simulates the production
// failure mode: input_json_delta fragments concatenated by the converter.
func TestValidateStreamingToolArgs_RealisticFragment(t *testing.T) {
	fragments := []string{
		`{"locati`,
		`on":"San `,
		`Francisc`,
		`o","days"`,
		`:3}`,
	}
	concatenated := strings.Join(fragments, "")
	out, repaired, err := validateStreamingToolArgs(concatenated)
	assert.NoError(t, err)
	assert.False(t, repaired, "the complete concatenation should already be valid")
	require.True(t, json.Valid([]byte(out)))
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	assert.Equal(t, "San Francisco", m["location"])

	// Truncation after a complete value but before the closing brace —
	// container-balancing repairs this (adds the missing "}").
	truncated := `{"location":"San Francisco","days":3`
	out2, repaired2, err2 := validateStreamingToolArgs(truncated)
	assert.NoError(t, err2)
	assert.True(t, repaired2)
	require.True(t, json.Valid([]byte(out2)), "%s", out2)
	var m2 map[string]any
	require.NoError(t, json.Unmarshal([]byte(out2), &m2))
	assert.Equal(t, "San Francisco", m2["location"])
}
