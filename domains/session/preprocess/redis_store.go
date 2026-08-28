package preprocess

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/redis/go-redis/v9"
)

// Redis store layout (R11.7):
//
//	manifest: session:artifact:{ver}:{tenantHash}:{sessionHash}:manifest
//	payload:  session:artifact:{ver}:{tenantHash}:{sessionHash}:{kind}:{variant}
//	lease:    session:artifact:{ver}:{tenantHash}:{sessionHash}:{kind}:lease:{depHash}
//
// tenantHash/sessionHash are the first 16 hex chars (8 bytes) of sha256 of the
// plain ids; keys are therefore tenant-scoped without leaking raw ids.
//
// The manifest is a flat HASH so that optional fields are cleaned with HDEL
// (empty value in the CAS script means delete — no HSET-only residue).
const (
	encodingAESGCM = "aesgcm-v1"
	encodingGZIP   = "gzip"
	encodingPlain  = "plain"

	fieldTurnNo  = "turn_no"
	fieldHead    = "head_request_id"
	fieldChain   = "chain_hash"
	fieldFlags   = "flags"
	fieldPayload = "payload"
	fieldEnc     = "encoding"
)

// casWriteScript atomically:
//  1. compares the stored manifest revision with the expected one
//     (a missing manifest compares equal to the zero revision);
//  2. on match, writes the new revision triple, flags, a list of manifest
//     field/value pairs (value "" => HDEL) and an optional payload-key group;
//  3. refreshes TTLs.
//
// Returns 1 on success, 0 on revision mismatch, 2 when the target revision
// head is already committed by the same request (idempotent replay, no write).
//
// KEYS[1] = manifest key; KEYS[2] = payload key (used only when the payload
// group is non-empty).
//
// ARGV: [1..3]=expected turn/head/chain, [4..6]=new turn/head/chain (”=keep),
// [7]=flags, [8]=manifest TTL ms, [9]=#manifest pairs, pairs..., then
// #payload pairs, pairs..., payload TTL ms.
var casWriteScript = redis.NewScript(`
local mkey = KEYS[1]
local ct, ch, cc = '0', '', string.rep('0', 64)
if redis.call('EXISTS', mkey) == 1 then
  local cur = redis.call('HMGET', mkey, 'turn_no', 'head_request_id', 'chain_hash')
  if cur[1] then ct = cur[1] end
  if cur[2] then ch = cur[2] end
  if cur[3] then cc = cur[3] end
end
if ct ~= ARGV[1] or ch ~= ARGV[2] or cc ~= ARGV[3] then
  if ARGV[5] ~= '' and ch == ARGV[5] and ct == ARGV[4] then
    return 2
  end
  return 0
end
if ARGV[4] ~= '' then redis.call('HSET', mkey, 'turn_no', ARGV[4]) end
if ARGV[5] ~= '' then redis.call('HSET', mkey, 'head_request_id', ARGV[5]) end
if ARGV[6] ~= '' then redis.call('HSET', mkey, 'chain_hash', ARGV[6]) end
redis.call('HSET', mkey, 'flags', ARGV[7])
local ttl = tonumber(ARGV[8])
local nf = tonumber(ARGV[9])
local i = 10
local n = i + nf * 2
while i < n do
  if ARGV[i + 1] == '' then
    redis.call('HDEL', mkey, ARGV[i])
  else
    redis.call('HSET', mkey, ARGV[i], ARGV[i + 1])
  end
  i = i + 2
end
local np = tonumber(ARGV[i])
i = i + 1
if np > 0 then
  local j = i
  local pn = j + np * 2
  while j < pn do
    if ARGV[j + 1] == '' then
      redis.call('HDEL', KEYS[2], ARGV[j])
    else
      redis.call('HSET', KEYS[2], ARGV[j], ARGV[j + 1])
    end
    j = j + 2
  end
  local pttl = tonumber(ARGV[pn])
  if pttl > 0 then redis.call('PEXPIRE', KEYS[2], pttl) end
end
if ttl > 0 then redis.call('PEXPIRE', mkey, ttl) end
return 1
`)

var releaseLeaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// RedisArtifactStore is the Redis (+ optional in-process L1) implementation of
// SessionArtifactStore.
type RedisArtifactStore struct {
	opts StoreOptions
	l1   *payloadLRU
}

// NewRedisArtifactStore validates the options and returns the store.
func NewRedisArtifactStore(opts StoreOptions) (*RedisArtifactStore, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("preprocess: store options: redis client is required")
	}
	if isZeroKey(opts.AESKey) {
		return nil, ErrRawKeyRequired
	}
	return &RedisArtifactStore{
		opts: opts,
		l1:   newPayloadLRU(opts.L1BudgetBytes),
	}, nil
}

func isZeroKey(k [32]byte) bool {
	for _, b := range k {
		if b != 0 {
			return false
		}
	}
	return true
}

// compile-time interface check.
var _ SessionArtifactStore = (*RedisArtifactStore)(nil)

func (s *RedisArtifactStore) manifestKey(tenantID, sessionID string) string {
	return fmt.Sprintf("session:artifact:%s:%s:%s:manifest",
		s.opts.keyVersion(), hash16(tenantID), hash16(sessionID))
}

func (s *RedisArtifactStore) artifactKey(tenantID, sessionID string, kind ArtifactKind, variant string) string {
	return fmt.Sprintf("session:artifact:%s:%s:%s:%s:%s",
		s.opts.keyVersion(), hash16(tenantID), hash16(sessionID), kind, variantOrDefault(variant))
}

func (s *RedisArtifactStore) leaseKey(tenantID, sessionID string, kind ArtifactKind, depHash [32]byte) string {
	return fmt.Sprintf("session:artifact:%s:%s:%s:%s:lease:%s",
		s.opts.keyVersion(), hash16(tenantID), hash16(sessionID), kind, hex.EncodeToString(depHash[:8]))
}

// aad builds the AES-GCM additional-authenticated data binding a raw payload
// to (tenant, session, kind): swapping ciphertext across tenants/sessions
// fails decryption.
func aad(tenantID, sessionID string, kind ArtifactKind) []byte {
	return []byte(tenantID + "\x1f" + sessionID + "\x1f" + string(kind))
}

func sealAESGCM(key [32]byte, plaintext, additional []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, additional), nil
}

func openAESGCM(key [32]byte, sealed, additional []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("%w: sealed payload too short", ErrArtifactCorrupted)
	}
	nonce, ct := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, additional)
	if err != nil {
		return nil, fmt.Errorf("%w: aes-gcm open failed (wrong key, tampered ciphertext or AAD mismatch)", ErrArtifactCorrupted)
	}
	return pt, nil
}

// encodePayload applies the at-rest encoding for the kind.
func (s *RedisArtifactStore) encodePayload(tenantID, sessionID string, kind ArtifactKind, payload []byte) ([]byte, string, error) {
	switch kind {
	case ArtifactRaw:
		sealed, err := sealAESGCM(s.opts.AESKey, payload, aad(tenantID, sessionID, kind))
		if err != nil {
			return nil, "", fmt.Errorf("preprocess: seal raw payload: %w", err)
		}
		return sealed, encodingAESGCM, nil
	case ArtifactSanitized:
		if s.opts.CompressSanitized {
			return gzipBytes(payload), encodingGZIP, nil
		}
		return payload, encodingPlain, nil
	default:
		if s.opts.CompressCompressed {
			return gzipBytes(payload), encodingGZIP, nil
		}
		return payload, encodingPlain, nil
	}
}

func (s *RedisArtifactStore) decodePayload(tenantID, sessionID string, kind ArtifactKind, encoding string, data []byte) ([]byte, error) {
	switch encoding {
	case encodingAESGCM:
		if kind != ArtifactRaw {
			return nil, fmt.Errorf("%w: aesgcm encoding on non-raw kind", ErrArtifactCorrupted)
		}
		return openAESGCM(s.opts.AESKey, data, aad(tenantID, sessionID, kind))
	case encodingGZIP:
		out, err := gunzipBytes(data)
		if err != nil {
			return nil, fmt.Errorf("%w: gunzip: %v", ErrArtifactCorrupted, err)
		}
		return out, nil
	case encodingPlain:
		return data, nil
	default:
		return nil, fmt.Errorf("%w: unknown encoding %q", ErrArtifactCorrupted, encoding)
	}
}

