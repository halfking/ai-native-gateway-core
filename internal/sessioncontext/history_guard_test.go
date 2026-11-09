package sessioncontext

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

func TestHistoryReaderDecodesKeysNestedJSONAndSurrogates(t *testing.T) {
	args := `{"password":"nested-secret","pem":"-----BEGIN PRIVATE KEY-----\nDATA\n-----END PRIVATE KEY-----"}`
	body := `{"role":"assistant","metadata":{"\u0070assword":"metadata-secret"},"unicode":"\u4e2d\uD83D\uDE00","tool_calls":[{"function":{"arguments":` + string(mustHistoryJSON(t, args)) + `}}]}`
	reader := newHistoryText(context.Background(), func() io.Reader { return strings.NewReader(body) })
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"password":"metadata-secret"`, `"password":"nested-secret"`, "中😀", "-----BEGIN PRIVATE KEY-----\nDATA\n-----END PRIVATE KEY-----"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("full decoded representation lost %q: %q", want, got)
		}
	}
}

func mustHistoryJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHistoryReaderExpandsRepeatedStringification(t *testing.T) {
	text := "-----BEGIN PRIVATE KEY-----\nBODY\n-----END PRIVATE KEY-----"
	for i := 0; i < 5; i++ {
		text = string(mustHistoryJSON(t, text))
	}
	body := string(mustHistoryJSON(t, map[string]any{"role": "assistant", "content": text}))
	got, err := io.ReadAll(newHistoryText(context.Background(), func() io.Reader { return strings.NewReader(body) }))
	if err != nil || !strings.Contains(string(got), "-----BEGIN PRIVATE KEY-----\nBODY\n-----END PRIVATE KEY-----") {
		t.Fatalf("nested strings hid the PEM: %v %q", err, got)
	}
}

func TestHistoryReaderHundredMBUsesBoundedMemory(t *testing.T) {
	const size = 100000000
	prefix, suffix := `{"role":"assistant","content":"`, `"}`
	open := func() io.Reader {
		return io.MultiReader(strings.NewReader(prefix), io.LimitReader(repeatingText("a"), size-int64(len(prefix)+len(suffix))), strings.NewReader(suffix))
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	n, err := io.Copy(io.Discard, newHistoryText(context.Background(), open))
	runtime.ReadMemStats(&after)
	if err != nil || n != 2*size+1 {
		t.Fatalf("incomplete full-message scans: n=%d err=%v", n, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 256<<10 {
		t.Fatalf("100MB reader allocated %d bytes", allocated)
	}
}

func TestHistoryReaderCancelsAndDoesNotHideMalformedOuterEscapes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open := func() io.Reader {
		return &cancelExcerptReader{r: io.MultiReader(strings.NewReader(`{"content":"`), io.LimitReader(repeatingText("a"), 1<<20), strings.NewReader(`"}`)), cancel: cancel}
	}
	if _, err := io.Copy(io.Discard, newHistoryText(ctx, open)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	for _, body := range []string{`{"content":"\uD800"}`, `{"content":"\q"}`, `{"content":"unfinished`} {
		if _, err := io.Copy(io.Discard, newHistoryText(context.Background(), func() io.Reader { return strings.NewReader(body) })); err == nil {
			t.Fatalf("malformed outer escape was hidden: %s", body)
		}
	}
}

func TestReduceGuardsWholeSmallAndLargeHistoryBeforeExcerpting(t *testing.T) {
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldPEM := strings.Repeat("irrelevant\n", 10000) + "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("DATA\n", 300) + "billing LARGELEAK\n" + strings.Repeat("DATA\n", 300) + "-----END PRIVATE KEY-----\n" + strings.Repeat("irrelevant\n", 10000)
	src := mustHistoryJSON(t, map[string]any{"model": "m", "messages": []any{
		map[string]any{"role": "user", "content": "Maintain configuration"},
		map[string]any{"role": "assistant", "metadata": map[string]string{"password": "tiny-secret"}, "content": "billing SMALLLEAK"},
		map[string]any{"role": "assistant", "content": oldPEM},
		map[string]any{"role": "user", "content": "Find billing configuration"},
	}})
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "t", SessionID: "s"}, strings.NewReader(string(src)))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	re := regexp.MustCompile(`(?s)"password"\s*:\s*"[^\"]+"|-----BEGIN PRIVATE KEY-----.*?-----END PRIVATE KEY-----`)
	ctx := WithHistoryGuard(context.Background(), func(ctx context.Context, open func() io.Reader) (bool, error) {
		calls++
		return !re.MatchReader(bufio.NewReaderSize(open(), 32<<10)), nil
	})
	if !HasHistoryGuard(ctx) || HasHistoryGuard(context.Background()) || HasHistoryGuard(WithHistoryGuard(context.Background(), nil)) {
		t.Fatal("guard binding is inconsistent")
	}
	got, _, err := Reduce(ctx, store, ref, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Contains(string(got), "SMALLLEAK") || strings.Contains(string(got), "LARGELEAK") {
		t.Fatalf("unprotected messages were excerpted before whole-message checks: calls=%d body=%s", calls, got)
	}
	wantErr := errors.New("history policy unavailable")
	_, _, err = Reduce(WithHistoryGuard(context.Background(), func(context.Context, func() io.Reader) (bool, error) { return false, wantErr }), store, ref, 64<<10)
	if !errors.Is(err, wantErr) {
		t.Fatalf("history guard failure did not fail closed: %v", err)
	}
}
