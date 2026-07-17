package autoroute

import (
	"context"
	"testing"
)

type embeddingTestClient struct {
	vector []float32
}

func (c *embeddingTestClient) Embed(_ context.Context, _ string) ([]float32, error) {
	return c.vector, nil
}

func TestEmbeddingClassifier_Classify_NoText(t *testing.T) {
	classifier := NewEmbeddingClassifier(nil, &embeddingTestClient{vector: []float32{1}}, "", 1)
	classification, err := classifier.Classify(context.Background(), ClassificationSignals{})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if classification.Primary != TaskChat {
		t.Fatalf("task = %q, want %q", classification.Primary, TaskChat)
	}
	if classification.Confidence != 0 {
		t.Fatalf("confidence = %v, want 0", classification.Confidence)
	}
	if classification.Classifier != "embedding" {
		t.Fatalf("classifier = %q, want embedding", classification.Classifier)
	}
}

func TestEmbeddingClassifier_VectorHelpers(t *testing.T) {
	vector := []float32{0.1, -0.25, 1}
	parsed, err := parseVector(vectorLiteral(vector), len(vector))
	if err != nil {
		t.Fatalf("parseVector: %v", err)
	}
	for i := range vector {
		if parsed[i] != vector[i] {
			t.Fatalf("parsed[%d] = %v, want %v", i, parsed[i], vector[i])
		}
	}
}