// manifest field (de)serialization -------------------------------------------

func metaFieldPrefix(kind ArtifactKind, variant string) (prefix, sep string) {
	switch kind {
	case ArtifactRaw:
		return "raw", "_"
	case ArtifactSanitized:
		return "san", "_"
	default:
		return "cv:" + variantOrDefault(variant), ":"
	}
}

func cvField(variant, field string) string {
	return "cv:" + variantOrDefault(variant) + ":" + field
}

// metaToFields renders meta into flat hash fields; zero-valued optional
// fields (failure code) become "" which the CAS script turns into HDEL.
// sep is "_" for the raw/sanitized prefixes and ":" for compressed variants
// ("cv:<variant>:<field>").
func metaToFields(prefix, sep string, m ArtifactMeta) [][2]string {
	failure := ""
	if m.FailureCode != 0 {
		failure = strconv.FormatUint(uint64(m.FailureCode), 10)
	}
	return [][2]string{
		{prefix + sep + "status", strconv.Itoa(int(m.Status))},
		{prefix + sep + "source", hex32(m.SourceHash)},
		{prefix + sep + "dep", hex32(m.DependencyHash)},
		{prefix + sep + "content", hex32(m.ContentHash)},
		{prefix + sep + "schema", strconv.Itoa(int(m.SchemaVersion))},
		{prefix + sep + "transform", strconv.Itoa(int(m.TransformVer))},
		{prefix + sep + "generated", strconv.FormatInt(m.GeneratedAt, 10)},
		{prefix + sep + "failure", failure},
	}
}

func parseHash32(s string) ([32]byte, error) {
	var out [32]byte
	if s == "" {
		return out, nil
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("preprocess: bad hash field %q", s)
	}
	copy(out[:], b)
	return out, nil
}

// metaFromFields parses meta via a full field-name builder f("status") =>
// "raw_status" / "cv:<variant>:status".
func metaFromFields(get func(string) string) (ArtifactMeta, error) {
	var m ArtifactMeta
	if v, err := strconv.ParseUint(get("status"), 10, 8); err == nil {
		m.Status = ArtifactStatus(v)
	}
	var err error
	if m.SourceHash, err = parseHash32(get("source")); err != nil {
		return m, err
	}
	if m.DependencyHash, err = parseHash32(get("dep")); err != nil {
		return m, err
	}
	if m.ContentHash, err = parseHash32(get("content")); err != nil {
		return m, err
	}
	if v, err := strconv.ParseUint(get("schema"), 10, 16); err == nil {
		m.SchemaVersion = uint16(v)
	}
	if v, err := strconv.ParseUint(get("transform"), 10, 16); err == nil {
		m.TransformVer = uint16(v)
	}
	if v, err := strconv.ParseInt(get("generated"), 10, 64); err == nil {
		m.GeneratedAt = v
	}
	if v, err := strconv.ParseUint(get("failure"), 10, 16); err == nil {
		m.FailureCode = uint16(v)
	}
	return m, nil
}

