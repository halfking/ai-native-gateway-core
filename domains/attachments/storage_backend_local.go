package attachments

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalStorageBackend implements StorageBackend using local filesystem
type LocalStorageBackend struct {
	baseDir string
}

// NewLocalStorageBackend creates a new local filesystem storage backend
func NewLocalStorageBackend(baseDir string) (*LocalStorageBackend, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("base directory cannot be empty")
	}

	// Ensure base directory exists
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	return &LocalStorageBackend{
		baseDir: baseDir,
	}, nil
}

// BaseDir returns the base directory
func (s *LocalStorageBackend) BaseDir() string {
	return s.baseDir
}

// SetBaseDir updates the base directory
func (s *LocalStorageBackend) SetBaseDir(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	s.baseDir = dir
	return nil
}

// Save stores file content
func (s *LocalStorageBackend) Save(ctx context.Context, key string, data []byte) error {
	filePath := s.getFilePath(key)
	
	// Ensure target directory exists
	targetDir := filepath.Dir(filePath)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}
	
	// Write file atomically using temp file + rename
	tmpFile, err := os.CreateTemp(targetDir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()
	
	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	
	// Atomic rename
	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	
	return nil
}

// Get retrieves file content
func (s *LocalStorageBackend) Get(ctx context.Context, key string) ([]byte, error) {
	filePath := s.getFilePath(key)
	
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", key)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	
	return data, nil
}

// Load is an alias for Get (for compatibility)
func (s *LocalStorageBackend) Load(ctx context.Context, key string) ([]byte, error) {
	return s.Get(ctx, key)
}

// Delete removes a file
func (s *LocalStorageBackend) Delete(ctx context.Context, key string) error {
	filePath := s.getFilePath(key)
	
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete file: %w", err)
	}
	
	return nil
}

// Exists checks if a file exists
func (s *LocalStorageBackend) Exists(ctx context.Context, key string) (bool, error) {
	filePath := s.getFilePath(key)
	
	_, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check file existence: %w", err)
	}
	
	return true, nil
}

// List lists all files with optional prefix filter
func (s *LocalStorageBackend) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	
	err := filepath.Walk(s.baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		
		// Skip directories
		if info.IsDir() {
			return nil
		}
		
		// Get relative path from base dir
		relPath, err := filepath.Rel(s.baseDir, path)
		if err != nil {
			return err
		}
		
		// Convert to storage key format (use forward slashes)
		storageKey := filepath.ToSlash(relPath)
		
		// Apply prefix filter if specified
		if prefix != "" && !strings.HasPrefix(storageKey, prefix) {
			return nil
		}
		
		keys = append(keys, storageKey)
		return nil
	})
	
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}
	
	return keys, nil
}

// GetReader retrieves a file reader
func (s *LocalStorageBackend) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	filePath := s.getFilePath(key)
	
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", key)
		}
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	
	return file, nil
}

// SaveReader stores content from a reader
func (s *LocalStorageBackend) SaveReader(ctx context.Context, key string, reader io.Reader, size int64) error {
	filePath := s.getFilePath(key)
	
	// Ensure target directory exists
	targetDir := filepath.Dir(filePath)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}
	
	// Write file atomically using temp file + rename
	tmpFile, err := os.CreateTemp(targetDir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()
	
	if _, err := io.Copy(tmpFile, reader); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	
	// Atomic rename
	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	
	return nil
}

// GetMetadata retrieves file metadata
func (s *LocalStorageBackend) GetMetadata(ctx context.Context, key string) (*FileMetadata, error) {
	filePath := s.getFilePath(key)
	
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", key)
		}
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}
	
	return &FileMetadata{
		Key:          key,
		Size:         info.Size(),
		LastModified: info.ModTime(),
		ContentType:  "", // Not stored in filesystem
		ETag:         "", // Not applicable for local storage
	}, nil
}

// GetBackendType returns the backend type
func (s *LocalStorageBackend) GetBackendType() string {
	return "filesystem"
}

// HealthCheck performs a health check
func (s *LocalStorageBackend) HealthCheck(ctx context.Context) error {
	// Check if base directory is accessible
	_, err := os.Stat(s.baseDir)
	if err != nil {
		return fmt.Errorf("base directory not accessible: %w", err)
	}
	
	// Try to create a temp file to ensure write permission
	tmpFile, err := os.CreateTemp(s.baseDir, ".healthcheck-*")
	if err != nil {
		return fmt.Errorf("cannot write to base directory: %w", err)
	}
	tmpFile.Close()
	os.Remove(tmpFile.Name())
	
	return nil
}

// getFilePath converts a storage key to an absolute file path
func (s *LocalStorageBackend) getFilePath(storageKey string) string {
	return filepath.Join(s.baseDir, filepath.FromSlash(storageKey))
}
