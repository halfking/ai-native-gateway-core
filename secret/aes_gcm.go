// aes_gcm.go — AES-256-GCM envelope encryption, replacing Fernet-CBC.
//
// Wire format: v1:<kid>:<base64url(nonce12 || ciphertext || tag16)>
//
// Backward compatibility: DecryptAny() tries AES-GCM first, then falls back
// to legacy Fernet decryption for rows not yet migrated.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const (
	gcmNonceSize = 12
	gcmVersion   = "v1"
	gcmSep       = ":"
	gcmAAD       = "llm-gateway:credential"
)

// --- Keyring -----------------------------------------------------------------

// Keyring holds 32-byte AES keys indexed by kid.
type Keyring struct {
	keys    map[string][32]byte
	current string
}

// NewKeyring creates a Keyring from a map of kid→rawKey.
func NewKeyring(keys map[string][32]byte, current string) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("securekey: keyring is empty")
	}
	if _, ok := keys[current]; !ok {
		return nil, fmt.Errorf("securekey: current_kid=%q not in keyring", current)
	}
	cp := make(map[string][32]byte, len(keys))
	for k, v := range keys {
		cp[k] = v
	}
	return &Keyring{keys: cp, current: current}, nil
}

// KeyringFromEnv loads the keyring from environment variables.
//
//   Priority:
//   1. KEYRING_JSON + KEYRING_CURRENT_KID
//   2. CREDENTIAL_ENCRYPTION_KEY (base64url 32 bytes) → kid "legacy"
//   3. SECRET_KEY SHA-256 → kid "legacy"
func KeyringFromEnv(secretKey, credEncKey string) (*Keyring, error) {
	rawJSON := strings.TrimSpace(os.Getenv("KEYRING_JSON"))
	if rawJSON != "" {
		var mapping map[string]string
		if err := json.Unmarshal([]byte(rawJSON), &mapping); err != nil {
			return nil, fmt.Errorf("KEYRING_JSON parse error: %w", err)
		}
		keys := make(map[string][32]byte, len(mapping))
		for kid, b64 := range mapping {
			raw, err := decodeBase64URLKey(b64)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", kid, err)
			}
			keys[kid] = raw
		}
		current := strings.TrimSpace(os.Getenv("KEYRING_CURRENT_KID"))
		if current == "" {
			var kids []string
			for k := range keys {
				kids = append(kids, k)
			}
			sort.Strings(kids)
			current = kids[len(kids)-1]
		}
		return NewKeyring(keys, current)
	}

	// Fallback: single key
	if credEncKey != "" {
		raw, err := decodeBase64URLKey(credEncKey)
		if err == nil {
			return NewKeyring(map[string][32]byte{"legacy": raw}, "legacy")
		}
	}
	if secretKey != "" {
		digest := sha256.Sum256([]byte(secretKey))
		return NewKeyring(map[string][32]byte{"legacy": digest}, "legacy")
	}
	return nil, ErrNoKey
}

func (kr *Keyring) getKey(kid string) ([32]byte, error) {
	raw, ok := kr.keys[kid]
	if !ok {
		return [32]byte{}, fmt.Errorf("unknown kid=%q", kid)
	}
	return raw, nil
}

// --- Encrypt / Decrypt -------------------------------------------------------

// EncryptAESGCM encrypts plaintext using the keyring's current kid.
// Returns a v1 envelope string.
func EncryptAESGCM(plaintext []byte, kr *Keyring) (string, error) {
	kid := kr.current
	rawKey := kr.keys[kid]

	nonce := make([]byte, gcmNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	block, err := aes.NewCipher(rawKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	// Seal appends tag; blob = nonce || ciphertext || tag
	blob := gcm.Seal(nonce, nonce, plaintext, []byte(gcmAAD))
	payload := base64.RawURLEncoding.EncodeToString(blob)
	return gcmVersion + gcmSep + kid + gcmSep + payload, nil
}

// DecryptAESGCM decrypts a v1 envelope string or byte slice.
func DecryptAESGCM[T string | []byte](envelope T, kr *Keyring) ([]byte, error) {
	return decryptAESGCMStr(string(envelope), kr)
}

func decryptAESGCMStr(envelope string, kr *Keyring) ([]byte, error) {
	parts := strings.SplitN(envelope, gcmSep, 3)
	if len(parts) != 3 || parts[0] != gcmVersion {
		return nil, fmt.Errorf("malformed AES-GCM envelope")
	}
	kid, payloadB64 := parts[1], parts[2]

	rawKey, err := kr.getKey(kid)
	if err != nil {
		return nil, err
	}
	blob, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, fmt.Errorf("base64url decode: %w", err)
	}
	if len(blob) < gcmNonceSize+16 {
		return nil, fmt.Errorf("payload too short (%d bytes)", len(blob))
	}

	nonce := blob[:gcmNonceSize]
	ctAndTag := blob[gcmNonceSize:]
	block, err := aes.NewCipher(rawKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ctAndTag, []byte(gcmAAD))
	if err != nil {
		return nil, errors.New("AES-GCM decryption failed — tampered data or wrong key")
	}
	return plaintext, nil
}

// IsV1Envelope returns true if the string looks like a v1 AES-GCM envelope.
func IsV1Envelope(s string) bool {
	return strings.HasPrefix(s, gcmVersion+gcmSep)
}

// DecryptAny decrypts either a v1 AES-GCM envelope or a legacy Fernet token.
// Returns (plaintext, isLegacy, error).  When isLegacy=true, the caller should
// re-encrypt and persist the new envelope to complete lazy migration.
func DecryptAny(ciphertext string, kr *Keyring, fernetKey []byte) ([]byte, bool, error) {
	if IsV1Envelope(ciphertext) {
		pt, err := DecryptAESGCM(ciphertext, kr)
		return pt, false, err
	}
	// Try Fernet legacy
	if len(fernetKey) == 32 {
		pt, err := DecryptFernet([]byte(ciphertext), fernetKey)
		if err == nil {
			return []byte(pt), true, nil
		}
	}
	return nil, false, errors.New("cannot decrypt: unknown format")
}

// --- Helpers -----------------------------------------------------------------

func decodeBase64URLKey(s string) ([32]byte, error) {
	s = strings.TrimRight(s, "=")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		// Try with padding
		switch len(s) % 4 {
		case 2:
			s += "=="
		case 3:
			s += "="
		}
		raw, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return [32]byte{}, fmt.Errorf("invalid base64url: %w", err)
		}
	}
	if len(raw) != 32 {
		return [32]byte{}, fmt.Errorf("expected 32 bytes, got %d", len(raw))
	}
	var out [32]byte
	copy(out[:], raw)
	return out, nil
}