// readManifest loads the manifest; exists reports whether any field is stored.
func (s *RedisArtifactStore) readManifest(ctx context.Context, tenantID, sessionID string) (*ArtifactManifest, bool, error) {
	// audit-24h-20260828-r4 P2: Use SafeHGetAll to prevent WRONGTYPE errors
	// when the manifest key collides with a non-hash type (e.g. a debug
	// SADD/SET call on the same key during live ops). SafeHGetAll runs
	// a TYPE guard and returns TypedError on mismatch; ErrKeyNotFound is
	// surfaced when the key does not exist (matches raw HGetAll's
	// (nil-map, nil-err) cache-miss signal — both mean "no manifest").
	vals, err := redissafe.SafeHGetAll(ctx, s.opts.Client, s.manifestKey(tenantID, sessionID))
	if err != nil {
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			return NewArtifactManifest(tenantID, sessionID), false, nil
		}
		return nil, false, fmt.Errorf("preprocess: safe hgetall manifest: %w", err)
	}
	m := NewArtifactManifest(tenantID, sessionID)
	if len(vals) == 0 {
		return m, false, nil
	}
	if tn, err := strconv.ParseInt(vals[fieldTurnNo], 10, 32); err == nil {
		m.Revision.TurnNo = int32(tn)
	}
	m.Revision.HeadRequestID = vals[fieldHead]
	if ch, err := parseHash32(vals[fieldChain]); err != nil {
		return nil, false, err
	} else {
		m.Revision.ChainHash = ch
	}
	if f, err := strconv.ParseUint(vals[fieldFlags], 10, 32); err == nil {
		m.Flags = ArtifactFlags(f)
	}
	if m.Raw, err = metaFromFields(func(f string) string { return vals["raw_"+f] }); err != nil {
		return nil, false, err
	}
	if m.Sanitized, err = metaFromFields(func(f string) string { return vals["san_"+f] }); err != nil {
		return nil, false, err
	}
	// compressed variants: cv:{variant}:{field}
	for k := range vals {
		const p = "cv:"
		if len(k) > len(p) && k[:len(p)] == p {
			rest := k[len(p):]
			if i := indexByte(rest, ':'); i > 0 {
				variant := rest[:i]
				if _, seen := m.CompressedVariants[variant]; !seen {
					meta, err := metaFromFields(func(f string) string { return vals[cvField(variant, f)] })
					if err != nil {
						return nil, false, err
					}
					m.CompressedVariants[variant] = meta
				}
			}
		}
	}
	return m, true, nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// GetManifest implements SessionArtifactStore.
func (s *RedisArtifactStore) GetManifest(ctx context.Context, tenantID, sessionID string) (*ArtifactManifest, error) {
	if tenantID == "" || sessionID == "" {
		return nil, fmt.Errorf("preprocess: tenant/session required")
	}
	m, _, err := s.readManifest(ctx, tenantID, sessionID)
	return m, err
}

// revision triple helpers -----------------------------------------------------

func revTuple(r SessionRevision) (string, string, string) {
	return strconv.FormatInt(int64(r.TurnNo), 10), r.HeadRequestID, hex32(r.ChainHash)
}

func revTupleKeep() (string, string, string) { return "", "", "" }

// runCAS executes the CAS script.  mfields/pfields use "" value for HDEL.
// Returns 1 ok / 0 conflict / 2 idempotent replay.
func (s *RedisArtifactStore) runCAS(ctx context.Context, keys []string, expected SessionRevision,
	newRev SessionRevision, hasNewRev bool, flags ArtifactFlags, manifestTTL time.Duration,
	mfields [][2]string, pfields [][2]string, payloadTTL time.Duration) (int, error) {

	expTurn, expHead, expChain := revTuple(expected)
	newTurn, newHead, newChain := revTupleKeep()
	if hasNewRev {
		newTurn, newHead, newChain = revTuple(newRev)
	}
	args := make([]interface{}, 0, 16+2*(len(mfields)+len(pfields)))
	args = append(args, expTurn, expHead, expChain, newTurn, newHead, newChain,
		strconv.FormatUint(uint64(flags), 10), strconv.FormatInt(manifestTTL.Milliseconds(), 10),
		strconv.Itoa(len(mfields)))
	for _, f := range mfields {
		args = append(args, f[0], f[1])
	}
	args = append(args, strconv.Itoa(len(pfields)))
	for _, f := range pfields {
		args = append(args, f[0], f[1])
	}
	args = append(args, strconv.FormatInt(payloadTTL.Milliseconds(), 10))

	res := casWriteScript.Run(ctx, s.opts.Client, keys, args...)
	n, err := res.Int()
	if err != nil {
		return 0, fmt.Errorf("preprocess: cas script: %w", err)
	}
	return n, nil
}

