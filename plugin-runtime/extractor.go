package pluginruntime

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractTarball extracts a gzip-compressed tar stream into destDir.
// It rejects any member whose target (after cleaning) escapes destDir,
// and any symlink whose link target resolves outside destDir. Returns
// the number of regular files extracted.
func ExtractTarball(r io.Reader, destDir string) (int, error) {
	cleanDest, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return 0, fmt.Errorf("resolve dest: %w", err)
	}
	if err := os.MkdirAll(cleanDest, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir dest: %w", err)
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, fmt.Errorf("tar read: %w", err)
		}
		// Reject absolute and Windows-drive member names BEFORE Join: filepath.Join
		// treats the second arg as relative, so "/etc/x" would silently become
		// "<dest>/etc/x" and bypass the traversal check below.
		if filepath.IsAbs(hdr.Name) || strings.Contains(hdr.Name, ":") {
			return count, fmt.Errorf("refusing absolute path: %q", hdr.Name)
		}
		target := filepath.Join(cleanDest, hdr.Name)
		cleanTarget := filepath.Clean(target)
		if !isWithin(cleanTarget, cleanDest) {
			return count, fmt.Errorf("refusing path traversal: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(cleanTarget, os.FileMode(hdr.Mode)&0o777|0o700); err != nil {
				return count, fmt.Errorf("mkdir %s: %w", hdr.Name, err)
			}
		case tar.TypeSymlink, tar.TypeLink:
			link := hdr.Linkname
			if filepath.IsAbs(link) {
				return count, fmt.Errorf("refusing absolute link: %q -> %q", hdr.Name, link)
			}
			linkResolved := filepath.Clean(filepath.Join(filepath.Dir(cleanTarget), link))
			if !isWithin(linkResolved, cleanDest) {
				return count, fmt.Errorf("refusing link escape: %q -> %q", hdr.Name, link)
			}
			if hdr.Typeflag == tar.TypeSymlink {
				if err := os.Symlink(link, cleanTarget); err != nil {
					return count, fmt.Errorf("symlink %s: %w", hdr.Name, err)
				}
			} else {
				if err := os.Link(filepath.Join(filepath.Dir(cleanTarget), link), cleanTarget); err != nil {
					return count, fmt.Errorf("hardlink %s: %w", hdr.Name, err)
				}
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
				return count, fmt.Errorf("mkdir parent %s: %w", hdr.Name, err)
			}
			f, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o755|0o600)
			if err != nil {
				return count, fmt.Errorf("create %s: %w", hdr.Name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return count, fmt.Errorf("write %s: %w", hdr.Name, err)
			}
			f.Close()
			count++
		default:
			// skip devices, fifos, etc.
		}
	}
	return count, nil
}

// isWithin reports whether target == root or is nested under root.
func isWithin(target, root string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..") && !strings.Contains(rel, string(filepath.Separator)+"..")
}
