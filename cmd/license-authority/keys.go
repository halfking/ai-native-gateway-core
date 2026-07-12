package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// LoadOrGenerateServerKeys loads existing Ed25519 keys or generates new ones on first run.
// Keys are stored in PEM format at <dataDir>/server.priv and <dataDir>/server.pub.
func LoadOrGenerateServerKeys(dataDir string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	privPath := filepath.Join(dataDir, "server.priv")
	pubPath := filepath.Join(dataDir, "server.pub")

	// Check if keys already exist
	if fileExists(privPath) && fileExists(pubPath) {
		return loadKeys(privPath, pubPath)
	}

	// Generate new key pair
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate Ed25519 key pair: %w", err)
	}

	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	// Save private key
	if err := savePrivateKey(privPath, privKey); err != nil {
		return nil, nil, err
	}

	// Save public key
	if err := savePublicKey(pubPath, pubKey); err != nil {
		return nil, nil, err
	}

	return privKey, pubKey, nil
}

// loadKeys reads Ed25519 keys from PEM files
func loadKeys(privPath, pubPath string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	// Load private key
	privPEM, err := os.ReadFile(privPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read private key: %w", err)
	}

	block, _ := pem.Decode(privPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, nil, fmt.Errorf("failed to decode private key PEM")
	}

	privKeyRaw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	privKey, ok := privKeyRaw.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("private key is not Ed25519")
	}

	// Load public key
	pubPEM, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read public key: %w", err)
	}

	block, _ = pem.Decode(pubPEM)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, nil, fmt.Errorf("failed to decode public key PEM")
	}

	pubKeyRaw, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	pubKey, ok := pubKeyRaw.(ed25519.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("public key is not Ed25519")
	}

	return privKey, pubKey, nil
}

// savePrivateKey writes an Ed25519 private key to PEM format
func savePrivateKey(path string, key ed25519.PrivateKey) error {
	privBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privBytes,
	})

	if err := os.WriteFile(path, privPEM, 0600); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	return nil
}

// savePublicKey writes an Ed25519 public key to PEM format
func savePublicKey(path string, key ed25519.PublicKey) error {
	pubBytes, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal public key: %w", err)
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})

	if err := os.WriteFile(path, pubPEM, 0644); err != nil {
		return fmt.Errorf("failed to write public key: %w", err)
	}

	return nil
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
