// Command bleve-backfill is a one-shot CLI that replays historical
// gateway log files (gateway-*.log and the rotated gateway-*.log.gz
// companions) into the Bleve index used by /api/admin/logs/search.
//
// Why this is a separate command
// ──────────────────────────────
// The BleveFanoutHandler in internal/logging only sees records
// emitted from the moment the gateway starts. Existing deployments
// have months of archived logs sitting under logs/ that operators
// routinely ask about — a single replay command keeps the admin
// search useful on day 1 without forcing the gateway to keep
// multi-month data resident.
//
// Usage
//
//	bleve-backfill \
//	  --log-dir /var/log/llm-gateway \
//	  --index-dir /var/lib/llm-gateway/bleve \
//	  --batch 512 \
//	  --workers 4 \
//	  --since 2026-01-01 \
//	  --dry-run
//
// Flags
//
//	--log-dir    Directory containing gateway.log + rotated .gz files.
//	             Defaults to LLM_GATEWAY_LOG_FILE's directory, or ./logs.
//	--index-dir  Bleve scorch index directory. Defaults to LLM_GATEWAY_LOG_INDEX_DIR
//	             or {log-dir}/../bleve.
//	--batch      Records per Bleve batch flush. Default 512.
//	--workers    Number of file-decode workers (1-16). Default 4.
//	--since      Only backfill records whose timestamp is >= this RFC3339
//	             value. Empty = backfill everything.
//	--dry-run    Parse but do not write to the index. Useful to count.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
)

func main() {
	var (
		logDir    = flag.String("log-dir", envOr("LLM_GATEWAY_LOG_DIR", "./logs"), "log directory to replay")
		indexDir  = flag.String("index-dir", envOr("LLM_GATEWAY_LOG_INDEX_DIR", ""), "bleve index directory")
		batchSize = flag.Int("batch", 512, "bleve batch flush threshold")
		workers   = flag.Int("workers", 4, "concurrent file decoders")
		since     = flag.String("since", "", "RFC3339 cutoff; only newer records are indexed")
		dryRun    = flag.Bool("dry-run", false, "parse only; do not write to the index")
	)
	flag.Parse()

	if *indexDir == "" {
		*indexDir = filepath.Join(*logDir, "..", "bleve")
	}
	if abs, err := filepath.Abs(*indexDir); err == nil {
		*indexDir = abs
	}

	var sinceT time.Time
	if *since != "" {
		t, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			log.Fatalf("--since must be RFC3339: %v", err)
		}
		sinceT = t
	}

	files, err := collectFiles(*logDir)
	if err != nil {
		log.Fatalf("scan log dir: %v", err)
	}
	if len(files) == 0 {
		log.Fatalf("no log files found under %s", *logDir)
	}
	log.Printf("found %d log file(s) under %s", len(files), *logDir)

	var idx bleve.Index
	if !*dryRun {
		if err := os.MkdirAll(*indexDir, 0o755); err != nil {
			log.Fatalf("create index dir: %v", err)
		}
		idx, err = bleve.Open(*indexDir)
		if err != nil {
			idx, err = bleve.New(*indexDir, bleve.NewIndexMapping())
			if err != nil {
				log.Fatalf("open/create index: %v", err)
			}
		}
		defer idx.Close()
		log.Printf("index: %s (batch=%d, workers=%d, dry-run=%v)", *indexDir, *batchSize, *workers, *dryRun)
	}

	var (
		count    atomic.Uint64
		skipped  atomic.Uint64
		errors   atomic.Uint64
		startAll = time.Now()
	)

	batchCh := make(chan *bleveBatch, *workers*2)
	var writerWG sync.WaitGroup
	if !*dryRun {
		writerWG.Add(1)
		go func() {
			defer writerWG.Done()
			batch := idx.NewBatch()
			flushTicker := time.NewTicker(500 * time.Millisecond)
			defer flushTicker.Stop()
			flush := func() {
				if batch.Size() == 0 {
					return
				}
				if err := idx.Batch(batch); err != nil {
					log.Printf("bleve batch flush: %v", err)
				}
				batch = idx.NewBatch()
			}
			for {
				select {
				case b, ok := <-batchCh:
					if !ok {
						flush()
						return
					}
					for _, item := range b.items {
						if err := batch.Index(item.id, item.doc); err != nil {
							errors.Add(1)
						}
					}
					if batch.Size() >= *batchSize {
						flush()
					}
				case <-flushTicker.C:
					flush()
				}
			}
		}()
	}

	var workerWG sync.WaitGroup
	work := make(chan string, len(files))
	for i := 0; i < *workers; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for path := range work {
			n, err := decodeFile(path, sinceT, batchCh, *dryRun, &count, &skipped)
			if err != nil {
				errors.Add(1)
				log.Printf("decode %s: %v", path, err)
				continue
			}
				log.Printf("  %s: %d record(s)", filepath.Base(path), n)
			}
		}()
	}
	for _, f := range files {
		work <- f
	}
	close(work)
	workerWG.Wait()
	close(batchCh)
	if !*dryRun {
		writerWG.Wait()
	}

	took := time.Since(startAll)
	log.Printf("done: indexed=%d skipped=%d errors=%d took=%s",
		count.Load(), skipped.Load(), errors.Load(), took)
}

// collectFiles returns all gateway.log + gateway-*.log(.gz)? files
// in the directory, sorted by name (which is also time-order thanks
// to lumberjack's timestamp suffix).
func collectFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if n == "gateway.log" || strings.HasPrefix(n, "gateway-") {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Strings(out)
	return out, nil
}

// decodeFile streams a single log file (handling .gz transparently),
// parses each line as JSON, and pushes enriched docs into the
// shared batch channel.
func decodeFile(path string, since time.Time, batchCh chan<- *bleveBatch, dryRun bool, count, skipped *atomic.Uint64) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var src io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		src = gz
	}

	scanner := bufio.NewScanner(src)
	// 4MB line buffer: large enough for our structured JSON records
	// even when a stack trace is inlined.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	local := &bleveBatch{items: make([]bleveItem, 0, 256)}
	flushed := 0
	flushLocal := func() {
		if len(local.items) == 0 {
			return
		}
		if !dryRun {
			batchCh <- local
		}
		flushed += len(local.items)
		local = &bleveBatch{items: make([]bleveItem, 0, 256)}
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(line, &raw); err != nil {
			skipped.Add(1)
			continue
		}
		tsStr, _ := raw["ts"].(string)
		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, tsStr)
		}
		if err != nil {
			skipped.Add(1)
			continue
		}
		if !since.IsZero() && ts.Before(since) {
			skipped.Add(1)
			continue
		}

		doc := map[string]interface{}{
			"ts":    ts,
			"level": stringOf(raw["level"]),
			"msg":   stringOf(raw["msg"]),
			"raw":   string(line),
		}
		// Lift common trace ids the admin search filter uses.
		for _, k := range []string{"request_id", "tenant_id", "user_id", "session_id", "trace_id", "model", "provider_id"} {
			if v, ok := raw[k]; ok {
				doc[k] = v
			}
		}

		local.items = append(local.items, bleveItem{
			id:  strconv.FormatInt(ts.UnixNano(), 10),
			doc: doc,
		})
		count.Add(1)
		if len(local.items) >= 256 {
			flushLocal()
		}
	}
	flushLocal()

	if err := scanner.Err(); err != nil {
		return flushed, err
	}
	return flushed, nil
}

type bleveBatch struct {
	items []bleveItem
}

type bleveItem struct {
	id  string
	doc map[string]interface{}
}

func stringOf(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
