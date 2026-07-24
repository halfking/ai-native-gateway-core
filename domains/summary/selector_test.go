package summary

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeCatalog struct {
	candidates []Candidate
	err        error
}

func (f *fakeCatalog) Candidates(_ context.Context, _ string, _ SummaryKind) ([]Candidate, error) {
	return f.candidates, f.err
}

func TestSelectSummaryModel_PrefersCheapAndAvailable(t *testing.T) {
	cat := &fakeCatalog{
		candidates: []Candidate{
			{Model: "m1", Cost: 0.001, Latency: 100 * time.Millisecond, Available: 1.0, Ctx: 8192},
			{Model: "m2", Cost: 0.01, Latency: 200 * time.Millisecond, Available: 0.95, Ctx: 32768},
			{Model: "m3", Cost: 0.005, Latency: 50 * time.Millisecond, Available: 0.99, Ctx: 4096},
		},
	}
	sel := NewSelector(cat, DefaultWeights())
	got, err := sel.Select(context.Background(), "tenant1", SummaryKindTitle)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "m1" {
		t.Fatalf("expected m1 (cheapest+available), got %s", got.Model)
	}
}

func TestSelectSummaryModel_FallbackOnEmptyCatalog(t *testing.T) {
	cat := &fakeCatalog{candidates: nil}
	sel := NewSelector(cat, DefaultWeights())
	_, err := sel.Select(context.Background(), "tenant1", SummaryKindTitle)
	if err == nil {
		t.Fatal("expected error on empty catalog")
	}
}

func TestSelectSummaryModel_PropagatesCatalogError(t *testing.T) {
	wantErr := errors.New("db down")
	cat := &fakeCatalog{err: wantErr}
	sel := NewSelector(cat, DefaultWeights())
	_, err := sel.Select(context.Background(), "tenant1", SummaryKindTitle)
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}

func TestSelectSummaryModel_SingleCandidateIsReturned(t *testing.T) {
	cat := &fakeCatalog{candidates: []Candidate{{Model: "only", Available: 0.5}}}
	sel := NewSelector(cat, DefaultWeights())
	got, err := sel.Select(context.Background(), "tenant1", SummaryKindTitle)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "only" {
		t.Fatalf("expected 'only', got %s", got.Model)
	}
}