// Get implements SessionArtifactStore.
func (s *RedisArtifactStore) Get(ctx context.Context, tenantID, sessionID string, kind ArtifactKind, variant string) (*SessionArtifact, bool, error) {
	if tenantID == "" || sessionID == "" || !kind.Valid() {
		return nil, false, fmt.Errorf("preprocess: invalid get arguments")
	}
	variant = variantOrDefault(variant)
	key := s.artifactKey(tenantID, sessionID, kind, variant)

	metaFields := []string{fieldEnc, "content_hash", "dep_hash", "source_hash", "schema_version", "generated_at", fieldTurnNo, fieldHead, fieldChain, "tokens"}
	raw, err := s.opts.Client.HMGet(ctx, key, metaFields...).Result()
	if err != nil {
		return nil, false, fmt.Errorf("preprocess: hmget artifact: %w", err)
	}
	if raw[0] == nil {
		return nil, false, nil
	}
	str := func(i int) string {
		if raw[i] == nil {
			return ""
		}
		if s, ok := raw[i].(string); ok {
			return s
		}
		return fmt.Sprintf("%v", raw[i])
	}
	art := &SessionArtifact{
		TenantID:  tenantID,
		SessionID: sessionID,
		Kind:      kind,
		Variant:   variant,
	}
	contentHash, err := parseHash32(str(1))
	if err != nil {
		return nil, false, err
	}
	art.ContentHash = contentHash
	if art.DependencyHash, err = parseHash32(str(2)); err != nil {
		return nil, false, err
	}
	if art.SourceHash, err = parseHash32(str(3)); err != nil {
		return nil, false, err
	}
	if v, err := strconv.ParseUint(str(4), 10, 16); err == nil {
		art.SchemaVersion = uint16(v)
	}
	if v, err := strconv.ParseInt(str(5), 10, 64); err == nil {
		art.GeneratedAt = v
	}
	if v, err := strconv.ParseInt(str(6), 10, 32); err == nil {
		art.Revision.TurnNo = int32(v)
	}
	art.Revision.HeadRequestID = str(7)
	if ch, err := parseHash32(str(8)); err != nil {
		return nil, false, err
	} else {
		art.Revision.ChainHash = ch
	}
	if v, err := strconv.ParseInt(str(9), 10, 32); err == nil {
		art.Tokens = int32(v)
	}

	l1Key := l1CacheKey(tenantID, sessionID, kind, variant, contentHash)
	if b, ok := s.l1.Get(l1Key); ok {
		art.Payload = b // already a deep copy from the LRU
		return art, true, nil
	}

	payload, err := s.opts.Client.HGet(ctx, key, fieldPayload).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("preprocess: hget payload: %w", err)
	}
	plain, err := s.decodePayload(tenantID, sessionID, kind, str(0), payload)
	if err != nil {
		return nil, false, err
	}
	if HashBytes(plain) != contentHash {
		return nil, false, fmt.Errorf("%w: content hash mismatch for %s", ErrArtifactCorrupted, kind)
	}
	s.l1.Put(l1Key, plain)
	art.Payload = cloneBytes(plain)
	return art, true, nil
}

func l1CacheKey(tenantID, sessionID string, kind ArtifactKind, variant string, contentHash [32]byte) string {
	return tenantID + "|" + sessionID + "|" + string(kind) + "|" + variant + "|" + hex32(contentHash)
}

