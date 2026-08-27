package dbx

import (
	"fmt"
	"sort"
	"sync/atomic"
)

// Registry is an immutable snapshot of validated table manifests. Build it
// once at host startup; there is no runtime mutation. The version counter
// lets hosts invalidate metadata caches when a new Registry replaces an old
// one.
type Registry struct {
	tables  map[string]*TableManifest
	version uint64
}

var registryEpoch atomic.Uint64

// NewRegistry validates and registers all manifests. Duplicate table names
// and invalid manifests are construction errors, never silent.
func NewRegistry(manifests ...TableManifest) (*Registry, error) {
	tables := make(map[string]*TableManifest, len(manifests))
	for i := range manifests {
		norm, err := manifests[i].normalize()
		if err != nil {
			return nil, err
		}
		if _, dup := tables[norm.Table]; dup {
			return nil, fmt.Errorf("dbx: registry: duplicate manifest for table %q", norm.Table)
		}
		normalized := norm
		tables[normalized.Table] = &normalized
	}
	return &Registry{
		tables:  tables,
		version: registryEpoch.Add(1),
	}, nil
}

// Lookup returns the manifest for a registered table, or ErrUnknownTable.
// The returned manifest is a defensive copy: mutating it never affects the
// registry. The copy still shares the immutable column slices/byName map -
// treat everything reachable from it as read-only.
func (r *Registry) Lookup(table string) (*TableManifest, error) {
	if r == nil {
		return nil, fmt.Errorf("dbx: registry: nil registry: %w", ErrUnknownTable)
	}
	m, ok := r.tables[table]
	if !ok {
		return nil, fieldError(ErrUnknownTable, table, "")
	}
	cp := *m
	return &cp, nil
}

// Tables returns registered table names in sorted order.
func (r *Registry) Tables() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.tables))
	for name := range r.tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Version returns the snapshot version (monotonic across constructions).
func (r *Registry) Version() uint64 {
	if r == nil {
		return 0
	}
	return r.version
}
