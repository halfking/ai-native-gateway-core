package i18n

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync/atomic"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.json
var embeddedLocales embed.FS

// bundle is the active i18n.Bundle, swapped atomically when AddOverrides reloads
// external translation files. Reads via currentBundle()/localizerFor() always
// observe a fully-loaded bundle.
var bundle atomic.Pointer[i18n.Bundle]

// localizers is a cache of *i18n.Localizer per Locale. It is rebuilt whenever
// the underlying bundle changes.
var localizers atomic.Pointer[map[Locale]*i18n.Localizer]

func init() {
	// The default language of a go-i18n Bundle is the fallback tag: a missing
	// message in the requested locale is looked up there. English is both the
	// source language and the ultimate fallback, so it must always be loaded.
	b := i18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("json", jsonUnmarshal)

	// Load embedded translation files. embed.FS roots at the package dir, so
	// "locales/en.json" is the path both for embedding and for go-i18n's
	// filename-based tag inference.
	if err := loadDir(b, embeddedLocales, "locales"); err != nil {
		// A missing embedded translation is a build-time packaging bug; fail
		// loudly at startup rather than silently serving untranslated copy.
		panic(fmt.Sprintf("i18n: failed to load embedded locales: %v", err))
	}

	storeBundle(b)
}

// loadDir walks dir inside fsys and parses every *.json message file into b.
// The filename stem (e.g. "zh-CN") is the language tag go-i18n infers.
func loadDir(b *i18n.Bundle, fsys fs.FS, dir string) error {
	return fs.WalkDir(fsys, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		data, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		// go-i18n derives the language tag from the base filename, so pass the
		// path as-is (e.g. "locales/zh-CN.json").
		if _, perr := b.ParseMessageFileBytes(data, path); perr != nil {
			// Skip the English source if it is empty of messages we already
			// hold as the default — but surface any genuine parse error.
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		return nil
	})
}

// storeBundle publishes b as the active bundle and rebuilds the localizer cache.
func storeBundle(b *i18n.Bundle) {
	cache := make(map[Locale]*i18n.Localizer, len(supportedEntries))
	for _, e := range supportedEntries {
		cache[e.locale] = i18n.NewLocalizer(b, string(e.locale))
	}
	bundle.Store(b)
	localizers.Store(&cache)
}

// currentBundle returns the active bundle.
func currentBundle() *i18n.Bundle {
	return bundle.Load()
}

// localizerFor returns the cached Localizer for loc. It always returns a
// non-nil Localizer (falling back to the default-language localizer).
func localizerFor(loc Locale) *i18n.Localizer {
	cache := localizers.Load()
	if cache != nil {
		if l, ok := (*cache)[loc]; ok && l != nil {
			return l
		}
	}
	// Defensive: if the cache lookup missed, build against the live bundle.
	return i18n.NewLocalizer(currentBundle(), string(DefaultLocale))
}

// AddOverrides layers external translation files from dir on top of the
// embedded catalogs, then atomically swaps in the rebuilt bundle. Missing files
// are logged but not fatal; a malformed file is returned as an error so the
// caller can decide whether to roll back.
//
// This mirrors the gateway's config hot-reload pattern: the reload path reads
// the new files into a fresh bundle built from the embedded baseline, so
// operators can override individual messages without forking the binary.
func AddOverrides(dir string) error {
	if dir == "" {
		return nil
	}
	merged := i18n.NewBundle(language.English)
	merged.RegisterUnmarshalFunc("json", jsonUnmarshal)

	// Re-seed the merged bundle from the embedded locales so overrides are
	// truly additive (a partial override file only needs the keys it changes).
	if err := loadDir(merged, embeddedLocales, "locales"); err != nil {
		return err
	}

	// Layer external overrides from disk. osFS adapts the real filesystem so
	// loadDir can walk it with the same code path used for the embed.
	if err := loadDir(merged, osFS{}, dir); err != nil {
		return err
	}

	storeBundle(merged)
	slog.Info("i18n: loaded external locale overrides",
		"dir", dir,
		"languages", len(merged.LanguageTags()))
	return nil
}
