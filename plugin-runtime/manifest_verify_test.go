package pluginruntime

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyManifestSignature_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	manifestBytes := []byte(`{"plugin_id":"x","plugin_version":"1","gateway_compatibility":{"api_contract":"gateway-plugin-v1"},"runtime":{"handshake_path":"/h","health_path":"/z"}}`)
	sig := ed25519.Sign(priv, manifestBytes)

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "plugin-manifest.json")
	os.WriteFile(manifestPath, manifestBytes, 0644)
	os.WriteFile(manifestPath+".sig", sig, 0644)

	if err := VerifyManifestSignature(manifestPath, hex.EncodeToString(pub)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyManifestSignature_Tampered(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sig := ed25519.Sign(priv, []byte(`{"plugin_id":"x"}`))
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "plugin-manifest.json")
	os.WriteFile(manifestPath, []byte(`{"plugin_id":"evil"}`), 0644)
	os.WriteFile(manifestPath+".sig", sig, 0644)

	if err := VerifyManifestSignature(manifestPath, hex.EncodeToString(pub)); err == nil {
		t.Fatal("tampered manifest should be rejected")
	}
}

func TestVerifyManifestSignature_NoPubkeySkips(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "plugin-manifest.json")
	os.WriteFile(manifestPath, []byte(`{}`), 0644)
	if err := VerifyManifestSignature(manifestPath, ""); err != nil {
		t.Fatalf("empty pubkey should skip verification, got %v", err)
	}
}

func TestVerifyManifestSignature_MissingSigFile(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "plugin-manifest.json")
	os.WriteFile(manifestPath, []byte(`{}`), 0644)
	if err := VerifyManifestSignature(manifestPath, hex.EncodeToString(pub)); err == nil {
		t.Fatal("missing sig file with pubkey set should fail")
	}
}
