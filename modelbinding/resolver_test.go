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
			results: []string{"", "deepseek-v4-flash-260425\x1f2"},
			request: "deepseek-flash",
			want:    "deepseek-v4-flash-260425",
		},
		{
			// 2026-10-31 批判式审计轮（第 11 轮立项卡实施）：特异性胜出 ⇒ 静默
			// 选择（不再 WARN），选择结果与 2026-09-06 P0.3 行为一致。
			name:    "specificity winner resolves silently (rank 1 beats rank 3)",
			results: []string{"", "z-ai/glm-5.3-flash\x1f1\x1ez-ai/glm-5.3\x1f3"},
			request: "glm-5.3-flash",
			want:    "z-ai/glm-5.3-flash",
			wantErr: nil,
		},
		{
			// 真同分（两绑定经同一变体命中）⇒ 维持 WARN+取首（2026-09-06 行为）。
			name:    "rank tie keeps first-candidate fallback",
			results: []string{"", "m-a\x1f2\x1em-b\x1f2"},
			request: "some-model",
			want:    "m-a",
			wantErr: nil,
		},
		{
			// rank 段缺失（异常 wire）⇒ 按「不比任何人特异」处理 ⇒ 交 WARN 路径。
			name:    "missing rank degrades to ambiguity path",
			results: []string{"", "m-a\x1em-b"},
			request: "some-model",
			want:    "m-a",
			wantErr: nil,
		},
		{
			// 旧测试名保留的歧义场景在新 wire 下即「exact 同名多绑定」⇒ 走
			// exact 路径的 WARN+取首（行为与 2026-09-06 P0.3 一致）。
			name:    "ambiguous alias selects first candidate (2026-09-06 P0.3 fix)",
			results: []string{"", "deepseek-v4-flash\x1f2\x1edeepseek-v4-flash-260425\x1f2"},
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
				// 新 wire：name\x1frank 对、\x1e 分隔 ⇒ 期望候选取名字段。
				for _, pair := range strings.Split(tt.results[len(tt.results)-1], "\x1e") {
					name, _, _ := strings.Cut(pair, "\x1f")
					wantCandidates = append(wantCandidates, name)
				}
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
		// 2026-10-31：wire 必须带 rank 对（name\x1frank、\x1e 分隔）——Go 侧
		// 要区分「特异性胜出（静默）」与「真同分（WARN）」，纯名字序列给不出判据。
		`ranked.name || E'\x1f' || ranked.best_rank`,
		`E'\x1e'`,
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
