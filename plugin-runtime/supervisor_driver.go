package pluginruntime

import (
	"context"
	"fmt"
	"path/filepath"
)

// SupervisorDriver adapts Supervisor (+ readiness callback) to the Reconcile Driver.
// Apply resolves entrypoint relative to ManifestPath's directory, starts the
// process, then runs Ready. Dispose stops the process.
type SupervisorDriver struct {
	Sup   *Supervisor
	Ready func(ctx context.Context, socketPath string, m *Manifest) error
}

func (d SupervisorDriver) Apply(m *Manifest, _ map[string]any) error {
	if d.Sup == nil || m == nil {
		return fmt.Errorf("supervisor driver: supervisor and manifest required")
	}
	cp := *m
	if cp.ManifestPath != "" && cp.Runtime.Entrypoint != "" && !filepath.IsAbs(cp.Runtime.Entrypoint) {
		cp.Runtime.Entrypoint = filepath.Join(filepath.Dir(cp.ManifestPath), cp.Runtime.Entrypoint)
	}
	st, err := d.Sup.Start(context.Background(), &cp)
	if err != nil {
		return err
	}
	if d.Ready != nil {
		if err := d.Ready(context.Background(), st.SocketPath, &cp); err != nil {
			_ = d.Sup.Stop(m.PluginID)
			return err
		}
	}
	return nil
}

func (d SupervisorDriver) Dispose(pluginID string) error {
	if d.Sup == nil {
		return fmt.Errorf("supervisor driver: nil supervisor")
	}
	err := d.Sup.Stop(pluginID)
	if err != nil && err.Error() == fmt.Sprintf("plugin %s not running", pluginID) {
		return nil
	}
	return err
}
