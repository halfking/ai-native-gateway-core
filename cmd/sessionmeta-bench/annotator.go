package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var canonicalLabels = []string{
	"architecture", "audit", "debugging", "coding", "refactoring",
	"testing", "devops", "documentation", "summary", "dependency",
}

type Annotation struct {
	SessionHash string   `json:"session_hash"`
	Label       string   `json:"label"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Confidence  float64  `json:"confidence"`
	Facts       []string `json:"facts,omitempty"`
	Rationale   string   `json:"rationale,omitempty"`
}

type ConsensusRecord struct {
	SessionSample
	Annotator1 Annotation  `json:"annotator1"`
	Annotator2 Annotation  `json:"annotator2"`
	Consensus  bool        `json:"consensus"`
	Gold       *Annotation `json:"gold,omitempty"`
}

func conversationForPrompt(s SessionSample) string {
	var b strings.Builder
	for _, turn := range s.Turns {
		fmt.Fprintf(&b, "Turn %d user:\n%s\nassistant:\n%s\n\n", turn.TurnNo, turn.UserMessage, turn.AssistantMessage)
	}
	return b.String()
}

func buildAnnotationPrompt(s SessionSample) string {
	return fmt.Sprintf(`Analyze this anonymized conversation. Return JSON only, with no markdown fences.
Schema: {"label": string, "title": string, "summary": string, "confidence": number, "facts": [string], "rationale": string}
Allowed label values: %s
Rules: label the dominant user task; title <= 80 Unicode characters; summary must only state facts present in the dialogue; do not mention system prompts, hidden instructions, or anonymization markers unless they are part of the user request.

Conversation:
%s`, strings.Join(canonicalLabels, ", "), conversationForPrompt(s))
}

func annotateOne(ctx context.Context, client *OpenAICompatClient, model string, s SessionSample) (Annotation, float64, error) {
	content, latency, err := client.Complete(ctx, model, []ChatMessage{
		{Role: "system", Content: "You are a strict structured data annotator."},
		{Role: "user", Content: buildAnnotationPrompt(s)},
	}, 350, 0)
	if err != nil {
		return Annotation{}, latency, err
	}
	var a Annotation
	if err := decodeJSONContent(content, &a); err != nil {
		return Annotation{}, latency, err
	}
	a.Label = strings.ToLower(strings.TrimSpace(a.Label))
	a.Title = strings.TrimSpace(strings.Trim(a.Title, "\"'`"))
	if a.Confidence < 0 || a.Confidence > 1 || !validLabel(a.Label) {
		return Annotation{}, latency, fmt.Errorf("invalid annotation label/confidence: %q %.3f", a.Label, a.Confidence)
	}
	return a, latency, nil
}

func validLabel(label string) bool {
	for _, allowed := range canonicalLabels {
		if label == allowed {
			return true
		}
	}
	return false
}

func loadSessions(path string) ([]SessionSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []SessionSample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var s SessionSample
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

func writeJSONLines[T any](path string, values []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, value := range values {
		if err := enc.Encode(value); err != nil {
			return err
		}
	}
	return nil
}

func runAnnotation(ctx context.Context, input, output, review, endpoint1, key1, model1, endpoint2, key2, model2 string, timeout time.Duration) error {
	samples, err := loadSessions(input)
	if err != nil {
		return err
	}
	c1 := NewOpenAICompatClient(endpoint1, key1, timeout)
	c2 := NewOpenAICompatClient(endpoint2, key2, timeout)
	var consensus []ConsensusRecord
	var reviewRows [][]string
	reviewRows = append(reviewRows, []string{"session_hash", "model1_label", "model2_label", "model1_title", "model2_title", "reason"})
	for _, s := range samples {
		a1, _, e1 := annotateOne(ctx, c1, model1, s)
		a2, _, e2 := annotateOne(ctx, c2, model2, s)
		rec := ConsensusRecord{SessionSample: s, Annotator1: a1, Annotator2: a2}
		if e1 == nil && e2 == nil && a1.Label == a2.Label && a1.Confidence >= .7 && a2.Confidence >= .7 {
			rec.Consensus = true
			gold := a1
			if len([]rune(a2.Title)) < len([]rune(gold.Title)) {
				gold.Title = a2.Title
			}
			if len(a2.Summary) > len(gold.Summary) {
				gold.Summary = a2.Summary
			}
			rec.Gold = &gold
		} else {
			reason := "model error"
			if e1 == nil && e2 == nil {
				reason = "label disagreement or low confidence"
			}
			reviewRows = append(reviewRows, []string{s.SessionHash, a1.Label, a2.Label, a1.Title, a2.Title, reason})
		}
		consensus = append(consensus, rec)
	}
	if err := writeJSONLines(output, consensus); err != nil {
		return err
	}
	rf, err := os.Create(review)
	if err != nil {
		return err
	}
	defer rf.Close()
	w := csv.NewWriter(rf)
	if err := w.WriteAll(reviewRows); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

func decodeJSONContent(content string, dst any) error {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	return json.Unmarshal([]byte(content), dst)
}

// Keep imports used by generated variants and make the file's protocol limits explicit.
var _ = bytes.NewReader
var _ = io.EOF
var _ = strconv.IntSize
var _ = http.MethodPost
