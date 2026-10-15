package compression

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// SessionCacheScriptBackend is required for shared writes. A configured Redis
// backend without it fails explicitly; it never silently becomes a local cache.
type SessionCacheScriptBackend interface {
	Eval(context.Context, string, []string, ...any) (any, error)
}

const (
	sharedLeaseTTL            = 30 * time.Second
	sharedLeaseCleanupTimeout = 500 * time.Millisecond
	sharedBodyLimit           = 128 << 20 // streaming.maxBodySize; configurable for larger deployments
	sharedStateLimit          = 512 << 20 // Redis' default maximum bulk-string size, not a new small state cap
	sharedAcquireScript       = `-- session-cache-acquire
if type(redis.acl_check_cmd) ~= 'function' then return redis.error_reply('session cache requires Redis 7.0 acl_check_cmd') end
if not redis.acl_check_cmd('SET',KEYS[1],ARGV[1],'NX','PX',ARGV[2]) or not redis.acl_check_cmd('GET',KEYS[1]) or not redis.acl_check_cmd('DEL',KEYS[1]) then return redis.error_reply('NOPERM session cache lease commands denied') end
return redis.call('SET',KEYS[1],ARGV[1],'NX','PX',ARGV[2]) and 1 or 0`
	sharedReleaseScript = `-- session-cache-release
if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) end
return 0`
	sharedInvalidateScript = `-- session-cache-invalidate
if type(redis.acl_check_cmd) ~= 'function' then return redis.error_reply('session cache requires Redis 7.0 acl_check_cmd') end
if redis.call('GET',KEYS[3]) ~= ARGV[1] then return 0 end
-- A single DEL retires both namespaces without a partial deletion on error.
if not redis.acl_check_cmd('DEL',KEYS[1],KEYS[2]) then return redis.error_reply('NOPERM session cache invalidate commands denied') end
redis.call('DEL',KEYS[1],KEYS[2])
return 1`
	sharedCommitScript = `-- session-cache-commit
if type(redis.acl_check_cmd) ~= 'function' then return redis.error_reply('session cache requires Redis 7.0 acl_check_cmd') end
if redis.call('GET',KEYS[2]) ~= ARGV[1] then return 0 end
local kind=redis.call('TYPE',KEYS[1]).ok
if kind ~= 'none' and kind ~= 'hash' then return redis.error_reply('WRONGTYPE session cache requires hash') end
local ttl=tonumber(ARGV[2])
if not ttl or ttl <= 0 then return redis.error_reply('invalid session cache TTL') end
-- Lua runtime errors do not roll back prior writes. Check every mutation,
-- with its actual arguments, before the first destructive command.
if not redis.acl_check_cmd('DEL',KEYS[1]) or not redis.acl_check_cmd('HSET',KEYS[1],unpack(ARGV,3)) or not redis.acl_check_cmd('PEXPIRE',KEYS[1],ttl) then return redis.error_reply('NOPERM session cache commit commands denied') end
redis.call('DEL',KEYS[1])
redis.call('HSET',KEYS[1],unpack(ARGV,3))
redis.call('PEXPIRE',KEYS[1],ttl)
return 1`
)

var ErrSessionCacheLeaseLost = errors.New("session cache lease lost before commit")

