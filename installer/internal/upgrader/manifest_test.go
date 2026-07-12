package upgrader

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest(t *testing.T) {
	t.Run("valid manifest", func(t *testing.T) {
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "manifest.json")

		content := `{
  "version": "v1.14.0",
  "build_seq": 800,
  "sha256": "abc123",
  "files": [
    {"path": "kx-gateway", "sha256": "def456"},
    {"path": "VERSION", "sha256": "ghi789"}
  ]
}`
		if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		manifest, err := LoadManifest(manifestPath)
		if err != nil {
			t.Fatalf("LoadManifest failed: %v", err)
		}

		if manifest.Version != "v1.14.0" {
			t.Errorf("expected version v1.14.0, got %s", manifest.Version)
		}
		if manifest.BuildSeq != 800 {
			t.Errorf("expected build_seq 800, got %d", manifest.BuildSeq)
		}
		if len(manifest.Files) != 2 {
			t.Errorf("expected 2 files, got %d", len(manifest.Files))
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := LoadManifest("/nonexistent/manifest.json")
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "manifest.json")

		if err := os.WriteFile(manifestPath, []byte("invalid json"), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := LoadManifest(manifestPath)
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})
}

func TestVerifyManifest(t *testing.T) {
	t.Run("all files match", func(t *testing.T) {
		tmpDir := t.TempDir()

		// 创建测试文件
		testFile := filepath.Join(tmpDir, "test.txt")
		content := []byte("hello world")
		if err := os.WriteFile(testFile, content, 0644); err != nil {
			t.Fatal(err)
		}

		// 计算实际 SHA256: echo -n "hello world" | shasum -a 256
		// b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
		manifest := &Manifest{
			Version:  "v1.0.0",
			BuildSeq: 100,
			Files: []FileChecksum{
				{Path: "test.txt", SHA256: "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"},
			},
		}

		if err := VerifyManifest(manifest, tmpDir); err != nil {
			t.Errorf("VerifyManifest failed: %v", err)
		}
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		tmpDir := t.TempDir()

		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
			t.Fatal(err)
		}

		manifest := &Manifest{
			Version: "v1.0.0",
			Files: []FileChecksum{
				{Path: "test.txt", SHA256: "wronghash"},
			},
		}

		err := VerifyManifest(manifest, tmpDir)
		if err == nil {
			t.Error("expected error for checksum mismatch")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		tmpDir := t.TempDir()

		manifest := &Manifest{
			Version: "v1.0.0",
			Files: []FileChecksum{
				{Path: "missing.txt", SHA256: "abc123"},
			},
		}

		err := VerifyManifest(manifest, tmpDir)
		if err == nil {
			t.Error("expected error for missing file")
		}
	})
}
