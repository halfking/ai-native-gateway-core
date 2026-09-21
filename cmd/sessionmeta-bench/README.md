# sessionmeta-bench

Session metadata quality benchmark: task classification, title extraction, and summary generation evaluation against production sessions with privacy-preserving anonymization.

## Overview

This tool implements the complete evaluation pipeline for session metadata quality:

1. **Phase 1**: Sample and anonymize real production sessions
2. **Phase 2**: Generate gold labels with dual-model consensus
3. **Phase 3**: Evaluate models (cloud baselines + local MLX/DFlash2)
4. **Phase 4**: Generate comparative reports

## Prerequisites

### Database Access (Phase 1 only)
- Read-only PostgreSQL access to `request_logs` and `session_turns`
- `DATABASE_URL` environment variable or `--dsn` flag

### Model Access
- **Cloud baselines**: OpenAI/Gemini API keys
- **Local models**: mlx-dspark server (https://github.com/ARahim3/mlx-dspark)

### Environment Variables
```bash
# Phase 1: Sampling
export DATABASE_URL="postgres://user:pass@host:5432/dbname?sslmode=require"

# Phase 2-4: Evaluation
export OPENAI_API_KEY="sk-..."
export GEMINI_API_KEY="..."
export MLX_ENDPOINT="http://127.0.0.1:8080/v1"  # optional, default shown
```

## Usage

### Phase 1: Sample Production Sessions

```bash
# Build
cd cmd/sessionmeta-bench
go build

# Sample (requires DATABASE_URL)
./sessionmeta-bench sample \
  --output testdata/sessions.jsonl \
  --target 80 \
  --seed 42 \
  --days 30

# Output: testdata/sessions.jsonl (anonymized, git-safe)
```

**Privacy guarantees**:
- Session hash: SHA256(tenant_id + session_id + seed), irreversible
- All PII anonymized: emails → `[EMAIL]`, phones → `[PHONE]`, IPs → `[IP]`
- Bearer tokens → `[TOKEN]`, connection strings → `[CONNECTION_STRING]`
- File paths → `[FILE_PATH]`, URL queries → `?[QUERY]`
- System prompts excluded from dialogue extraction
- No raw tenant_id/session_id/user_id in output

**Sampling strategy**:
- Stratified by turn count, language, client type
- Excludes internal actors: auto-title-generator, quality-test, self-check, probes
- Fixed random seed for reproducibility

### Phase 2: Generate Gold Labels (NOT IMPLEMENTED YET)

```bash
./sessionmeta-bench annotate \
  --input testdata/sessions.jsonl \
  --output testdata/gold.jsonl \
  --review testdata/review.csv \
  --model1 gpt-4o-mini \
  --model2 gemini-2.0-flash-exp
```

**Dual-model consensus protocol**:
1. Each session labeled independently by two high-quality cloud models
2. Consensus samples (both agree + high confidence) → `gold.jsonl`
3. Divergence samples → `review.csv` for human arbitration
4. Stratified 20% of consensus samples also go to review for quality audit

**Output schema** (gold.jsonl):
```json
{
  "session_hash": "abc123...",
  "gold_label": "coding",
  "gold_title": "Implement retry with exponential backoff",
  "gold_summary": "User requested HTTP client retry logic...",
  "confidence": 0.95,
  "annotator_agreement": true,
  "human_verified": false
}
```

### Phase 3: Evaluate Models (NOT IMPLEMENTED YET)

```bash
# Cloud baseline
./sessionmeta-bench eval \
  --mode all \
  --gold testdata/gold.jsonl \
  --endpoint https://api.openai.com/v1 \
  --model gpt-4o-mini \
  --output results/gpt4o-mini.json

# Local MLX baseline
./sessionmeta-bench eval \
  --mode all \
  --gold testdata/gold.jsonl \
  --endpoint http://127.0.0.1:8080/v1 \
  --model mlx-community/Qwen3-8B-8bit \
  --output results/mlx-baseline.json

# Local DFlash2 (same endpoint, different --mode in mlx-dspark serve)
./sessionmeta-bench eval \
  --mode all \
  --gold testdata/gold.jsonl \
  --endpoint http://127.0.0.1:8080/v1 \
  --model mlx-community/Qwen3-8B-8bit \
  --output results/mlx-dflash2.json
```

**Evaluation modes**:
- `label`: Task classification only (10 V3 categories)
- `title`: Title extraction only
- `summary`: Summary generation only
- `all`: All three (default)

**Metrics collected**:
- **Classification**: Accuracy, macro-F1, per-class P/R/F1, confusion matrix
- **Title**: Format success rate, rune length compliance, leakage detection
- **Summary**: Relevance, faithfulness, specificity (LLM-as-judge)
- **Performance**: P50/P95/P99 latency, throughput, error rates
- **Cost**: Token usage (cloud), memory/GPU (local)

### Phase 4: Generate Report (NOT IMPLEMENTED YET)

```bash
./sessionmeta-bench report \
  --results 'results/*.json' \
  --output report.md
```

Generates comparative markdown report with:
- Accuracy tables and confusion matrices
- Latency/cost tradeoff charts (markdown tables)
- Stratified analysis (short/long sessions, zh/en/mixed)
- Quality vs speed comparison (baseline vs DFlash2)

## MLX/DFlash2 Setup

### Install mlx-dspark
```bash
# Python 3.10+ required
pip install mlx-dspark

# Verify
mlx-dspark --help
```

### Start baseline server
```bash
mlx-dspark serve \
  --model mlx-community/Qwen3-8B-8bit \
  --mode baseline \
  --max-batch 4 \
  --host 127.0.0.1

# Health check
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/v1/models
```

### Start DFlash2 server
```bash
# Stop baseline first, then:
mlx-dspark serve \
  --model mlx-community/Qwen3-8B-8bit \
  --mode dflash \
  --max-batch 4 \
  --host 127.0.0.1
```

**Model selection guide** (by memory):
- 16GB: `Qwen3-4B-8bit` or `Qwen3-8B-8bit`
- 32GB: `Qwen3-8B-4bit` or `Qwen3.5-14B-8bit`
- 64GB+: `Qwen3.8-27B-8bit`

**DFlash2 compatibility**: Not all main models have DFlash2 drafters. Check:
- https://huggingface.co/incoai (official DFlash2 models)
- `mlx-dspark serve --help` for supported pairs

## Project Structure

```
cmd/sessionmeta-bench/
├── main.go              # CLI entry point
├── sampler.go           # Phase 1: Production sampling + anonymization
├── sampler_test.go      # Privacy regression tests
├── annotator.go         # Phase 2: Dual-model consensus (TODO)
├── evaluator.go         # Phase 3: Model evaluation (TODO)
├── reporter.go          # Phase 4: Report generation (TODO)
├── testdata/
│   ├── sessions.jsonl   # Anonymized production samples (git-ignored)
│   ├── gold.jsonl       # Gold labels (git-ignored if contains samples)
│   └── review.csv       # Human review queue (git-ignored)
└── results/
    ├── gpt4o-mini.json  # Evaluation results (git-ignored)
    ├── mlx-baseline.json
    └── mlx-dflash2.json
```

## Testing

```bash
# Unit tests (no database required)
go test -v

# Integration test with database (manual)
export DATABASE_URL="..."
./sessionmeta-bench sample --output /tmp/test.jsonl --target 5
jq . /tmp/test.jsonl | grep -E '\[EMAIL\]|\[PHONE\]|\[IP\]'  # verify anonymization
```

## Current Status

- ✅ Phase 1.1: Sampling + anonymization implementation
- ✅ Phase 1.1: Unit tests (100% pass)
- 🚧 Phase 1.2: Execute sampling (requires DATABASE_URL)
- ⏳ Phase 2.1: Dual-model annotator
- ⏳ Phase 2.2: Consensus + review generation
- ⏳ Phase 3: Evaluation engine
- ⏳ Phase 4: Report generation

## References

- [V3 Task Types](../../autoroute/task_types_v3.go) — 10-category classification
- [Session Summarizer](../../domains/sessionsummary/summarizer.go) — Production title/summary
- [MLX-DSpark](https://github.com/ARahim3/mlx-dspark) — Local inference server
- [DFlash2 Models](https://huggingface.co/incoai) — Speculative drafters

## Security Notes

- **Never commit** `DATABASE_URL`, API keys, or raw production data
- Review `.gitignore` before pushing testdata/
- Anonymization is one-way; session hashes cannot be reversed
- MLX server binds `127.0.0.1` by default (no external exposure)
- Cloud API calls use TLS; logs must not echo PII

## Troubleshooting

### Database connection fails
```bash
# Test read-only access
psql "$DATABASE_URL" -c "SHOW default_transaction_read_only;"
# Should return: on
```

### MLX server won't start
```bash
# Check Python version
python3 --version  # >= 3.10 required

# Check available memory
sysctl hw.memsize  # macOS
free -h            # Linux

# Try smaller model
mlx-dspark serve --model mlx-community/Qwen3-4B-8bit --mode baseline
```

### Sampling returns < target
- Widen time window: `--days 60`
- Relax turn constraints: `--min-turns 1 --max-turns 50`
- Check exclusion list in `sampler.go:ExcludeActors`
