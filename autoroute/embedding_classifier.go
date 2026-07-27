package autoroute

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// EmbeddingClient is the minimal embedding API needed by the shadow classifier.
// It is defined here to keep autoroute independent from domains/analysis.
type EmbeddingClient interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// EmbeddingDB is the minimal PostgreSQL API needed by the classifier.
// It also allows pgxmock to cover vector queries and EMA transactions.
type EmbeddingDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// EmbeddingClassifier finds the nearest task centroid using cosine similarity.
// Centroids are updated online by UpdateCentroidEMA after sampled requests.
//
// 2026-07-27: the struct carries no mutable in-memory state — pool/embed/
// model/dim/alpha are write-once at construction — so there is nothing left
// for a mutex to protect (see UpdateCentroidEMA).
type EmbeddingClassifier struct {
	pool  EmbeddingDB
	embed EmbeddingClient
	model string
	dim   int
	alpha float64
}

// NewEmbeddingClassifier creates an embedding classifier with EMA defaults.
func NewEmbeddingClassifier(pool EmbeddingDB, embed EmbeddingClient, model string, dim int) *EmbeddingClassifier {
	if dim <= 0 {
		dim = 1024
	}
	return &EmbeddingClassifier{
		pool:  pool,
		embed: embed,
		model: strings.TrimSpace(model),
		dim:   dim,
		alpha: 0.95,
	}
}

// Name implements Classifier.
func (c *EmbeddingClassifier) Name() string { return "embedding" }

// Classify embeds the final user prompt and returns its nearest stored centroid.
func (c *EmbeddingClassifier) Classify(ctx context.Context, sigs ClassificationSignals) (*Classification, error) {
	classification, _, err := c.classifyWithVector(ctx, sigs)
	return classification, err
}

// ClassifyWithVector is used by the shadow path so the embedding is generated
// once and can also be fed into the asynchronous EMA update.
func (c *EmbeddingClassifier) ClassifyWithVector(ctx context.Context, sigs ClassificationSignals) (*Classification, []float32, error) {
	return c.classifyWithVector(ctx, sigs)
}

func (c *EmbeddingClassifier) classifyWithVector(ctx context.Context, sigs ClassificationSignals) (*Classification, []float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return &Classification{Primary: TaskChat, Confidence: 0, Classifier: "embedding", Signals: sigs}, nil, fmt.Errorf("embedding classifier: classifier is nil")
	}
	if strings.TrimSpace(sigs.LastUserPrompt) == "" {
		return &Classification{Primary: TaskChat, Confidence: 0, Classifier: c.Name(), Signals: sigs}, nil, nil
	}
	if c.embed == nil {
		return c.lowConfidence("embedding client not configured"), nil, fmt.Errorf("embedding classifier: client not configured")
	}
	if c.pool == nil {
		return c.lowConfidence("database not configured"), nil, fmt.Errorf("embedding classifier: database not configured")
	}

	vec, err := c.embed.Embed(ctx, sigs.LastUserPrompt)
	if err != nil {
		return c.lowConfidence("embedding failed"), nil, fmt.Errorf("embedding classifier: embed: %w", err)
	}
	if err := validateVector(vec, c.dim); err != nil {
		return c.lowConfidence("invalid embedding"), nil, err
	}

	var task string
	var similarity float64
	err = c.pool.QueryRow(ctx, `
		SELECT task_type, 1 - (centroid <=> $1::vector) AS similarity
		FROM public.task_type_centroids
		WHERE embedding_model = $2
		ORDER BY centroid <=> $1::vector
		LIMIT 1`, vectorLiteral(vec), c.model).Scan(&task, &similarity)
	if err != nil {
		if err == pgx.ErrNoRows {
			return &Classification{Primary: TaskChat, Confidence: 0, Classifier: c.Name(), Signals: sigs}, vec, nil
		}
		return c.lowConfidence("nearest centroid query failed"), vec, fmt.Errorf("embedding classifier: nearest centroid: %w", err)
	}
	if !isValidTaskType(TaskType(task)) {
		return c.lowConfidence("centroid contains invalid task type"), vec, fmt.Errorf("embedding classifier: invalid task type %q", task)
	}
	if math.IsNaN(similarity) || math.IsInf(similarity, 0) {
		similarity = 0
	}
	if similarity < 0 {
		similarity = 0
	}
	if similarity > 1 {
		similarity = 1
	}
	return &Classification{
		Primary:    TaskType(task),
		Confidence: similarity,
		Signals:    sigs,
		Classifier: c.Name(),
		Reason:     "nearest task-type centroid",
	}, vec, nil
}

