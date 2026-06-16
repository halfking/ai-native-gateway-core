package admin

import (
	"testing"
)

func TestBucketIndex_AllCases(t *testing.T) {
	tests := []struct {
		predictor string
		bucket    string
		want      int
	}{
		{"prompt_length", "0-500", 0},
		{"prompt_length", "500-2000", 1},
		{"prompt_length", "2000-8000", 2},
		{"prompt_length", "8000+", 3},
		{"tools", "0", 0},
		{"tools", "1", 1},
		{"tools", "2+", 2},
		{"images", "no_image", 0},
		{"images", "has_image", 1},
		{"code_block", "no_code", 0},
		{"code_block", "has_code", 1},
		{"language", "ar", 0},
		{"language", "en", 1},
		{"language", "ja", 2},
		{"language", "ko", 3},
		{"language", "ru", 4},
		{"language", "zh", 5},
		{"language", "other", 6},
	}
	for _, tc := range tests {
		got := bucketIndex(tc.predictor, tc.bucket)
		if got != tc.want {
			t.Errorf("bucketIndex(%q, %q) = %d, want %d", tc.predictor, tc.bucket, got, tc.want)
		}
	}
}

func TestTotalSamples(t *testing.T) {
	rows := []QualityCorrelationRow{
		{Samples: 10},
		{Samples: 20},
		{Samples: 30},
	}
	if n := totalSamples(rows); n != 60 {
		t.Errorf("totalSamples = %d, want 60", n)
	}
	if n := totalSamples(nil); n != 0 {
		t.Errorf("totalSamples(nil) = %d, want 0", n)
	}
}

func TestWeightedPearson_EdgeCases(t *testing.T) {
	if r := weightedPearson(nil, nil, nil); r != 0 {
		t.Errorf("expected 0 for nil input, got %f", r)
	}
	if r := weightedPearson([]float64{1}, []float64{2}, []float64{1}); r != 0 {
		t.Errorf("expected 0 for single element, got %f", r)
	}
	if r := weightedPearson([]float64{1, 2}, []float64{3, 3}, []float64{1, 1}); r != 0 {
		t.Errorf("expected 0 for constant y, got %f", r)
	}
}

func TestSumFloat(t *testing.T) {
	if n := sumFloat([]float64{1.5, 2.5, 3.0}); n != 7.0 {
		t.Errorf("sumFloat = %f, want 7.0", n)
	}
	if n := sumFloat(nil); n != 0.0 {
		t.Errorf("sumFloat(nil) = %f, want 0.0", n)
	}
}

func TestInterpretCorrelation(t *testing.T) {
	tests := []struct {
		predictor string
		r         float64
		want      string
	}{
		{"prompt_length", 0.85, "positively"},
		{"tools", -0.5, "negatively"},
		{"images", 0.05, "very weakly"},
	}
	for _, tc := range tests {
		got := interpretCorrelation(tc.predictor, tc.r)
		if got == "" {
			t.Errorf("interpretCorrelation(%q, %f) returned empty", tc.predictor, tc.r)
		}
	}
}

func TestBuildBreakdownQuery_ValidInputs(t *testing.T) {
	valid := []string{"prompt_length", "tools", "images", "code_block", "language"}
	for _, by := range valid {
		q, err := buildBreakdownQuery(by)
		if err != nil {
			t.Errorf("buildBreakdownQuery(%q): unexpected error: %v", by, err)
		}
		if q == "" {
			t.Errorf("buildBreakdownQuery(%q): empty query", by)
		}
	}
}

func TestBuildBreakdownQuery_InvalidInput(t *testing.T) {
	q, err := buildBreakdownQuery("invalid")
	if q != "" || err != nil {
		t.Errorf("expected (\"\", nil) for invalid input, got (q=%q, err=%v)", q, err)
	}
}
