package child

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation"
	"gopkg.in/yaml.v3"
)

func ReadPolicy(path string, out any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, remediation.MaxPayload+1))
	if err != nil {
		return err
	}
	if len(data) > remediation.MaxPayload {
		return errors.New("template exceeds size limit")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("expected one YAML template")
	}
	return nil
}

// Serve loads one preinstalled template and serves its private Unix endpoint.
func Serve(id string, configure func(*pluginruntime.Manifest) (http.HandlerFunc, error)) error {
	manifest, err := pluginruntime.LoadManifest(os.Getenv("AI_SESSION_MANAGER_MANIFEST"))
	if err != nil {
		return err
	}
	if manifest.PluginID != id ||
		manifest.GatewayCompatibility.APIContract != pluginruntime.SupportedAPIContractV2 ||
		!slices.Contains(manifest.Capabilities, pluginruntime.CapabilityErrorRemediation) {
		return errors.New("invalid remediation manifest")
	}
	suggest, err := configure(manifest)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+manifest.Runtime.HandshakePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pluginruntime.HandshakeResponse{
			PluginID: manifest.PluginID, PluginVersion: manifest.PluginVersion,
			APIContract: pluginruntime.SupportedAPIContractV2, Status: "ready",
			Capabilities: []string{pluginruntime.CapabilityErrorRemediation},
		})
	})
	mux.HandleFunc("GET "+manifest.Runtime.HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST "+remediation.SuggestPath, suggest)
	socket := os.Getenv("AI_SESSION_MANAGER_PLUGIN_SOCKET")
	if !filepath.IsAbs(socket) {
		return errors.New("private plugin socket must be absolute")
	}
	// A previous process can leave its socket inode after a crash.
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen on plugin socket: %w", err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	server := &http.Server{
		Handler: mux, ReadHeaderTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096,
	}
	return server.Serve(listener)
}
