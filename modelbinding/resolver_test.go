package modelbinding

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeDB struct {
	results []string
	args    [][]any
}

func (db *fakeDB) QueryRow(_ context.Context, _ string, args ...interface{}) pgx.Row {
	db.args = append(db.args, args)
	value := db.results[0]
	db.results = db.results[1:]
	return fakeRow{value: value}
}

type fakeRow struct{ value string }

func (r fakeRow) Scan(dest ...interface{}) error {
	if len(dest) != 1 {
		return errors.New("unexpected scan destination count")
	}
	ptr := dest[0].(*string)
	*ptr = r.value
	return nil
}

func TestResolveRawBinding(t *testing.T) {
	tests := []struct {
		name    string
		results []string
		request string
		want    string
		wantErr error
	}{
		{
			name:    "exact raw name wins over equivalent alias",
			results: []string{"deepseek-v4-flash-260425"},
			request: "deepseek-v4-flash-260425",
			want:    "deepseek-v4-flash-260425",
		},
		{
			name:    "unique alias resolves one raw binding",
			results: []string{"", "deepseek-v4-flash-260425"},
			request: "deepseek-flash",
			want:    "deepseek-v4-flash-260425",
		},
		{
			name:    "ambiguous alias fails without selecting a binding",
			results: []string{"", "deepseek-v4-flash\x1fdeepseek-v4-flash-260425"},
			request: "deepseek-flash",
			wantErr: ErrAmbiguousModelBinding,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDB{results: tt.results}
			got, err := ResolveRawBinding(context.Background(), db, 12, tt.request)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ResolveRawBinding() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ResolveRawBinding() = %q, want %q", got, tt.want)
			}
			wantCandidates := []string(nil)
			if errors.Is(err, ErrAmbiguousModelBinding) {
				wantCandidates = strings.Split(tt.results[len(tt.results)-1], "\x1f")
			}
			if errors.Is(err, ErrAmbiguousModelBinding) && !reflect.DeepEqual(AmbiguousCandidates(err), wantCandidates) {
				t.Fatalf("ambiguous candidates = %v, want %v", AmbiguousCandidates(err), wantCandidates)
			}
			if len(tt.results) > 1 {
				variants, ok := db.args[1][1].([]string)
				if !ok || !contains(variants, tt.request) {
					t.Fatalf("fallback variants = %v, want %q included", db.args[1][1], tt.request)
				}
			}
		})
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