func legacyRedisKey(tenantID, sessionID string) string {
	return "session:sc:" + tenantID + ":" + sessionID + ":v1"
}
func sharedLimit(env string, fallback int) (int, error) {
	raw := os.Getenv(env)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > 512<<20 {
		return 0, fmt.Errorf("%s must be between 1 and 536870912 bytes", env)
	}
	return int(value), nil
}
func sharedToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("session cache token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}
func (c *SessionCache) scriptBackend() (SessionCacheScriptBackend, error) {
	backend, ok := c.redis.(SessionCacheScriptBackend)
	if !ok {
		return nil, errors.New("session cache Redis backend lacks atomic script support")
	}
	return backend, nil
}

// withSharedLease invokes work once. It never retries a transform after lease
// loss. Cleanup uses its own bounded context even after caller cancellation;
// compare-token prevents deleting a successor's lease. Failed cleanup leaves
// only the expiring lease and never changes the primary result.
func (c *SessionCache) withSharedLease(ctx context.Context, tenantID, sessionID string, work func(string) error) (err error) {
	backend, err := c.scriptBackend()
	if err != nil {
		return err
	}
	token, err := sharedToken()
	if err != nil {
		return err
	}
	lockKey := redisKey(tenantID, sessionID) + ":lease"
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		var result any
		result, err = backend.Eval(ctx, sharedAcquireScript, []string{lockKey}, token, sharedLeaseTTL.Milliseconds())
		if err != nil {
			return fmt.Errorf("acquire session cache lease: %w", err)
		}
		if result == int64(1) {
			break
		}
		if result != int64(0) {
			return fmt.Errorf("invalid session cache acquire result: %T", result)
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), sharedLeaseCleanupTimeout)
		defer cancel()
		_, releaseErr := backend.Eval(cleanup, sharedReleaseScript, []string{lockKey}, token)
		if releaseErr != nil {
			slog.WarnContext(cleanup, "session_cache: lease cleanup failed", "session", sessionID, "error", releaseErr, "primary_failed", err != nil)
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	return work(token)
}

func (c *SessionCache) invalidateShared(ctx context.Context, tenantID, sessionID string, token string) error {
	backend, err := c.scriptBackend()
	if err != nil {
		return err
	}
	key := redisKey(tenantID, sessionID)
	result, err := backend.Eval(ctx, sharedInvalidateScript, []string{legacyRedisKey(tenantID, sessionID), key, key + ":lease"}, token)
	if err != nil {
		return fmt.Errorf("invalidate shared session cache: %w", err)
	}
	if result == int64(0) {
		return ErrSessionCacheLeaseLost
	}
	if result != int64(1) {
		return fmt.Errorf("invalid session cache invalidate result: %T", result)
	}
	return nil
}

func (c *SessionCache) readShared(ctx context.Context, tenantID, sessionID string) (*SessionState, []byte, string, error) {
	fields, err := c.redis.HGetAll(ctx, redisKey(tenantID, sessionID))
	if err != nil {
		return nil, nil, "", fmt.Errorf("read session cache: %w", err)
	}
	if len(fields) == 0 {
		return nil, nil, "", nil
	}
	fail := func(reason string) (*SessionState, []byte, string, error) {
		return nil, nil, "", fmt.Errorf("invalid shared session cache pair: %s", reason)
	}
	if fields["pair_v"] != "2" || len(fields["revision"]) != 32 {
		return fail("version or revision")
	}
	if _, err := hex.DecodeString(fields["revision"]); err != nil {
		return fail("revision encoding")
	}
	stateLimit, err := sharedLimit("LLM_GATEWAY_SESSION_CACHE_MAX_STATE_BYTES", sharedStateLimit)
	if err != nil {
		return nil, nil, "", err
	}
	stateJSON := fields["state"]
	if len(stateJSON) > stateLimit {
		return fail("state exceeds limit")
	}
	if sha256Hex([]byte(stateJSON)) != fields["state_hash"] {
		return fail("state hash")
	}
	var state SessionState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return fail("state JSON")
	}
	if state.SchemaVersion != schemaVersion {
		return fail("state schema")
	}
	bodyLimit, err := sharedLimit("LLM_GATEWAY_SESSION_CACHE_MAX_BODY_BYTES", sharedBodyLimit)
	if err != nil {
		return nil, nil, "", err
	}
	size, err := strconv.ParseInt(fields["body_size"], 10, 64)
	if err != nil || size < 0 || size > int64(bodyLimit) {
		return fail("body size")
	}
	if len(fields["body"]) > bodyLimit {
		return fail("encoded body exceeds limit")
	}
	var body []byte
	switch fields["body_codec"] {
	case "raw":
		body = []byte(fields["body"])
	case "gzip":
		reader, err := gzip.NewReader(strings.NewReader(fields["body"]))
		if err != nil {
			return fail("gzip header")
		}
		body, err = io.ReadAll(io.LimitReader(reader, int64(bodyLimit)+1))
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return fail("gzip payload")
		}
	default:
		return fail("body codec")
	}
	if len(body) > bodyLimit || int64(len(body)) != size {
		return fail("decoded body size")
	}
	hash := sha256Hex(body)
	if hash != fields["body_hash"] || state.LastOutboundHash != hash {
		return fail("body hash")
	}
	if len(body) == 0 {
		body = nil
	}
	return &state, body, fields["revision"], nil
}