// PutCAS implements SessionArtifactStore.
func (s *RedisArtifactStore) PutCAS(ctx context.Context, a *SessionArtifact, expected SessionRevision) (bool, error) {
	if a == nil {
		return false, fmt.Errorf("preprocess: nil artifact")
	}
	if a.TenantID == "" || a.SessionID == "" || !a.Kind.Valid() || a.Payload == nil {
		return false, fmt.Errorf("preprocess: invalid artifact for PutCAS")
	}
	if isZeroHash(a.ContentHash) {
		a.ContentHash = HashBytes(a.Payload)
	}
	if a.GeneratedAt == 0 {
		a.GeneratedAt = s.opts.now().UnixMilli()
	}
	variant := variantOrDefault(a.Variant)
	a.Variant = variant

	cur, _, err := s.readManifest(ctx, a.TenantID, a.SessionID)
	if err != nil {
		return false, err
	}
	flags := cur.Flags
	flags &^= StaleFlag(a.Kind) | BuildingFlag(a.Kind)
	flags |= ReadyFlag(a.Kind)

	meta := ArtifactMeta{
		Status:         StatusReady,
		SourceHash:     a.SourceHash,
		DependencyHash: a.DependencyHash,
		ContentHash:    a.ContentHash,
		SchemaVersion:  a.SchemaVersion,
		TransformVer:   a.TransformVer,
		GeneratedAt:    a.GeneratedAt,
		FailureCode:    a.FailureCode,
	}
	prefix, sep := metaFieldPrefix(a.Kind, variant)
	mfields := metaToFields(prefix, sep, meta)

	sealed, encoding, err := s.encodePayload(a.TenantID, a.SessionID, a.Kind, a.Payload)
	if err != nil {
		return false, err
	}
	tTurn, tHead, tChain := revTuple(a.Revision)
	pfields := [][2]string{
		{fieldPayload, string(sealed)},
		{fieldEnc, encoding},
		{"content_hash", hex32(a.ContentHash)},
		{"dep_hash", hex32(a.DependencyHash)},
		{"source_hash", hex32(a.SourceHash)},
		{"schema_version", strconv.Itoa(int(a.SchemaVersion))},
		{"generated_at", strconv.FormatInt(a.GeneratedAt, 10)},
		{fieldTurnNo, tTurn},
		{fieldHead, tHead},
		{fieldChain, tChain},
		{"tokens", strconv.Itoa(int(a.Tokens))},
	}

	mkey := s.manifestKey(a.TenantID, a.SessionID)
	pkey := s.artifactKey(a.TenantID, a.SessionID, a.Kind, variant)
	res, err := s.runCAS(ctx, []string{mkey, pkey}, expected, SessionRevision{}, false, flags,
		s.opts.manifestTTL(), mfields, pfields, s.opts.ttlFor(a.Kind))
	if err != nil {
		return false, err
	}
	if res != 1 {
		return false, nil
	}
	s.l1.Put(l1CacheKey(a.TenantID, a.SessionID, a.Kind, variant, a.ContentHash), a.Payload)
	return true, nil
}