func (c *EmbeddingClassifier) lowConfidence(reason string) *Classification {
	return &Classification{Primary: TaskChat, Confidence: 0, Classifier: c.Name(), Reason: reason}
}

// UpdateCentroidEMA incorporates vec into taskType's centroid. The row lock
// keeps concurrent requests from losing samples while the Go-side EMA avoids
// relying on pgvector arithmetic operators.
//
// 并发说明:此函数没有可变的进程内状态需要保护 (pool/model/dim/alpha 在启动后
// 不可变),EMA 更新的原子性由数据库事务 + SELECT ... FOR UPDATE 行锁保证。
// 因此不再持有进程级 mutex —— 旧实现把 mutex 跨整段 DB 事务持有,任何 DB 停顿
// (行锁等待、慢盘) 都会阻塞所有访问该分类器的 goroutine,而该锁对正确性没有贡献。
func (c *EmbeddingClassifier) UpdateCentroidEMA(ctx context.Context, taskType TaskType, vec []float32) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.pool == nil {
		return fmt.Errorf("embedding classifier: database not configured")
	}
	if !isValidTaskType(taskType) {
		return fmt.Errorf("embedding classifier: invalid task type %q", taskType)
	}
	if err := validateVector(vec, c.dim); err != nil {
		return err
	}

	// 2026-07-27 concurrency fix: no process-wide mutex around the
	// transaction. Correctness comes from the row-level `SELECT ... FOR
	// UPDATE` below: concurrent EMA updaters (in this process or any other
	// instance) serialize on the centroid row itself, so no sample is lost.
	// Holding c.mu across BeginTx → QueryRow → Exec → Commit added nothing
	// (it cannot coordinate other instances) while a single DB stall pinned
	// every shadow-EMA goroutine behind it.
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("embedding classifier: begin EMA transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO public.task_type_centroids (task_type, embedding_model, centroid, sample_count)
		VALUES ($1, $2, $3::vector, 0)
		ON CONFLICT (task_type, embedding_model) DO NOTHING`, string(taskType), c.model, vectorLiteral(vec))
	if err != nil {
		return fmt.Errorf("embedding classifier: initialize centroid: %w", err)
	}

	var rawCentroid string
	var sampleCount int
	err = tx.QueryRow(ctx, `
		SELECT centroid::text, sample_count
		FROM public.task_type_centroids
		WHERE task_type = $1 AND embedding_model = $2
		FOR UPDATE`, string(taskType), c.model).Scan(&rawCentroid, &sampleCount)
	if err != nil {
		return fmt.Errorf("embedding classifier: read centroid: %w", err)
	}
	old, err := parseVector(rawCentroid, c.dim)
	if err != nil {
		return fmt.Errorf("embedding classifier: parse centroid: %w", err)
	}
	if sampleCount == 0 {
		old = append([]float32(nil), vec...)
	} else {
		for i := range old {
			old[i] = float32(c.alpha*float64(old[i]) + (1-c.alpha)*float64(vec[i]))
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE public.task_type_centroids
		SET centroid = $1::vector, sample_count = $2, updated_at = now()
		WHERE task_type = $3 AND embedding_model = $4`, vectorLiteral(old), sampleCount+1, string(taskType), c.model)
	if err != nil {
		return fmt.Errorf("embedding classifier: update centroid: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("embedding classifier: commit EMA transaction: %w", err)
	}
	return nil
}

func validateVector(vec []float32, dim int) error {
	if len(vec) != dim {
		return fmt.Errorf("embedding classifier: vector dimensions %d, want %d", len(vec), dim)
	}
	for i, value := range vec {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("embedding classifier: vector value %d is not finite", i)
		}
	}
	return nil
}

func vectorLiteral(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, value := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func parseVector(raw string, dim int) ([]float32, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return nil, fmt.Errorf("invalid vector literal")
	}
	parts := strings.Split(raw[1:len(raw)-1], ",")
	if len(parts) != dim {
		return nil, fmt.Errorf("vector dimensions %d, want %d", len(parts), dim)
	}
	vec := make([]float32, dim)
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, fmt.Errorf("value %d: %w", i, err)
		}
		vec[i] = float32(value)
	}
	return vec, nil
}
