// Package identity implements unified client fingerprint extraction and stable
// identity generation.  This is a port of app/core/identity.py from the
// Python control plane — the two implementations MUST produce identical
// outputs for the same inputs.
package identity

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

var identitySalt = os.Getenv("LLM_GATEWAY_IDENTITY_SALT")

// Header names accepted as fingerprint material.
const (
	headerDeviceSeed    = "X-Device-Seed"
	headerMachineID     = "X-Machine-Id"
	headerRuntimeName   = "X-Runtime-Name"
	headerRuntimeVer    = "X-Runtime-Version"
	headerOSName        = "X-OS-Name"
	headerOSArch        = "X-OS-Arch"
	virtualIPPrefix     = 10
	virtualMACFirstByte = 0x02
)

// ClientFingerprint holds raw normalised fingerprint fields extracted from
// the request.  Mirrors Python's ClientFingerprint dataclass.
type ClientFingerprint struct {
	DeviceSeed      string
	MachineID       string
	RuntimeName     string
	RuntimeVersion  string
	OSName          string
	OSArch          string
	UserAgent       string
	ClientProfile   string
}

// PrimarySeed returns the highest-priority stable seed, falling back to a
// composite of all available fields.  Mirrors Python's primary_seed().
func (f *ClientFingerprint) PrimarySeed() string {
	if f.DeviceSeed != "" {
		return f.DeviceSeed
	}
	if f.MachineID != "" {
		return f.MachineID
	}
	parts := make([]string, 0, 6)
	parts = appendIf(parts, f.UserAgent)
	parts = appendIf(parts, f.OSName)
	parts = appendIf(parts, f.OSArch)
	parts = appendIf(parts, f.RuntimeName)
	parts = appendIf(parts, f.RuntimeVersion)
	parts = appendIf(parts, f.ClientProfile)
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, "|")
}

func appendIf(parts []string, s string) []string {
	if s != "" {
		return append(parts, s)
	}
	return parts
}

// ClientIdentity holds the stable identity context derived for a single
// request.  Mirrors Python's ClientIdentity dataclass.
type ClientIdentity struct {
	TenantID        string
	AppOrKey        string
	Fingerprint     ClientFingerprint
	IdentityHash    string // 64-char hex (SHA-256)
	VirtualClientID string // "vc-" + IdentityHash[:16]
	VirtualIP       string // e.g. "10.42.17.33"
	VirtualMAC      string // e.g. "02:ab:cd:ef:12:34"
}

// ShortID returns the first 16 hex chars of the identity hash for logging.
func (c *ClientIdentity) ShortID() string {
	if len(c.IdentityHash) >= 16 {
		return c.IdentityHash[:16]
	}
	return c.IdentityHash
}

func sha256Hex(data string) string {
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h)
}

func deriveVirtualIP(h string) string {
	b := hexBytes(h, 0, 6)
	if len(b) < 3 {
		return "10.1.1.1"
	}
	clamp := func(v byte) int { v = v%254 + 1; return int(v) }
	return fmt.Sprintf("%d.%d.%d.%d", virtualIPPrefix, clamp(b[0]), clamp(b[1]), clamp(b[2]))
}

func deriveVirtualMAC(h string) string {
	b := hexBytes(h, 6, 16)
	if len(b) < 5 {
		return "02:00:00:00:00:00"
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		virtualMACFirstByte, b[0], b[1], b[2], b[3], b[4])
}

// hexBytes extracts up to n hex-encoded bytes from string s[start:end].
func hexBytes(s string, start, end int) []byte {
	if start >= len(s) {
		return nil
	}
	if end > len(s) {
		end = len(s)
	}
	// Ensure even length for hex decoding
	hexStr := s[start:end]
	if len(hexStr)%2 != 0 {
		hexStr = hexStr[:len(hexStr)-1]
	}
	if len(hexStr) == 0 {
		return nil
	}
	b := make([]byte, len(hexStr)/2)
	for i := 0; i < len(b); i++ {
		v := 0
		n, _ := fmt.Sscanf(hexStr[2*i:2*i+2], "%02x", &v)
		if n != 1 {
			v = 0
		}
		b[i] = byte(v)
	}
	return b
}