func (c *SessionCache) commitShared(ctx context.Context, tenantID, sessionID, token string, state *SessionState, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bodyLimit, err := sharedLimit("LLM_GATEWAY_SESSION_CACHE_MAX_BODY_BYTES", sharedBodyLimit)
	if err != nil {
		return err
	}
	if len(body) > bodyLimit {
		return fmt.Errorf("session cache body exceeds %d bytes", bodyLimit)
	}
	state = cloneSessionState(state)
	if state.SchemaVersion == 0 {
		state.SchemaVersion = schemaVersion
	}
	if state.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported session cache state schema: %d", state.SchemaVersion)
	}
	state.LastOutboundHash = sha256Hex(body)
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode session cache state: %w", err)
	}
	stateLimit, err := sharedLimit("LLM_GATEWAY_SESSION_CACHE_MAX_STATE_BYTES", sharedStateLimit)
	if err != nil {
		return err
	}
	if len(stateJSON) > stateLimit {
		return fmt.Errorf("session cache state exceeds %d bytes", stateLimit)
	}
	encoded, codec := body, "raw"
	if len(body) > 1024 {
		var buf bytes.Buffer
		writer := gzip.NewWriter(&buf)
		if _, err := writer.Write(body); err != nil {
			return fmt.Errorf("compress session cache body: %w", err)
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("compress session cache body: %w", err)
		}
		if buf.Len() < len(body) {
			encoded, codec = buf.Bytes(), "gzip"
		}
	}
	revision, err := sharedToken()
	if err != nil {
		return err
	}
	backend, err := c.scriptBackend()
	if err != nil {
		return err
	}
	runThreeTierCheck(ctx, tenantID, sessionID, state)
	args := []any{token, sessionCacheRedisTTL().Milliseconds(), "pair_v", "2", "revision", revision,
		"state", string(stateJSON), "state_hash", sha256Hex(stateJSON), "body", string(encoded),
		"body_codec", codec, "body_size", strconv.Itoa(len(body)), "body_hash", state.LastOutboundHash}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := backend.Eval(ctx, sharedCommitScript, []string{redisKey(tenantID, sessionID), redisKey(tenantID, sessionID) + ":lease"}, args...)
	if err != nil {
		return fmt.Errorf("commit session cache pair: %w", err)
	}
	if result == int64(0) {
		return ErrSessionCacheLeaseLost
	}
	if result != int64(1) {
		return fmt.Errorf("invalid session cache commit result: %T", result)
	}
	c.setL1Revision(l1Key(tenantID, sessionID), state, body, revision)
	return nil
}

// loadSharedLocked rechecks the authority after acquiring the lease. DB
// backfill and legacy imports cannot overwrite a writer that won that lease.
func (c *SessionCache) loadSharedLocked(ctx context.Context, tenantID, sessionID, token string) (*SessionState, []byte, error) {
	st, body, rev, err := c.readShared(ctx, tenantID, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if st != nil {
		c.setL1Revision(l1Key(tenantID, sessionID), st, body, rev)
		return st, body, nil
	}
	legacy, err := c.redis.HGetAll(ctx, legacyRedisKey(tenantID, sessionID))
	if err != nil {
		return nil, nil, fmt.Errorf("read legacy session cache: %w", err)
	}
	if len(legacy) > 0 {
		var old SessionState
		if err := decodeSessionStateFields(legacy, &old); err != nil {
			return nil, nil, err
		}
		if old.SchemaVersion == schemaVersion {
			if old.LastOutboundHash == "" && old.MsgCount == 0 {
				st = &old
			} else {
				// Only the exact original envelope proves association. Equal message
				// counts or a rewrapped V2 message array cannot establish provenance.
				if c.db != nil {
					_, body, err = c.loadFromLegacyDB(ctx, tenantID, sessionID)
					if err != nil {
						return nil, nil, err
					}
				}
				if len(body) == 0 || sha256Hex(body) != old.LastOutboundHash {
					return nil, nil, errors.New("legacy session cache has no exact matching outbound body")
				}
				st = &old
			}
		}
	}
	if st == nil && (c.turnReader != nil || c.db != nil) {
		st, body, err = c.loadFromDB(ctx, tenantID, sessionID)
		if err != nil {
			return nil, nil, err
		}
	}
	if st == nil {
		return nil, nil, nil
	}
	if err := c.commitShared(ctx, tenantID, sessionID, token, st, body); err != nil {
		return nil, nil, err
	}
	st = cloneSessionState(st)
	st.LastOutboundHash = sha256Hex(body)
	return st, body, nil
}

func (c *SessionCache) getShared(ctx context.Context, tenantID, sessionID string) (st *SessionState, body []byte, err error) {
	st, body, rev, err := c.readShared(ctx, tenantID, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if st != nil {
		// A revision is immutable. Redis was read and validated before L1 reuse;
		// process-local state can never hide a shared failure or a newer revision.
		key := l1Key(tenantID, sessionID)
		c.mu.Lock()
		entry := c.l1[key]
		if entry != nil && entry.revision == rev {
			c.ll.MoveToFront(entry.elem)
			st, body = cloneSessionState(entry.state), append([]byte(nil), entry.body...)
			c.mu.Unlock()
			return st, body, nil
		}
		c.mu.Unlock()
		c.setL1Revision(key, st, body, rev)
		return st, body, nil
	}
	err = c.withSharedLease(ctx, tenantID, sessionID, func(token string) error {
		var loadErr error
		st, body, loadErr = c.loadSharedLocked(ctx, tenantID, sessionID, token)
		return loadErr
	})
	if err != nil {
		return nil, nil, err
	}
	return st, body, nil
}
