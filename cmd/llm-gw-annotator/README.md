# llm-gw-annotator

Human annotation tool for AUTO route training data.

## Overview

`llm-gw-annotator` is a CLI tool for managing the human annotation workflow for low-confidence AUTO route samples. It enables human reviewers to validate and correct ML predictions, providing high-quality training data.

## Privacy Guarantee

✅ **Only exports structured features** (model, task, tokens, region, etc.)  
❌ **Never exports prompts, messages, or responses**  
✅ **Annotators only see non-reversible features**

## Quick Start

### Prerequisites

1. PostgreSQL database with Migration 664 applied
2. Go 1.21+ for building
3. `DATABASE_URL` environment variable set

### Installation

```bash
# Build the tool
go build -o llm-gw-annotator

# Verify installation
./llm-gw-annotator --help
```

### Usage

#### 1. Export low-confidence samples

```bash
./llm-gw-annotator export \
  --start 2026-08-01 \
  --end 2026-09-01 \
  --max-confidence 0.7 \
  --limit 1000 \
  --output annotations.csv
```

#### 2. Human annotation

Open `annotations.csv` in Excel or Google Sheets and fill in:
- `human_provider`: The correct provider (e.g., `openai`, `anthropic`, `aws_bedrock`)
- `is_correct`: `TRUE` or `FALSE`
- `reason`: `performance`, `cost`, `availability`, `quality`, `other`, or `correct`
- `annotator`: Your username or email

#### 3. Validate CSV format

```bash
./llm-gw-annotator validate annotations.csv
```

#### 4. Import annotations

```bash
./llm-gw-annotator import annotations.csv
```

#### 5. View statistics

```bash
./llm-gw-annotator stats
```

## Commands

### `export`

Export low-confidence samples to CSV.

**Flags:**
- `--start`: Start date (YYYY-MM-DD, required)
- `--end`: End date (YYYY-MM-DD, required)
- `--min-confidence`: Minimum confidence (default: 0.0)
- `--max-confidence`: Maximum confidence (default: 0.7)
- `--limit`: Max samples to export (default: 1000)
- `--output`: Output CSV file path (required)

**Example:**
```bash
./llm-gw-annotator export \
  --start 2026-08-01 \
  --end 2026-09-01 \
  --max-confidence 0.7 \
  --limit 500 \
  --output annotations.csv
```

### `validate`

Validate CSV format before import.

**Example:**
```bash
./llm-gw-annotator validate annotations.csv
```

### `import`

Import annotated CSV to database.

**Example:**
```bash
./llm-gw-annotator import annotations.csv
```

### `stats`

Show annotation statistics.

**Example:**
```bash
./llm-gw-annotator stats
```

## CSV Format

### Exported CSV (for annotation)

```csv
request_id,model_name,task_type,prompt_tokens,is_streaming,has_vision,region,profile,auto_provider,confidence,human_provider,is_correct,reason,annotator
req_001,gpt-4,chat,150,false,false,us-east-1,balanced,openai,0.65,,,,,
```

### Annotated CSV (for import)

```csv
request_id,model_name,task_type,prompt_tokens,is_streaming,has_vision,region,profile,auto_provider,confidence,human_provider,is_correct,reason,annotator
req_001,gpt-4,chat,150,false,false,us-east-1,balanced,openai,0.65,openai,TRUE,correct,alice
req_002,claude-3-opus,chat,200,true,false,eu-west-1,quality,anthropic,0.58,aws_bedrock,FALSE,cost,bob
```

### Required Annotation Fields

- **human_provider**: The correct provider (required)
- **is_correct**: `TRUE` if ML prediction is correct, `FALSE` otherwise (required)
- **reason**: Annotation reason (optional but recommended)
  - `correct`: ML prediction is correct
  - `performance`: Performance reason (latency, throughput)
  - `cost`: Cost reason
  - `availability`: Availability reason
  - `quality`: Quality reason (output quality)
  - `other`: Other reason
- **annotator**: Annotator username or email (required)

## Configuration

### Database Connection

Set via environment variable or command-line flag:

```bash
# Environment variable
export DATABASE_URL="postgresql://user:pass@localhost:5432/llm_gateway"

# Command-line flag
./llm-gw-annotator --db "postgresql://..." export ...
```

## Demo

Run the quick start demo to see the complete workflow:

```bash
./quickstart.sh
```

This will:
1. Export sample data
2. Create mock annotations
3. Validate the CSV
4. Import annotations
5. Show statistics

## Documentation

- **Deployment Guide**: `../../docs/deployment/p2.1-human-annotation-workflow.md`
- **Completion Summary**: `../../docs/planning/p2.1-completion-summary.md`
- **P2 Planning**: `../../docs/planning/p2-next-steps.md`

## Development

### Run Tests

```bash
cd ../..
go test -v ./annotation/...
```

### Build

```bash
go build -o llm-gw-annotator
```

### Code Structure

```
annotation/
  types.go       - Core types and validation
  exporter.go    - Export low-confidence samples
  importer.go    - Import and validate annotations
  stats.go       - Statistics queries
  *_test.go      - Unit tests

cmd/llm-gw-annotator/
  main.go        - CLI tool entry point
  quickstart.sh  - Demo script
  README.md      - This file
```

## Troubleshooting

### No samples to export

**Cause**: No samples match the criteria in the date range.

**Solution**: Adjust the confidence range or date range.

```bash
# Increase max confidence
./llm-gw-annotator export \
  --start 2026-08-01 --end 2026-09-01 \
  --max-confidence 0.8 \
  --output annotations.csv
```

### Import shows "already annotated"

**Cause**: The request_id already has an annotation.

**Solution**: Skip or delete the existing annotation.

```sql
-- Check existing annotation
SELECT * FROM training_human_annotations WHERE request_id = 'req_xxx';

-- Delete if needed (use with caution)
DELETE FROM training_human_annotations WHERE request_id = 'req_xxx';
```

### Validation fails with "invalid reason"

**Cause**: The reason field contains an invalid value.

**Solution**: Use one of the valid reasons: `performance`, `cost`, `availability`, `quality`, `other`, `correct`.

## Support

For issues or questions, contact the AUTO Route Team.

## License

Internal use only - Part of LLM Gateway project.
