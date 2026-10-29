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
			name:    "ambiguous alias selects first candidate (2026-09-06 P0.3 fix)",
			results: []string{"", "deepseek-v4-flash\x1fdeepseek-v4-flash-260425"},
			request: "deepseek-flash",
			want:    "deepseek-v4-flash",
			wantErr: nil,
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

// querySpyDB 捕获收到的 SQL 文本（fakeDB 只收参数不收文本 ⇒ 排序子句被
// 静默改掉时无人知晓 ⇒ 本轮 211 生产告警的教训需要一道契约门）。
type querySpyDB struct {
	fakeDB
	queries []string
}

func (db *querySpyDB) QueryRow(ctx context.Context, query string, args ...interface{}) pgx.Row {
	db.queries = append(db.queries, query)
	return db.fakeDB.QueryRow(ctx, query, args...)
}

// TestResolveRawBinding_NonExactQueryRanksBySpecificity：
// 2026-10-30 24h 审计轮（211 生产实锤）：归一化回退查询原按
// pm.raw_model_name 字典序取第一个候选 ⇒ requested=glm-5.3-flash 时
// `z-ai/glm-5.3`（经宽泛变体 glm-5.3 命中）排在 `z-ai/glm-5.3-flash`
// （经最特异变体 glm-5.3-flash 命中）前面 ⇒ **把非 flash 模型发给要
// flash 的用户**。修法 = 候选按「命中的最特异变体下标」（WITH ORDINALITY
// 的 min(i)）排序。本契约钉住三件事：排序因子存在、DISTINCT ON 防重、
// 兜底仍按名字稳定排序。
func TestResolveRawBinding_NonExactQueryRanksBySpecificity(t *testing.T) {
	db := &querySpyDB{fakeDB: fakeDB{results: []string{"", "z-ai/glm-5.3-flash"}}}
	if _, err := ResolveRawBinding(context.Background(), db, 19, "glm-5.3-flash"); err != nil {
		t.Fatalf("ResolveRawBinding() error = %v", err)
	}
	if len(db.queries) != 2 {
		t.Fatalf("expected 2 queries (exact + normalized fallback), got %d", len(db.queries))
	}
	nonExact := db.queries[1]
	for _, needle := range []string{
		"WITH ORDINALITY",                    // 变体下标必须可得（特异性排序的依据）
		"best_rank",                          // 排序因子
		"ORDER BY ranked.best_rank NULLS LAST, ranked.name", // 特异性优先 + 名字稳定兜底
		"DISTINCT ON (pm.raw_model_name)",    // 每个候选名只留最特异的那一行
	} {
		if !strings.Contains(nonExact, needle) {
			t.Fatalf("non-exact query missing %q — 候选特异性排序被改动，211 生产「要 flash 给非 flash」的回归会复发\nSQL: %s", needle, nonExact)
		}
	}
	// 精确路径不得引入特异性排序（同名多绑定时仍按名字序取第一个，行为不变）
	if strings.Contains(db.queries[0], "best_rank") {
		t.Fatalf("exact query should stay unranked, got: %s", db.queries[0])
	}
}
