package pluginruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarball builds an in-memory .tar.gz from the given (path -> content) map.
func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		tw.Write([]byte(content))
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestExtractTarballHappyPath(t *testing.T) {
	dest := t.TempDir()
	tarball := makeTarball(t, map[string]string{
		"bin/ai-session-manager": "fake-binary",
		"plugin-manifest.json":   `{"plugin_id":"asm"}`,
		"web/index.html":         "<html/>",
	})
	r := bytes.NewReader(tarball)
	n, err := ExtractTarball(r, dest)
	if err != nil {
		t.Fatalf("ExtractTarball: %v", err)
	}
	if n != 3 {
		t.Errorf("extracted count = %d, want 3", n)
	}
	for _, p := range []string{"bin/ai-session-manager", "plugin-manifest.json", "web/index.html"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestExtractTarballRejectsTraversal(t *testing.T) {
	cases := map[string]string{
		"dotdot":        "../escape.txt",
		"absolute":      "/etc/bad.txt",
		"nested-dotdot": "bin/../../escape.txt",
	}
	for name, member := range cases {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gw)
			tw.WriteHeader(&tar.Header{Name: member, Mode: 0o644, Size: 1})
			tw.Write([]byte("x"))
			tw.Close()
			gw.Close()
			_, err := ExtractTarball(bytes.NewReader(buf.Bytes()), dest)
			if err == nil {
				t.Fatalf("expected rejection for %s, got nil", name)
			}
			// verify nothing escaped
			if _, err := os.Stat(filepath.Join(dest, "escape.txt")); err == nil {
				t.Errorf("escape.txt should not exist for %s", name)
			}
		})
	}
}

func TestExtractTarballRejectsSymlinkEscape(t *testing.T) {
	dest := t.TempDir()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "link", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Size: 0})
	tw.Close()
	gw.Close()
	_, err := ExtractTarball(bytes.NewReader(buf.Bytes()), dest)
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestExtractTarballRejectsRelativeSymlinkEscape(t *testing.T) {
	// symlink "link" -> "../../../etc/passwd" should be rejected
	dest := t.TempDir()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "link", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "../../../etc/passwd", Size: 0})
	tw.Close()
	gw.Close()
	_, err := ExtractTarball(bytes.NewReader(buf.Bytes()), dest)
	if err == nil {
		t.Fatal("expected relative symlink escape rejection")
	}
}

func TestExtractTarballPreservesFileMode(t *testing.T) {
	dest := t.TempDir()
	// rebuild with executable mode
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "bin/run.sh", Mode: 0o755, Size: 9})
	tw.Write([]byte("#!/bin/sh\n"))
	tw.Close()
	gw.Close()
	_, err := ExtractTarball(bytes.NewReader(buf.Bytes()), dest)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	info, _ := os.Stat(filepath.Join(dest, "bin/run.sh"))
	// mode masked with 0o755|0o600 per impl; 0o755 & 0o755 | 0o600 = 0o755
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 755", info.Mode().Perm())
	}
}
