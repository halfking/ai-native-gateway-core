package secret

import "errors"

// Credential reveal-path sentinels. These classify failures along the
// fetchReveal / RevealAPIKey path (DB lookup, keyring, decryption, negative
// cache) so any caller — provider package metrics, admin tooling, future
// services — can route via errors.Is rather than string matching.
//
// Adding a new sentinel here is a contract change: the closed reason
// vocabulary in provider/credential_decrypt_metrics.go must list every
// constant in this block.
//
// History: previously declared as package-local errReveal* in
// provider/credential_decrypt_metrics.go and promoted here as part of the
// 2026-08-18 credential-17 cleanup so the secret package is the single
// authority for decryption-related error classification.
var (
	ErrRevealUnknownFormat = errors.New("credential reveal: decrypt unknown format")
	ErrRevealDecrypt       = errors.New("credential reveal: decrypt failed")
	ErrRevealNotFound      = errors.New("credential reveal: not found or disabled")
	ErrRevealNotConfigured = errors.New("credential reveal: not configured")
	ErrRevealRotation      = errors.New("credential reveal: rotation invalidated")
	// ErrRevealCached wraps a previously cached failure with its original
	// cause so classifyRevealFailure can surface the incident signature
	// (e.g. unknown_format) rather than masking it as "cached".
	ErrRevealCached = errors.New("credential reveal: cached failure")
)

// Upstream decryption-layer sentinels. These classify failures inside the
// secret package itself; provider-layer callers should prefer the
// ErrReveal* family for consistent reason-label mapping, but downstream
// callers (admin diagnose, bg probes) can use these directly.
//
// History: ErrUnknownFormat replaces the anonymous errors.New("cannot
// decrypt: unknown format") that DecryptAny previously emitted. That string
// was the 2026-08-18 credential-17 incident signature — caller code had
// to string-compare err.Error() to detect it, which made the failure mode
// fragile under any future text refactor.
var (
	ErrUnknownFormat = errors.New("cannot decrypt: unknown format")
	ErrDecrypt       = errors.New("AES-GCM decryption failed — tampered data or wrong key")
)