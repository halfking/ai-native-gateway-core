package admin

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const mobileNodeDescriptorTTL = 5 * time.Minute

// MobileNodeDescriptorService creates short-lived, signed QR payloads used by
// the native client to add a gateway without transferring user credentials.
type MobileNodeDescriptorService struct {
	nodeID, name, publicOrigin, netbirdOrigin, peerID string
	signingKey                                        ed25519.PrivateKey
	netbird                                           *NetBirdGatewayAccess
}

type MobileNodeDescriptor struct {
	Version       int    `json:"version"`
	NodeID        string `json:"node_id"`
	Name          string `json:"name"`
	PublicOrigin  string `json:"public_origin,omitempty"`
	NetBirdOrigin string `json:"netbird_origin,omitempty"`
	PeerID        string `json:"peer_id,omitempty"`
	ExpiresAt     string `json:"expires_at"`
	Nonce         string `json:"nonce"`
	PublicKey     string `json:"public_key"`
}

// NewMobileNodeDescriptorServiceFromEnv returns nil when QR provisioning is
// not configured. A private NetBird origin is advertised only when the
// management API can also ensure the peer group and HTTPS policy.
func NewMobileNodeDescriptorServiceFromEnv() (*MobileNodeDescriptorService, error) {
	keyText := strings.TrimSpace(os.Getenv("LLMGW_NODE_DESCRIPTOR_PRIVATE_KEY"))
	if keyText == "" {
		return nil, nil
	}
	decode := func(s string) ([]byte, error) {
		if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
			return b, nil
		}
		return base64.StdEncoding.DecodeString(s)
	}
	keyBytes, err := decode(keyText)
	if err != nil {
		return nil, errors.New("invalid mobile node signing key encoding")
	}
	var signingKey ed25519.PrivateKey
	switch len(keyBytes) {
	case ed25519.SeedSize:
		signingKey = ed25519.NewKeyFromSeed(keyBytes)
	case ed25519.PrivateKeySize:
		signingKey = ed25519.NewKeyFromSeed(keyBytes[:ed25519.SeedSize])
		if !bytes.Equal(signingKey, keyBytes) {
			return nil, errors.New("mobile node private key public half does not match its seed")
		}
	default:
		return nil, errors.New("mobile node signing key must contain an Ed25519 seed or private key")
	}

	publicOrigin, err := normalizeGatewayOrigin(os.Getenv("LLMGW_PUBLIC_ORIGIN"))
	if err != nil {
		return nil, fmt.Errorf("invalid LLMGW_PUBLIC_ORIGIN: %w", err)
	}
	netbirdOrigin, err := normalizeGatewayOrigin(os.Getenv("LLMGW_NETBIRD_ORIGIN"))
	if err != nil {
		return nil, fmt.Errorf("invalid LLMGW_NETBIRD_ORIGIN: %w", err)
	}
	if publicOrigin == "" && netbirdOrigin == "" {
		return nil, errors.New("at least one mobile gateway origin must be configured")
	}

	service := &MobileNodeDescriptorService{
		nodeID:        strings.TrimSpace(os.Getenv("LLMGW_NODE_ID")),
		name:          strings.TrimSpace(os.Getenv("LLMGW_NODE_NAME")),
		publicOrigin:  publicOrigin,
		netbirdOrigin: netbirdOrigin,
		peerID:        strings.TrimSpace(os.Getenv("NETBIRD_GATEWAY_PEER_ID")),
		signingKey:    signingKey,
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(service.nodeID) || strings.HasPrefix(service.nodeID, "preset:") || strings.HasPrefix(service.nodeID, "legacy:") || service.name == "" || len(utf16.Encode([]rune(service.name))) > 160 || len(service.peerID) > 128 {
		return nil, errors.New("LLMGW_NODE_ID and LLMGW_NODE_NAME are required")
	}
	if netbirdOrigin != "" {
		originURL, _ := url.Parse(netbirdOrigin)
		if originURL.Port() != "" {
			return nil, errors.New("NetBird gateway origin must use HTTPS port 443")
		}
		access, err := NewNetBirdGatewayAccessFromEnv()
		if err != nil {
			return nil, err
		}
		service.netbird = access
		if service.peerID == "" {
			return nil, errors.New("NETBIRD_GATEWAY_PEER_ID is required when a NetBird origin is configured")
		}
	}
	return service, nil
}

func normalizeGatewayOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > 512 {
		return "", errors.New("origin is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("must be an origin without a path, credentials, query, or fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" {
		return "", errors.New("HTTPS is required")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid port")
		}
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	if u.Port() == "443" {
		u.Host = strings.ToLower(u.Hostname())
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (s *MobileNodeDescriptorService) Create(r *http.Request) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, errors.New("mobile node QR provisioning is not configured")
	}
	expires := time.Now().UTC().Add(mobileNodeDescriptorTTL)
	nonce := make([]byte, 18)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, fmt.Errorf("create mobile node nonce: %w", err)
	}
	publicKey := s.signingKey.Public().(ed25519.PublicKey)
	payload := MobileNodeDescriptor{
		Version: 1, NodeID: s.nodeID, Name: s.name, PublicOrigin: s.publicOrigin,
		NetBirdOrigin: s.netbirdOrigin, PeerID: s.peerID, ExpiresAt: expires.Format(time.RFC3339),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode mobile node descriptor: %w", err)
	}
	if len(payloadBytes) > 2048 {
		return "", time.Time{}, errors.New("mobile node descriptor exceeds client size limit")
	}
	if s.netbird != nil {
		if err := s.netbird.EnsureGatewayAccess(r.Context(), s.nodeID, s.peerID); err != nil {
			return "", time.Time{}, err
		}
	}
	signature := ed25519.Sign(s.signingKey, payloadBytes)
	envelope := "llmgw-node:v1." + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(signature)
	return envelope, expires, nil
}

func (h *Handler) handleMobileNodeDescriptor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.mobileNodeDescriptor == nil {
		http.Error(w, "mobile node provisioning is not configured", http.StatusServiceUnavailable)
		return
	}
	payload, expires, err := h.mobileNodeDescriptor.Create(r)
	if err != nil {
		slog.Warn("mobile node access reconciliation failed", "node_id", h.mobileNodeDescriptor.nodeID, "error", err)
		// Avoid reflecting provider/network errors or credentials to the caller.
		http.Error(w, "could not prepare gateway access; check NetBird and node configuration", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	digest := sha256.Sum256(h.mobileNodeDescriptor.signingKey.Public().(ed25519.PublicKey))
	_ = json.NewEncoder(w).Encode(map[string]string{"payload": payload, "expires_at": expires.Format(time.RFC3339), "signing_key_fingerprint": hex.EncodeToString(digest[:])})
}
