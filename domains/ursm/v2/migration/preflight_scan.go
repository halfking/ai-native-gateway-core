package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Preflight performs the read-only inventory phase. It never writes a key:
// its output is the input to the durable ledger/copy phases (doc 14 §4).
type Preflight struct {
	rdb    *redis.Client
	prefix string
}

func NewPreflight(rdb *redis.Client, prefix string) *Preflight {
	return &Preflight{rdb: rdb, prefix: prefix}
}

// Entry is the exact, auditable inventory record for one source Redis key.
type Entry struct {
	SourceKey     string
	TargetKey     string
	Class         Classification
	Reason        string
	Schema        store.KeySchema
	Type          string
	PTTLMillis    int64
	Generation    int64
	FieldChecksum string
	Tuple         *store.ParsedNodeKey
}

// Report is a deterministic preflight result. Go is true only when no
// ambiguous or conflict verdict exists; a non-zero excluded count does not
// authorize those entries — it only records why they are non-authoritative.
type Report struct {
	Entries          []Entry
	Checksum         string
	Migratable       int
	CanonicalPresent int
	Ambiguous        int
	Excluded         int
}

func (r Report) Go() bool { return r.Ambiguous == 0 }

// Scan uses SCAN rather than KEYS and reads only `prefix + node:*` in this
// slice. Associated windows and candidate indexes are accounted for by the
// logical node tuple in later copy/cleanup phases; candidate indexes are
// separately rebuilt, never copied (doc 14 §2/§5.2.6).
func (p *Preflight) Scan(ctx context.Context) (Report, error) {
	if p == nil || p.rdb == nil {
		return Report{}, fmt.Errorf("ursm.v2: preflight requires redis")
	}
	var keys []string
	var cursor uint64
	for {
		batch, next, err := p.rdb.Scan(ctx, cursor, p.prefix+"node:*", 200).Result()
		if err != nil {
			return Report{}, fmt.Errorf("ursm.v2: preflight scan: %w", err)
		}
		keys = append(keys, batch...)
		if next == 0 {
			break
		}
		cursor = next
	}
	sort.Strings(keys)
	report := Report{Entries: make([]Entry, 0, len(keys))}
	for _, key := range keys {
		entry, err := p.inspect(ctx, key)
		if err != nil {
			return Report{}, err
		}
		report.Entries = append(report.Entries, entry)
		switch entry.Class {
		case ClassMigratable:
			report.Migratable++
		case ClassCanonicalPresent:
			report.CanonicalPresent++
		case ClassAmbiguous:
			report.Ambiguous++
		case ClassExcludedNonAuthoritative:
			report.Excluded++
		}
	}
	report.Checksum = checksumEntries(report.Entries)
	return report, nil
}

func (p *Preflight) inspect(ctx context.Context, key string) (Entry, error) {
	c := ClassifyKey(p.prefix, key)
	e := Entry{SourceKey: key, Class: c.Class, Reason: c.Reason, Schema: c.Schema, Tuple: c.Tuple}
	kind, err := p.rdb.Type(ctx, key).Result()
	if err != nil {
		return Entry{}, fmt.Errorf("ursm.v2: preflight type %s: %w", key, err)
	}
	e.Type = kind
	pttl, err := p.rdb.PTTL(ctx, key).Result()
	if err != nil {
		return Entry{}, fmt.Errorf("ursm.v2: preflight pttl %s: %w", key, err)
	}
	e.PTTLMillis = pttlMillis(pttl)
	if kind == "hash" {
		fields, err := p.rdb.HGetAll(ctx, key).Result()
		if err != nil {
			return Entry{}, fmt.Errorf("ursm.v2: preflight hgetall %s: %w", key, err)
		}
		e.FieldChecksum = checksumFields(fields)
		e.Generation = parseGeneration(fields["generation"])
	}
	if e.Class == ClassMigratable && e.Tuple != nil {
		target, err := store.K2NodeKeyForTenant(p.prefix, e.Tuple.TenantID, e.Tuple.CredentialID, e.Tuple.RawModel)
		if err != nil {
			return Entry{}, fmt.Errorf("ursm.v2: preflight canonical target %s: %w", key, err)
		}
		e.TargetKey = target
	}
	return e, nil
}

func pttlMillis(ttl time.Duration) int64 {
	// go-redis maps Redis's special values to negative durations. Preserve
	// the Redis protocol meaning in milliseconds: -2 expired/missing, -1
	// persistent, otherwise positive remaining time (doc 14 §6.1).
	if ttl == -2*time.Nanosecond {
		return -2
	}
	if ttl == -1*time.Nanosecond {
		return -1
	}
	return ttl.Milliseconds()
}

func parseGeneration(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// checksumFields is stable across unordered Redis HGETALL replies: sorted
// field names, NUL separator, value, newline; its format is frozen because
// copy fencing and exact-key cleanup rely on it (doc 15 §2).
func checksumFields(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(fields[k]))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func checksumEntries(entries []Entry) string {
	h := sha256.New()
	for _, e := range entries {
		parts := []string{
			e.SourceKey, e.TargetKey, string(e.Class), e.Reason, strconv.Itoa(int(e.Schema)),
			e.Type, strconv.FormatInt(e.PTTLMillis, 10), strconv.FormatInt(e.Generation, 10), e.FieldChecksum,
		}
		_, _ = h.Write([]byte(strings.Join(parts, "\x00")))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
