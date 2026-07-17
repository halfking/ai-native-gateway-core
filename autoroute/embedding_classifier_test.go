package autoroute

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
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

func TestEmbeddingClassifier_Classify_NearestCentroid(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT task_type, 1 - \\(centroid <=> \\$1::vector\\)").
		WithArgs("[1,0]", "test-model").
		WillReturnRows(pgxmock.NewRows([]string{"task_type", "similarity"}).AddRow("reasoning", 0.91))

	classifier := NewEmbeddingClassifier(mock, &embeddingTestClient{vector: []float32{1, 0}}, "test-model", 2)
	classification, err := classifier.Classify(context.Background(), ClassificationSignals{LastUserPrompt: "prove this"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if classification.Primary != TaskReasoning || classification.Confidence != 0.91 {
		t.Fatalf("classification = %+v", classification)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestEmbeddingClassifier_Classify_EmptyTable(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT task_type, 1 - \\(centroid <=> \\$1::vector\\)").
		WithArgs("[1,0]", "test-model").
		WillReturnError(pgx.ErrNoRows)

	classifier := NewEmbeddingClassifier(mock, &embeddingTestClient{vector: []float32{1, 0}}, "test-model", 2)
	classification, err := classifier.Classify(context.Background(), ClassificationSignals{LastUserPrompt: "hello"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if classification.Primary != TaskChat || classification.Confidence != 0 {
		t.Fatalf("classification = %+v", classification)
	}
}

func TestEmbeddingClassifier_UpdateCentroidEMA_IncrementsAndDrifts(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec("INSERT INTO public.task_type_centroids").WithArgs("code", "test-model", "[1,0]").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("SELECT centroid::text, sample_count").WithArgs("code", "test-model").
		WillReturnRows(pgxmock.NewRows([]string{"centroid", "sample_count"}).AddRow("[0.8,0.6]", 1))
	mock.ExpectExec("UPDATE public.task_type_centroids").WithArgs(pgxmock.AnyArg(), 2, "code", "test-model").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	classifier := NewEmbeddingClassifier(mock, nil, "test-model", 2)
	if err := classifier.UpdateCentroidEMA(context.Background(), TaskCode, []float32{1, 0}); err != nil {
		t.Fatalf("UpdateCentroidEMA: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