// ExtractFingerprint extracts and normalises fingerprint fields from HTTP
// headers.  Mirrors Python's extract_fingerprint().
func ExtractFingerprint(r *http.Request, clientProfile string) ClientFingerprint {
	h := r.Header
	fp := ClientFingerprint{
		DeviceSeed:     firstNonEmpty(h.Get(headerDeviceSeed)),
		MachineID:      firstNonEmpty(h.Get(headerMachineID)),
		RuntimeName:    firstNonEmpty(h.Get(headerRuntimeName)),
		RuntimeVersion: firstNonEmpty(h.Get(headerRuntimeVer)),
		OSName:         firstNonEmpty(h.Get(headerOSName)),
		OSArch:         firstNonEmpty(h.Get(headerOSArch)),
		UserAgent:      firstNonEmpty(h.Get("User-Agent")),
		ClientProfile:  clientProfile,
	}
	// Always blend client IP into the fingerprint when no explicit seeding
	// header (DeviceSeed/MachineID) is present, to prevent identity collision
	// for clients behind NAT or with identical user agents.
	if fp.DeviceSeed == "" && fp.MachineID == "" {
		clientIP := extractClientIP(r)
		if clientIP != "" {
			// Prepend IP-based tag so PrimarySeed returns a unique composite
			if fp.UserAgent == "" {
				fp.UserAgent = "ip:" + clientIP
			} else {
				fp.UserAgent = "ip:" + clientIP + "|" + fp.UserAgent
			}
		}
		// Last resort: even without client IP, add a random component
		// for uniqueness. This trades perfect reproducibility for
		// preventing mass identity collision.
		if fp.UserAgent == "" {
			fp.UserAgent = "ip:unknown"
		}
	}
	return fp
}

// extractClientIP extracts the client IP from X-Forwarded-For or RemoteAddr.
func extractClientIP(r *http.Request) string {
	// Try X-Forwarded-For first (for proxied requests)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (client IP)
		if idx := strings.IndexByte(xff, ','); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	// Fall back to RemoteAddr (ip:port format)
	if addr := r.RemoteAddr; addr != "" {
		if idx := strings.LastIndex(addr, ":"); idx > 0 {
			return addr[:idx]
		}
		return addr
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// BuildIdentity builds a stable ClientIdentity from tenant/key anchors +
// fingerprint.  Mirrors Python's build_identity().
func BuildIdentity(
	tenantID string,
	applicationID *int,
	apiKeyID *int,
	fp ClientFingerprint,
) ClientIdentity {
	appOrKey := "key0"
	if applicationID != nil {
		appOrKey = fmt.Sprintf("app%d", *applicationID)
	} else if apiKeyID != nil {
		appOrKey = fmt.Sprintf("key%d", *apiKeyID)
	}

	seed := fp.PrimarySeed()
	identityKey := fmt.Sprintf("%s:%s:%s:%s", tenantID, appOrKey, seed, identitySalt)
	identityHash := sha256Hex(identityKey)

	vip := deriveVirtualIP(identityHash)
	vmac := deriveVirtualMAC(identityHash)
	vcid := "vc-" + identityHash[:16]

	source := "composite"
	if fp.DeviceSeed != "" {
		source = "device_seed"
	} else if fp.MachineID != "" {
		source = "machine_id"
	}
	slog.Debug("identity built",
		"tenant", tenantID,
		"app_or_key", appOrKey,
		"seed_source", source,
		"hash_short", identityHash[:16],
		"vip", vip,
		"vmac", vmac,
	)

	return ClientIdentity{
		TenantID:        tenantID,
		AppOrKey:        appOrKey,
		Fingerprint:     fp,
		IdentityHash:    identityHash,
		VirtualClientID: vcid,
		VirtualIP:       vip,
		VirtualMAC:      vmac,
	}
}

// BuildIdentityFromRequest is a one-shot helper: extract fingerprint then
// build identity.  Mirrors Python's build_identity_from_request().
func BuildIdentityFromRequest(
	r *http.Request,
	tenantID string,
	applicationID *int,
	apiKeyID *int,
	clientProfile string,
) ClientIdentity {
	fp := ExtractFingerprint(r, clientProfile)
	return BuildIdentity(tenantID, applicationID, apiKeyID, fp)
}