// AppendRawTurn implements SessionArtifactStore.
func (s *RedisArtifactStore) AppendRawTurn(ctx context.Context, mutation SessionMutation, expected SessionRevision) (SessionRevision, error) {
	if err := mutation.Validate(); err != nil {
		return SessionRevision{}, err
	}
	if mutation.Delta != nil && HashBytes(mutation.Delta) != mutation.DeltaHash {
		return SessionRevision{}, fmt.Errorf("%w: delta hash does not match payload", ErrInvalidMutation)
	}
	// append_delta must follow the expected revision's turn (0 = derive
	// automatically).  Replays with a stale expected still pass this check for
	// their original bundle and are resolved by the CAS/idempotent path.
	if mutation.Mutation == MutationAppendDelta && mutation.TurnNo != 0 && mutation.TurnNo != expected.TurnNo+1 {
		return SessionRevision{}, fmt.Errorf("%w: append_delta turn %d does not follow revision turn %d",
			ErrInvalidMutation, mutation.TurnNo, expected.TurnNo)
	}
	var next SessionRevision
	zeroChain := SessionRevision{}.ChainHash
	switch mutation.Mutation {
	case MutationAppendDelta, MutationAttachmentOnly:
		turn := expected.TurnNo + 1
		if mutation.Mutation == MutationAttachmentOnly {
			turn = expected.TurnNo // attachments do not create conversational turns
		}
		next = SessionRevision{
			TurnNo:        turn,
			HeadRequestID: mutation.RequestID,
			ChainHash:     AdvanceChainHash(expected.ChainHash, mutation.DeltaHash),
		}
	case MutationReplaceSnapshot:
		next = SessionRevision{
			TurnNo:        mutation.TurnNo,
			HeadRequestID: mutation.RequestID,
			ChainHash:     AdvanceChainHash(expected.ChainHash, mutation.DeltaHash),
		}
	case MutationReset:
		next = SessionRevision{
			TurnNo:        mutation.TurnNo,
			HeadRequestID: mutation.RequestID,
			ChainHash:     AdvanceChainHash(zeroChain, mutation.DeltaHash),
		}
	}

	cur, _, err := s.readManifest(ctx, mutation.TenantID, mutation.SessionID)
	if err != nil {
		return SessionRevision{}, err
	}

	// Raw gets the appended content (ready at the new revision); downstream
	// layers are marked stale (R11.6 step 4) — payloads are NOT deleted.
	flags := cur.Flags
	flags &^= FlagRawStale | FlagRawBuilding | FlagSanitizeDegraded
	flags |= FlagRawReady
	flags &^= FlagSanitizedReady | FlagSanitizedBuilding
	flags |= FlagSanitizedStale
	flags &^= FlagCompressedReady | FlagCompressedBuilding
	flags |= FlagCompressedStale

	rawMeta := ArtifactMeta{
		Status:         StatusReady,
		SourceHash:     next.ChainHash,
		DependencyHash: mutation.DependencyHash,
		ContentHash:    mutation.DeltaHash,
		SchemaVersion:  mutation.SchemaVersion,
		GeneratedAt:    s.opts.now().UnixMilli(),
	}
	mfields := metaToFields("raw", "_", rawMeta)
	mfields = append(mfields, [2]string{"san_status", strconv.Itoa(int(StatusStale))})
	for variant := range cur.CompressedVariants {
		mfields = append(mfields, [2]string{cvField(variant, "status"), strconv.Itoa(int(StatusStale))})
	}

	var pfields [][2]string
	payloadTTL := s.opts.ttlFor(ArtifactRaw)
	if mutation.Delta != nil {
		sealed, err := sealAESGCM(s.opts.AESKey, mutation.Delta, aad(mutation.TenantID, mutation.SessionID, ArtifactRaw))
		if err != nil {
			return SessionRevision{}, fmt.Errorf("preprocess: seal raw delta: %w", err)
		}
		tTurn, tHead, tChain := revTuple(next)
		pfields = [][2]string{
			{fieldPayload, string(sealed)},
			{fieldEnc, encodingAESGCM},
			{"content_hash", hex32(mutation.DeltaHash)},
			{"dep_hash", hex32(mutation.DependencyHash)},
			{"source_hash", hex32(next.ChainHash)},
			{"schema_version", strconv.Itoa(int(mutation.SchemaVersion))},
			{"generated_at", strconv.FormatInt(rawMeta.GeneratedAt, 10)},
			{fieldTurnNo, tTurn},
			{fieldHead, tHead},
			{fieldChain, tChain},
		}
	}

	mkey := s.manifestKey(mutation.TenantID, mutation.SessionID)
	keys := []string{mkey}
	if len(pfields) > 0 {
		keys = append(keys, s.artifactKey(mutation.TenantID, mutation.SessionID, ArtifactRaw, "default"))
	}
	res, err := s.runCAS(ctx, keys, expected, next, true, flags, s.opts.manifestTTL(), mfields, pfields, payloadTTL)
	if err != nil {
		return SessionRevision{}, err
	}
	switch res {
	case 1:
		return next, nil
	case 2:
		// idempotent replay: the same request already committed this turn.
		m, _, err := s.readManifest(ctx, mutation.TenantID, mutation.SessionID)
		if err != nil {
			return SessionRevision{}, err
		}
		return m.Revision, nil
	default:
		return SessionRevision{}, fmt.Errorf("%w: manifest at %s, expected %s", ErrRevisionConflict, cur.Revision, expected)
	}
}

