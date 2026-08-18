package secret

import (
	"errors"
	"testing"
)

// TestDecryptAnyReturnsErrUnknownFormat guards the sentinel that replaced
// the anonymous errors.New("cannot decrypt: unknown format").  The 2026-08-18
// credential-17 incident signature was this exact message — call sites used
// strings.Contains to detect it, which made the failure mode fragile under
// any future text refactor.  Now callers can errors.Is(err, ErrUnknownFormat)
// instead.
func TestDecryptAnyReturnsErrUnknownFormat(t *testing.T) {
	cases := []struct {
		name       string
		ciphertext string
	}{
		{"empty string", ""},
		{"garbage", "not-a-real-envelope"},
		{"v1 prefix with garbage payload", "v1:legacy:$$$"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := DecryptAny(tc.ciphertext, nil, nil)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, ErrUnknownFormat) {
				t.Fatalf("expected errors.Is(err, ErrUnknownFormat)=true, got err=%v", err)
			}
		})
	}
}

// TestDecryptAESGCMReturnsErrDecrypt guards the sentinel that replaced
// the anonymous errors.New("AES-GCM decryption failed …").  We encrypt
// with one keyring and decrypt with another — the GCM auth tag check must
// fail and the wrapper must surface ErrDecrypt rather than the bare
// gcm.Open error.
func TestDecryptAESGCMReturnsErrDecrypt(t *testing.T) {
	encKr, err := NewKeyring(map[string][32]byte{"k1": {1}}, "k1")
	if err != nil {
		t.Fatalf("enc keyring: %v", err)
	}
	env, err := EncryptAESGCM([]byte("hello"), encKr)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Decrypt with a different key under the same kid — kid resolution
	// succeeds, but GCM tag check fails and the wrapper returns ErrDecrypt.
	decKr, err := NewKeyring(map[string][32]byte{"k1": {2}}, "k1")
	if err != nil {
		t.Fatalf("dec keyring: %v", err)
	}
	_, err = DecryptAESGCM(env, decKr)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected errors.Is(err, ErrDecrypt)=true, got err=%v", err)
	}
}

// TestErrRevealSentinelsAreDistinct ensures the six ErrReveal* sentinels
// do not collide on identity (i.e. each is a separate errors.New instance).
// If two of them accidentally became the same value, classifyRevealFailure
// would misroute alerts and the credential-17 dashboard would break.
func TestErrRevealSentinelsAreDistinct(t *testing.T) {
	sentinels := []error{
		ErrRevealUnknownFormat,
		ErrRevealDecrypt,
		ErrRevealNotFound,
		ErrRevealNotConfigured,
		ErrRevealRotation,
		ErrRevealCached,
	}
	seen := make(map[error]string, len(sentinels))
	for i, s := range sentinels {
		if other, ok := seen[s]; ok {
			t.Fatalf("sentinel %d duplicates %s", i, other)
		}
		seen[s] = sentinelName(i)
	}
}

func sentinelName(i int) string {
	switch i {
	case 0:
		return "ErrRevealUnknownFormat"
	case 1:
		return "ErrRevealDecrypt"
	case 2:
		return "ErrRevealNotFound"
	case 3:
		return "ErrRevealNotConfigured"
	case 4:
		return "ErrRevealRotation"
	case 5:
		return "ErrRevealCached"
	}
	return "?"
}