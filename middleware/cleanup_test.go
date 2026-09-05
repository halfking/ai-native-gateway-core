package middleware

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCleanupStackRunsLIFOAndIsIdempotent(t *testing.T) {
	var got []int
	stack := NewCleanupStack()
	stack.Add(func() { got = append(got, 1) })
	stack.Add(func() { got = append(got, 2) })
	stack.RunAll()
	stack.RunAll()
	if !reflect.DeepEqual(got, []int{2, 1}) {
		t.Fatalf("cleanup order = %v", got)
	}
}

func TestCleanupStackContinuesAfterPanic(t *testing.T) {
	var called bool
	stack := NewCleanupStack()
	stack.Add(func() { called = true })
	stack.Add(func() { panic("cleanup failure") })
	stack.RunAll()
	if !called {
		t.Fatal("cleanup after panic was not called")
	}
}

func TestRecoveryMiddlewareInstallsStackAndRecovers(t *testing.T) {
	var cleaned bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RegisterCleanup(r.Context(), func() { cleaned = true })
		panic("boom")
	})
	r := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	NewRecoveryMiddleware().Wrap(next).ServeHTTP(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if !cleaned {
		t.Fatal("registered cleanup did not run")
	}
}
