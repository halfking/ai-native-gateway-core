package telemetry

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestQualityFlagsArgEncodesEmptyAndNonEmptyArrays(t *testing.T) {
	tests := []struct {
		name     string
		flags    []string
		wantDims []pgtype.ArrayDimension
		wantVals []string
	}{
		{
			name:     "nil slice becomes empty array",
			flags:    nil,
			wantDims: []pgtype.ArrayDimension{{Length: 0, LowerBound: 1}},
			wantVals: nil,
		},
		{
			name:     "empty slice becomes empty array",
			flags:    []string{},
			wantDims: []pgtype.ArrayDimension{{Length: 0, LowerBound: 1}},
			wantVals: []string{},
		},
		{
			name:     "values preserve order",
			flags:    []string{"missing_tool_name", "invalid_json"},
			wantDims: []pgtype.ArrayDimension{{Length: 2, LowerBound: 1}},
			wantVals: []string{"missing_tool_name", "invalid_json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arg := qualityFlagsArg(tt.flags)
			if len(tt.wantVals) == 0 {
				require.Equal(t, "{}", arg)
			} else {
				require.Equal(t, pgtype.FlatArray[string](tt.wantVals), arg)
			}

			encoded, err := pgtype.NewMap().Encode(pgtype.TextArrayOID, pgtype.TextFormatCode, arg, nil)
			require.NoError(t, err)
			if len(tt.wantVals) == 0 {
				require.Equal(t, "{}", string(encoded))
			} else {
				require.Equal(t, "{missing_tool_name,invalid_json}", string(encoded))
			}
		})
	}
}
