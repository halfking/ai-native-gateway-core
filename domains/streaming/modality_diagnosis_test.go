package streaming

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestVisionRequest_DetectedAsVision is the entry point of the whole
// diagnosis, and it is worth pinning on its own.
//
// ★ The chain that produced the production incident:
//
//	IDE agent's request body carries screenshots
//	  → detectRequestModality sees an image block → "vision"
//	  → router's modality gate admits only vision/multimodal rows
//	  → a text-only model has no candidate
//	  → (before the fix) 503 "No available provider for model 'X'"
//
// If the detection ever stops recognising image blocks, the diagnosis stops
// firing and the error silently reverts to the unhelpful 503 — with no test
// failing. The body below is the shape the IDE agent actually sends:
// OpenAI chat-completions content parts with an image_url block.
func TestVisionRequest_DetectedAsVision(t *testing.T) {
	body := []byte(`{
      "model": "glm-5.3-flash",
      "messages": [
        {"role": "system", "content": "you are a helpful assistant"},
        {"role": "user", "content": [
          {"type": "text", "text": "看下这张图"},
          {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAAAQABAAD"}}
        ]}
      ]
    }`)

	got := detectRequestModality(body)
	if got != modalityVision {
		t.Errorf("detectRequestModality = %q, want %q", got, modalityVision)
	}
}

// TestTextRequest_DetectedAsText is the negative control.
//
// ★ Without it, a detector that always answers "vision" would satisfy the
// test above. The two together pin the detector's discrimination, which is
// what the mismatch branch keys off.
func TestTextRequest_DetectedAsText(t *testing.T) {
	body := []byte(`{
      "model": "glm-5.3-flash",
      "messages": [{"role": "user", "content": "你好"}]
    }`)

	got := detectRequestModality(body)
	if got != modalityText {
		t.Errorf("detectRequestModality = %q, want %q", got, modalityText)
	}
}

// TestModalityMismatch_MatchesRouterGate cross-checks the diagnosis against
// the rule the router itself applies.
//
// ★ This is the check that keeps the error message honest. The diagnosis
// says "this model cannot serve this modality"; the router says "this model
// cannot serve this modality" using provider/candidate_query_sql_base's
// predicate. If the two ever disagree, the message would blame modality for
// a zero-candidate exit that modality had nothing to do with — which is
// worse than the old generic 503, because it misdirects the client.
//
// The comparison is table-driven over every (registered, requested) pair, so
// a new modality added to one side without the other fails here.
func TestModalityMismatch_MatchesRouterGate(t *testing.T) {
	registered := []string{"text", "multimodal", "vision", "audio", "video", "image"}
	requested := []string{"text", "vision", "audio", "video", "image", "multimodal"}

	for _, reg := range registered {
		for _, req := range requested {
			// What the router's predicate admits.
			routerAdmits := routerAcceptsModality(reg, req)
			// What the diagnosis concludes.
			diagnosisSaysMismatch := !provider.ModalityAccepts(reg, req)

			if diagnosisSaysMismatch == routerAdmits {
				t.Errorf("registered=%q requested=%q: router admits=%v but diagnosis says mismatch=%v — "+
					"the error message would name a cause the router never had",
					reg, req, routerAdmits, diagnosisSaysMismatch)
			}
		}
	}
}

// routerAcceptsModality mirrors the candidate query's modality predicate.
//
// Kept as a literal transcription of provider/client.go's
// candidateQuerySQLBase filter, because the point of this test is to compare
// two INDEPENDENT expressions of the same rule. Reusing one implementation
// would make the comparison vacuous.
func routerAcceptsModality(registered, requested string) bool {
	switch requested {
	case "":
		return true
	case "text":
		return true
	case "multimodal":
		return registered == "multimodal"
	case "image":
		return registered == "image" || registered == "vision" || registered == "multimodal"
	default: // vision / audio / video
		return registered == requested || registered == "multimodal"
	}
}

// TestModalityMismatch_OnlyForMediaRequests pins the guard that keeps the
// new branch away from text requests.
//
// A text request must never be reported as a modality mismatch: for text,
// every model in the catalog qualifies, and a mismatch diagnosis would send
// the user chasing a nonexistent problem.
func TestModalityMismatch_OnlyForMediaRequests(t *testing.T) {
	for _, mod := range []string{"", "text"} {
		if provider.ModalityAccepts("text", mod) != true {
			t.Errorf("a %q request must be acceptable to a text model", mod)
		}
	}
	for _, mod := range []string{"vision", "audio", "video"} {
		if provider.ModalityAccepts("text", mod) != false {
			t.Errorf("a %q request must NOT be acceptable to a text-only model", mod)
		}
	}
}

// TestModalityMismatchResolver_Contract pins what the resolver promises its
// caller.
//
// The handler branches on the returned value, so the contract matters:
// nil means "not a modality problem" and the handler must keep reporting the
// retryable 503 in that case.
func TestModalityMismatchResolver_Contract(t *testing.T) {
	r := &modalityMismatchResolver{known: true, mismatch: &provider.ModalityMismatch{
		RequestedModality:  "vision",
		RegisteredModality: "text",
	}}
	got, err := r.ModelModalityMismatch(context.Background(), "glm-5.3-flash", "", "default", "vision")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IsZero() {
		t.Error("resolver must report the mismatch it was given")
	}
	if got.RequestedModality != "vision" || got.RegisteredModality != "text" {
		t.Errorf("mismatch = %+v, want vision/text", got)
	}
}
