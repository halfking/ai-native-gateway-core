package upgrader

import (
	"os"
	"path/filepath"
	"strings"
)

func resolveInstanceID() string {
	if id := strings.TrimSpace(os.Getenv("INSTANCE_ID")); id != "" {
		return id
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "local"
	}
	data, err := os.ReadFile(filepath.Join(homeDir, ".kx-gateway", "instance.id"))
	if err != nil {
		return "local"
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "local"
	}
	return id
}
