package admin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/memora"
)

type fakeMemoraSearch struct {
	byUser map[string][]memora.Memory
	err    error
}

func (f *fakeMemoraSearch) Disabled() bool { return false }

func (f *fakeMemoraSearch) SearchAdmin(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byUser[userID], nil
}

func TestFormatReadableBlockJSON(t *testing.T) {
	kind, display := formatReadableBlock(`{"name":"test","count":3}`)
	if kind != "json" {
		t.Fatalf("kind=%q want json", kind)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(display), &parsed); err != nil {
		t.Fatalf("display is not valid json: %v", err)
	}
	if parsed["name"] != "test" {
		t.Fatalf("parsed name=%v", parsed["name"])
	}
}

func TestFormatReadableBlockText(t *testing.T) {
	kind, display := formatReadableBlock("部署 llm-gateway-go 成功，healthz 200")
	if kind != "text" {
		t.Fatalf("kind=%q want text", kind)
	}
	if display == "" {
		t.Fatal("display empty")
	}
}

func TestFormatReadableBlockEmpty(t *testing.T) {
	kind, display := formatReadableBlock("   ")
	if kind != "text" || display != "" {
		t.Fatalf("got kind=%q display=%q", kind, display)
	}
}

func TestSearchMergedFactsDualNamespace(t *testing.T) {
	client := &fakeMemoraSearch{
		byUser: map[string][]memora.Memory{
			"k:42:default": {
				{ID: "1", Text: "task fact about deployment"},
			},
			"k:42:gw-session:ses_abc": {
				{ID: "2", Text: "[会话总结] user asked about routing"},
			},
		},
	}
	blocks, err := searchMergedFacts(context.Background(), client, "", 42, "default", "ses_abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("len=%d want 2", len(blocks))
	}
	sources := map[string]bool{}
	for _, b := range blocks {
		sources[b.Source] = true
	}
	if !sources["task"] || !sources["gw-session"] {
		t.Fatalf("sources=%v", sources)
	}
}

func TestSearchMergedFactsDedupe(t *testing.T) {
	dup := "same fact repeated"
	client := &fakeMemoraSearch{
		byUser: map[string][]memora.Memory{
			"k:7:task-a": {{ID: "1", Text: dup}},
			"k:7:gw-session:ses-x": {{ID: "2", Text: dup}},
		},
	}
	blocks, err := searchMergedFacts(context.Background(), client, "", 7, "task-a", "ses-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("len=%d want 1 after dedupe", len(blocks))
	}
}

func TestSearchMergedFactsEmpty(t *testing.T) {
	client := &fakeMemoraSearch{byUser: map[string][]memora.Memory{}}
	blocks, err := searchMergedFacts(context.Background(), client, "", 1, "task", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("len=%d want 0", len(blocks))
	}
}

func TestTruncateMemoraPreview(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "字"
	}
	got := truncateMemoraPreview(long)
	runes := []rune(got)
	if len(runes) > memoraPreviewMaxLen+1 {
		t.Fatalf("preview too long: %d runes", len(runes))
	}
	if string(runes[len(runes)-1]) != "…" {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
}

func TestBatchMemoraPreviewsEmptyStatus(t *testing.T) {
	client := &fakeMemoraSearch{byUser: map[string][]memora.Memory{}}
	results := batchMemoraPreviews(context.Background(), client, []sessionPreviewInput{
		{Index: 0, TaskID: "t1", APIKeyID: 1},
	})
	if len(results) != 1 || results[0].Status != "empty" {
		t.Fatalf("results=%+v", results)
	}
}

func TestFirstReadablePreview(t *testing.T) {
	p := firstReadablePreview([]readableBlock{
		{Text: ""},
		{Text: "hello world"},
	})
	if p != "hello world" {
		t.Fatalf("got %q", p)
	}
}

func TestReadableBlocksToMapsEmpty(t *testing.T) {
	maps := readableBlocksToMaps(nil)
	if len(maps) != 0 {
		t.Fatalf("want empty slice, got %d", len(maps))
	}
}