// InvalidateFrom implements SessionArtifactStore.
func (s *RedisArtifactStore) InvalidateFrom(ctx context.Context, tenantID, sessionID string, kind ArtifactKind) error {
	if tenantID == "" || sessionID == "" || !kind.Valid() {
		return fmt.Errorf("preprocess: invalid invalidate arguments")
	}
	affected := append([]ArtifactKind{kind}, kind.DownstreamOf()...)
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		cur, exists, err := s.readManifest(ctx, tenantID, sessionID)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		flags := cur.Flags
		var mfields [][2]string
		for _, k := range affected {
			flags &^= ReadyFlag(k) | BuildingFlag(k)
			flags |= StaleFlag(k)
			switch k {
			case ArtifactRaw:
				mfields = append(mfields, [2]string{"raw_status", strconv.Itoa(int(StatusStale))})
			case ArtifactSanitized:
				mfields = append(mfields, [2]string{"san_status", strconv.Itoa(int(StatusStale))})
				flags &^= FlagSanitizeDegraded
			case ArtifactCompressed:
				for variant := range cur.CompressedVariants {
					mfields = append(mfields, [2]string{cvField(variant, "status"), strconv.Itoa(int(StatusStale))})
				}
			}
		}
		res, err := s.runCAS(ctx, []string{s.manifestKey(tenantID, sessionID)}, cur.Revision, SessionRevision{}, false,
			flags, s.opts.manifestTTL(), mfields, nil, 0)
		if err != nil {
			return err
		}
		if res == 1 || res == 2 {
			return nil
		}
		// res == 0: revision moved; reload and retry (never overwrite).
	}
	return fmt.Errorf("%w: invalidatefrom retry budget exhausted", ErrRevisionConflict)
}

// TryMarkTerminalAppend implements the optional terminalMarkerStore
// capability: an exactly-once marker per (session, request) so replayed
// AppendTerminal calls (client resend / cross-node replay) do not append a
// second assistant turn.  The marker lives for the manifest TTL.
func (s *RedisArtifactStore) TryMarkTerminalAppend(ctx context.Context, tenantID, sessionID, requestID string, ttl time.Duration) (bool, error) {
	if tenantID == "" || sessionID == "" || requestID == "" {
		return false, fmt.Errorf("preprocess: invalid terminal marker arguments")
	}
	if ttl <= 0 {
		ttl = s.opts.manifestTTL()
	}
	key := fmt.Sprintf("session:artifact:%s:%s:%s:terminal:%s",
		s.opts.keyVersion(), hash16(tenantID), hash16(sessionID), requestID)
	ok, err := s.opts.Client.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("preprocess: setnx terminal marker: %w", err)
	}
	return ok, nil
}

// AcquireBuildLease implements SessionArtifactStore.
func (s *RedisArtifactStore) AcquireBuildLease(ctx context.Context, tenantID, sessionID string, kind ArtifactKind, depHash [32]byte) (Lease, bool, error) {
	if tenantID == "" || sessionID == "" || !kind.Valid() {
		return nil, false, fmt.Errorf("preprocess: invalid lease arguments")
	}
	token := make([]byte, defaultLeaseTokenB)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return nil, false, fmt.Errorf("preprocess: lease token: %w", err)
	}
	key := s.leaseKey(tenantID, sessionID, kind, depHash)
	ok, err := s.opts.Client.SetNX(ctx, key, hex.EncodeToString(token), s.opts.leaseTTL()).Result()
	if err != nil {
		return nil, false, fmt.Errorf("preprocess: setnx lease: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	return &redisLease{
		client: s.opts.Client,
		key:    key,
		token:  hex.EncodeToString(token),
		kind:   kind,
	}, true, nil
}

type redisLease struct {
	client redis.UniversalClient
	key    string
	token  string
	kind   ArtifactKind
}

func (l *redisLease) Token() string      { return l.token }
func (l *redisLease) Kind() ArtifactKind { return l.kind }
func (l *redisLease) Release(ctx context.Context) error {
	return releaseLeaseScript.Run(ctx, l.client, []string{l.key}, l.token).Err()
}
