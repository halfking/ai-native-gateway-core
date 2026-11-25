package upgrader

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	offlineTestOldImage = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	offlineTestNewImage = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestApplyOfflinePackageSwitchesComposeImageAndRestoresOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a shell fixture")
	}
	const docker = `#!/bin/sh
set -eu
case "$1 $2" in
  'inspect --format') cat "$OFFLINE_TEST_DIR/running" ;;
  'load -i') test -s "$3" ;;
  'image inspect') test "$5" = 'example.invalid/gateway:new'; echo "$OFFLINE_TEST_NEW_IMAGE" ;;
  'image tag') printf '%s\n' "$3" > "$OFFLINE_TEST_DIR/tag_${4#*:}" ;;
  'compose -p')
    test "$3" = llm-gateway-go
    shift 5
    test "$*" = 'up -d --no-deps --pull never --force-recreate --wait --wait-timeout 120 llm-gateway-go'
    printf '%s\n' "$APP_IMAGE_TAG" >> "$OFFLINE_TEST_DIR/switches"
    cat "$OFFLINE_TEST_DIR/tag_$APP_IMAGE_TAG" > "$OFFLINE_TEST_DIR/running"
    if [ "$APP_IMAGE_TAG" = new ] && [ "$OFFLINE_TEST_FAIL_HEALTH" = true ]; then exit 1; fi
    ;;
  *) exit 2 ;;
esac
`
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("health_failure=%t", fail), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "app"), 0700); err != nil {
				t.Fatal(err)
			}
			originalEnv := "# keep this comment\nAPP_IMAGE_TAG=old\nOTHER_SETTING='a=1'\n"
			for name, contents := range map[string]string{
				"docker": docker, "compose.yml": "services: {}\n", "running": offlineTestOldImage + "\n",
				"app/VERSION": "old\n", ".env": originalEnv,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("OFFLINE_TEST_DIR", dir)
			t.Setenv("OFFLINE_TEST_FAIL_HEALTH", fmt.Sprint(fail))
			t.Setenv("OFFLINE_TEST_NEW_IMAGE", offlineTestNewImage)
			t.Setenv("APP_IMAGE_TAG", "ambient-tag")
			archive, migrations := writeOfflineComposeTestArchive(t, dir)
			a := NewOfflineApplier(dir, filepath.Join(dir, "backups"), filepath.Join(dir, "compose.yml"), "fixture")
			calls := 0
			a.UpgradeStartup = func(sqlDir string, files []string) error {
				calls++
				if !slices.Equal(files, migrations) {
					t.Fatalf("migration order = %v, want %v", files, migrations)
				}
				for _, name := range files {
					if _, err := os.Stat(filepath.Join(sqlDir, "startup", name)); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			if err := a.ApplyOfflinePackage(archive, dir); (err != nil) != fail || fail && !strings.Contains(err.Error(), "recreate Compose gateway") {
				t.Fatalf("ApplyOfflinePackage error = %v, health failure = %t", err, fail)
			}
			if calls != 1 {
				t.Fatalf("migration runs = %d, want 1", calls)
			}
			want := map[string]string{
				"running": offlineTestNewImage + "\n", "app/VERSION": "new\n", "switches": "new\n",
				".env": strings.Replace(originalEnv, "APP_IMAGE_TAG=old", "APP_IMAGE_TAG=new", 1),
			}
			if fail {
				want["running"], want["app/VERSION"], want["switches"], want[".env"] = offlineTestOldImage+"\n", "old\n", "new\nold\n", originalEnv
			}
			for name, expected := range want {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != expected {
					t.Errorf("%s = %q, %v; want %q", name, got, err, expected)
				}
			}
		})
	}
}

func writeOfflineComposeTestArchive(t *testing.T, dir string) (string, []string) {
	t.Helper()
	path := filepath.Join(dir, "upgrade.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	archive := tar.NewWriter(gzipWriter)
	write := func(name string, content []byte) {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	migrations := []string{"983_request_metrics.sql", "973_request_metrics_projection.sql"}
	manifest := Manifest{Version: "vnew", MigrationMode: "installer-startup", AppImage: "example.invalid/gateway:new", AppImageID: offlineTestNewImage}
	images := []byte("fixture image")
	write("images.tar", images)
	manifest.Files = append(manifest.Files, FileChecksum{Path: "images.tar", SHA256: fmt.Sprintf("%x", sha256.Sum256(images))})
	for _, name := range migrations {
		name = "migrations/startup/" + name
		contents := []byte("SELECT 1;")
		write(name, contents)
		manifest.Files = append(manifest.Files, FileChecksum{Path: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(contents))})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	write("manifest.json", data)
	for _, close := range []func() error{archive.Close, gzipWriter.Close, file.Close} {
		if err := close(); err != nil {
			t.Fatal(err)
		}
	}
	return path, migrations
}
