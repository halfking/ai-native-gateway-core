// Package upgrader 提供升级包管理功能
package upgrader

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var startupSQLFilename = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*\.sql$`)
var offlineImageReference = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.:/-]*$`)
var offlineImageTag = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$`)
var offlineImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Manifest 离线升级包清单
type Manifest struct {
	Version       string         `json:"version"`   // 版本号，如 v1.14.0
	BuildSeq      int            `json:"build_seq"` // 构建序号
	SHA256        string         `json:"sha256"`    // 整包 SHA256（可选）
	Files         []FileChecksum `json:"files"`     // 文件清单
	MigrationMode string         `json:"migration_mode,omitempty"`
	AppImage      string         `json:"app_image,omitempty"`
	AppImageID    string         `json:"app_image_id,omitempty"`
}

func (m *Manifest) installerImageTag() (string, error) {
	tag := strings.TrimPrefix(m.Version, "v")
	if !offlineImageTag.MatchString(tag) || !offlineImageReference.MatchString(m.AppImage) || !offlineImageID.MatchString(m.AppImageID) {
		return "", fmt.Errorf("invalid installer package image reference, image ID or version tag")
	}
	for _, file := range m.Files {
		if file.Path == "images.tar" {
			return tag, nil
		}
	}
	return "", fmt.Errorf("installer package is missing a verified images.tar entry")
}

// installerStartupFiles returns the checksum-verified startup file order for
// an installer-startup package. Legacy archives use their original layout.
func installerStartupFiles(manifest *Manifest) ([]string, error) {
	if manifest.MigrationMode == "" {
		return nil, nil
	}
	if manifest.MigrationMode != "installer-startup" {
		return nil, fmt.Errorf("unknown offline migration mode %q", manifest.MigrationMode)
	}
	var files []string
	seen := make(map[string]bool)
	for _, entry := range manifest.Files {
		path := filepath.ToSlash(filepath.Clean(entry.Path))
		if !strings.HasPrefix(path, "migrations/") {
			continue
		}
		name, ok := strings.CutPrefix(path, "migrations/startup/")
		if !ok || path != entry.Path || !startupSQLFilename.MatchString(name) || strings.HasSuffix(name, ".down.sql") {
			return nil, fmt.Errorf("invalid installer startup migration path %q", entry.Path)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate installer startup migration %q", name)
		}
		seen[name] = true
		files = append(files, name)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("installer startup migration list is empty")
	}
	return files, nil
}

// FileChecksum 文件校验和
type FileChecksum struct {
	Path   string `json:"path"`   // 相对路径
	SHA256 string `json:"sha256"` // SHA256 校验和
}

// LoadManifest 从文件加载 manifest.json
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest JSON: %w", err)
	}

	// 基本校验
	if manifest.Version == "" {
		return nil, fmt.Errorf("manifest missing version field")
	}
	if len(manifest.Files) == 0 {
		return nil, fmt.Errorf("manifest files must not be empty")
	}
	for _, file := range manifest.Files {
		if err := validateManifestFile(file); err != nil {
			return nil, err
		}
	}

	return &manifest, nil
}

// VerifyManifest 验证清单中所有文件的 SHA256
func VerifyManifest(manifest *Manifest, extractDir string) error {
	if manifest == nil || len(manifest.Files) == 0 {
		return fmt.Errorf("manifest files must not be empty")
	}
	for _, f := range manifest.Files {
		filePath, err := secureManifestPath(extractDir, f.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(filePath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", f.Path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("manifest path is not a regular file: %s", f.Path)
		}

		// 计算文件 SHA256
		hash, err := calculateFileSHA256(filePath)
		if err != nil {
			return fmt.Errorf("calculate SHA256 for %s: %w", f.Path, err)
		}

		// 对比校验和
		if hash != f.SHA256 {
			return fmt.Errorf("SHA256 mismatch for %s: expected %s, got %s", f.Path, f.SHA256, hash)
		}
	}

	return nil
}

func validateManifestFile(file FileChecksum) error {
	if _, err := secureManifestPath("/extract", file.Path); err != nil {
		return err
	}
	if !sha256Pattern.MatchString(strings.TrimSpace(file.SHA256)) {
		return fmt.Errorf("invalid SHA256 for %s", file.Path)
	}
	return nil
}

func secureManifestPath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("invalid manifest path: %q", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("manifest path escapes extraction directory: %q", name)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve extraction directory: %w", err)
	}
	pathAbs, err := filepath.Abs(filepath.Join(rootAbs, clean))
	if err != nil {
		return "", fmt.Errorf("resolve manifest path: %w", err)
	}
	if pathAbs != rootAbs && !strings.HasPrefix(pathAbs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("manifest path escapes extraction directory: %q", name)
	}
	return pathAbs, nil
}

// calculateFileSHA256 计算文件的 SHA256 校验和
func calculateFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
