package middleware

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

const (
	// MaxClockSkew is the maximum allowed time difference between client and server
	MaxClockSkew = 300 * time.Second

	// NonceCleanupInterval is how often we clean expired nonces
	NonceCleanupInterval = 1 * time.Minute

	// NonceTTL is how long we keep nonces in the replay prevention map
	NonceTTL          = 5 * time.Minute
	MaxSignedBodySize = 4 << 20
)

// nonceEntry tracks when a nonce was seen
type nonceEntry struct {
	timestamp time.Time
}

// nonceStore manages nonce replay prevention
type nonceStore struct {
	mu     sync.Mutex
	nonces map[string]nonceEntry
}

func newNonceStore() *nonceStore {
	store := &nonceStore{
		nonces: make(map[string]nonceEntry),
	}
	go store.cleanup()
	return store
}

// check returns true if nonce is new (not seen before)
func (ns *nonceStore) check(nonce string) bool {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	if _, exists := ns.nonces[nonce]; exists {
		return false
	}

	ns.nonces[nonce] = nonceEntry{timestamp: time.Now()}
	return true
}

// cleanup periodically removes expired nonces
func (ns *nonceStore) cleanup() {
	ticker := time.NewTicker(NonceCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		ns.mu.Lock()
		cutoff := time.Now().Add(-NonceTTL)
		for nonce, entry := range ns.nonces {
			if entry.timestamp.Before(cutoff) {
				delete(ns.nonces, nonce)
			}
		}
		ns.mu.Unlock()
	}
}

// ClientPublicKeyLookup is a function that returns the public key for a given instance ID
type ClientPublicKeyLookup func(instanceID string) (ed25519.PublicKey, error)

// NonceChecker is an interface for checking nonce replay
type NonceChecker interface {
	check(nonce string) bool
}

// SignatureVerifier creates an Echo middleware that verifies Ed25519 signatures
// on incoming requests to prevent tampering and replay attacks.
// Uses in-memory nonce store by default.
func SignatureVerifier(clientPublicKeyLookup ClientPublicKeyLookup) echo.MiddlewareFunc {
	store := newNonceStore()

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract required headers
			instanceID := c.Request().Header.Get("X-Instance-ID")
			if instanceID == "" || len(instanceID) > 256 {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing X-Instance-ID header")
			}

			timestampStr := c.Request().Header.Get("X-Timestamp")
			if timestampStr == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing X-Timestamp header")
			}

			nonce := c.Request().Header.Get("X-Nonce")
			if nonce == "" || len(nonce) > 256 {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing X-Nonce header")
			}

			signatureB64 := c.Request().Header.Get("X-Signature")
			if signatureB64 == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing X-Signature header")
			}

			// Parse timestamp
			timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Timestamp format")
			}

			// Check clock skew
			now := time.Now().Unix()
			diff := now - timestamp
			if diff < 0 {
				diff = -diff
			}
			if time.Duration(diff)*time.Second > MaxClockSkew {
				return echo.NewHTTPError(http.StatusUnauthorized, "timestamp outside acceptable range")
			}

			// Read request body
			body, err := io.ReadAll(io.LimitReader(c.Request().Body, MaxSignedBodySize+1))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
			}
			if len(body) > MaxSignedBodySize {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
			}
			// Restore body for downstream handlers
			c.Request().Body = io.NopCloser(bytes.NewReader(body))

			// Construct message: timestamp:nonce:body
			message := fmt.Sprintf("%s:%s:%s", timestampStr, nonce, string(body))

			// Decode signature
			signature, err := base64.StdEncoding.DecodeString(signatureB64)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid signature encoding")
			}

			// Lookup client public key
			publicKey, err := clientPublicKeyLookup(instanceID)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, fmt.Sprintf("unknown instance ID: %v", err))
			}

			// Verify signature
			if !ed25519.Verify(publicKey, []byte(message), signature) {
				return echo.NewHTTPError(http.StatusUnauthorized, "signature verification failed")
			}
			if !store.check(instanceID + ":" + nonce) {
				return echo.NewHTTPError(http.StatusUnauthorized, "nonce already used")
			}

			// Signature valid, proceed
			return next(c)
		}
	}
}

// SignatureVerifierWithRedis creates an Echo middleware that verifies Ed25519 signatures
// using Redis for distributed nonce replay prevention.
func SignatureVerifierWithRedis(clientPublicKeyLookup ClientPublicKeyLookup, redisClient *redis.Client) echo.MiddlewareFunc {
	store := newRedisNonceStore(redisClient)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract required headers
			instanceID := c.Request().Header.Get("X-Instance-ID")
			if instanceID == "" || len(instanceID) > 256 {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing X-Instance-ID header")
			}

			timestampStr := c.Request().Header.Get("X-Timestamp")
			if timestampStr == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing X-Timestamp header")
			}

			nonce := c.Request().Header.Get("X-Nonce")
			if nonce == "" || len(nonce) > 256 {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing X-Nonce header")
			}

			signatureB64 := c.Request().Header.Get("X-Signature")
			if signatureB64 == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing X-Signature header")
			}

			// Parse timestamp
			timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Timestamp format")
			}

			// Check clock skew
			now := time.Now().Unix()
			diff := now - timestamp
			if diff < 0 {
				diff = -diff
			}
			if time.Duration(diff)*time.Second > MaxClockSkew {
				return echo.NewHTTPError(http.StatusUnauthorized, "timestamp outside acceptable range")
			}

			// Read request body
			body, err := io.ReadAll(io.LimitReader(c.Request().Body, MaxSignedBodySize+1))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
			}
			if len(body) > MaxSignedBodySize {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
			}
			// Restore body for downstream handlers
			c.Request().Body = io.NopCloser(bytes.NewReader(body))

			// Construct message: timestamp:nonce:body
			message := fmt.Sprintf("%s:%s:%s", timestampStr, nonce, string(body))

			// Decode signature
			signature, err := base64.StdEncoding.DecodeString(signatureB64)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid signature encoding")
			}

			// Lookup client public key
			publicKey, err := clientPublicKeyLookup(instanceID)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, fmt.Sprintf("unknown instance ID: %v", err))
			}

			// Verify signature
			if !ed25519.Verify(publicKey, []byte(message), signature) {
				return echo.NewHTTPError(http.StatusUnauthorized, "signature verification failed")
			}
			ok, err := store.check(c.Request().Context(), instanceID+":"+nonce)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "nonce check failed")
			}
			if !ok {
				return echo.NewHTTPError(http.StatusForbidden, "replay attack detected: nonce already used")
			}

			// Signature valid, proceed
			return next(c)
		}
	}
}
