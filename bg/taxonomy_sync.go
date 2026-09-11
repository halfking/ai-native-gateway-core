package bg

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

type TaxonomyFamily struct {
	ID          string            `yaml:"id"`
	DisplayName string            `yaml:"display_name"`
	Vendor      string            `yaml:"vendor"`
	Versions    []TaxonomyVersion `yaml:"versions"`
}

type TaxonomyVersion struct {
	CanonicalName string   `yaml:"canonical_name"`
	DisplayName   string   `yaml:"display_name"`
	Modality      string   `yaml:"modality"`
	ContextWindow int      `yaml:"context_window"`
	ParametersB   *float64 `yaml:"parameters_b"`
	Aliases       []string `yaml:"aliases"`
	Capabilities  []string `yaml:"capabilities"`
}

type TaxonomyDoc struct {
	Families []TaxonomyFamily `yaml:"families"`
}

type TaxonomySync struct {
	db       *pgxpool.Pool
	interval time.Duration
	yamlPath string
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewTaxonomySync(db *pgxpool.Pool, yamlPath string) *TaxonomySync {
	if yamlPath == "" {
		yamlPath = "data/model_taxonomy.yaml"
	}
	return &TaxonomySync{
		db:       db,
		interval: 6 * time.Hour,
		yamlPath: yamlPath,
		done:     make(chan struct{}),
	}
}

func (t *TaxonomySync) Start(ctx context.Context) {
	ctx, t.cancel = context.WithCancel(ctx)
	go t.run(ctx)
	slog.Info("taxonomy sync started", "interval", t.interval, "path", t.yamlPath)
}

func (t *TaxonomySync) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
	<-t.done
}

func (t *TaxonomySync) run(ctx context.Context) {
	defer close(t.done)

	t.sync(ctx)

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.sync(ctx)
		}
	}
}

func (t *TaxonomySync) sync(ctx context.Context) {
	doc, err := t.loadYAML()
	if err != nil {
		slog.Warn("taxonomy sync: load failed", "error", err)
		return
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	synced := 0
	aliasesSynced := 0

	for _, fam := range doc.Families {
		for _, ver := range fam.Versions {
			if ver.CanonicalName == "" {
				continue
			}

			err := t.upsertCanonical(timeoutCtx, fam.ID, ver)
			if err != nil {
				slog.Debug("taxonomy sync: upsert canonical failed",
					"canonical", ver.CanonicalName, "error", err)
				continue
			}
			synced++

			canonicalID, err := t.getCanonicalID(timeoutCtx, ver.CanonicalName)
			if err != nil || canonicalID == 0 {
				continue
			}

			for _, alias := range ver.Aliases {
				if alias == "" {
					continue
				}
				err := t.upsertAlias(timeoutCtx, canonicalID, alias, ver.CanonicalName)
				if err == nil {
					aliasesSynced++
				}
			}
		}
	}

	slog.Info("taxonomy sync completed",
		"canonical_models", synced,
		"aliases", aliasesSynced,
	)
}

func (t *TaxonomySync) loadYAML() (*TaxonomyDoc, error) {
	data, err := os.ReadFile(t.yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &TaxonomyDoc{Families: []TaxonomyFamily{}}, nil
		}
		return nil, err
	}

	var doc TaxonomyDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	return &doc, nil
}

func (t *TaxonomySync) upsertCanonical(ctx context.Context, familyID string, ver TaxonomyVersion) error {
	modality := ver.Modality
	if modality == "" {
		modality = "text"
	}
	contextWindow := ver.ContextWindow
	if contextWindow == 0 {
		contextWindow = 8192
	}

	_, err := t.db.Exec(ctx, `
		INSERT INTO models_canonical (canonical_name, family, display_name, modality, context_window, parameters_b, source, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'taxonomy-yaml', 'active')
		ON CONFLICT (canonical_name) DO UPDATE SET
			family = EXCLUDED.family,
			display_name = EXCLUDED.display_name,
			modality = EXCLUDED.modality,
			context_window = EXCLUDED.context_window,
			parameters_b = EXCLUDED.parameters_b,
			source = 'taxonomy-yaml'
	`, ver.CanonicalName, familyID, ver.DisplayName, modality, contextWindow, ver.ParametersB)

	return err
}

func (t *TaxonomySync) upsertAlias(ctx context.Context, canonicalID int, rawName, canonicalName string) error {
	// 2026-09-12: ON CONFLICT (raw_name) never worked — model_aliases has no
	// unique index on raw_name (only uq_model_aliases_canonical_raw on
	// (canonical_id, raw_name), added in migration 357), so every alias
	// upsert failed with 42P10 and the caller swallowed the error ("taxonomy
	// sync completed ... aliases 0" even with a populated YAML).
	//
	// Semantics "taxonomy is authoritative", adapted to the real constraint:
	// the taxonomy mapping is inserted/reactivated (arbiter
	// (canonical_id, raw_name)), then competing rows for the same raw_name
	// pointing at other canonicals are demoted to 'disabled' rather than
	// repointed — repointing N duplicate rows onto one canonical is
	// impossible under the pair constraint (23505, observed live) and
	// demotion keeps the rows for ops review while shrinking the ambiguity
	// the resolver ORDER BY root-fix has to tolerate ('disabled' is in the
	// model_aliases_status_check vocabulary and invisible to the resolver's
	// COALESCE(status,'active')='active' filter; 'inactive' is NOT a legal
	// value and silently fails the check constraint). Two operator-intent
	// guards: a 'disabled' taxonomy target is never reactivated (an
	// explicit operator kill beats the 6h sync; the admin create path has
	// no such guard because the API call itself is the operator acting),
	// and only ACTIVE competitors are demoted ('deprecated'/'hidden' rows
	// are already invisible — relabeling them is pointless churn).
	//
	// Two plain statements on purpose: a WITH ... DO UPDATE ... RETURNING
	// CTE guarded by NOT EXISTS is unsafe here — data-modifying CTEs run
	// concurrently with the main query on the same snapshot, so the guard
	// can fire before the UPDATE's RETURNING rows land. Order matters:
	// the authoritative row must exist before competitors are demoted, so a
	// crash between the two leaves the taxonomy mapping active, never the
	// competitor. The pair is racy only against other writers in the
	// microseconds between the statements; the sync runs every 6h on a
	// single control-plane node.
	if _, err := t.db.Exec(ctx, `
		INSERT INTO model_aliases (raw_name, canonical_id, status)
		VALUES (lower($1), $2, 'active')
		ON CONFLICT (canonical_id, raw_name) DO UPDATE SET
			status = 'active',
			updated_at = now()
		WHERE model_aliases.status <> 'disabled'
	`, rawName, canonicalID); err != nil {
		return err
	}
	_, err := t.db.Exec(ctx, `
		UPDATE model_aliases
		SET status = 'disabled', updated_at = now()
		WHERE raw_name = lower($1)
		  AND canonical_id <> $2
		  AND status = 'active'
	`, rawName, canonicalID)
	return err
}

func (t *TaxonomySync) getCanonicalID(ctx context.Context, canonicalName string) (int, error) {
	var id int
	err := t.db.QueryRow(ctx, `SELECT id FROM models_canonical WHERE canonical_name = $1`, canonicalName).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (t *TaxonomySync) RunOnce(ctx context.Context) error {
	t.sync(ctx)
	return nil
}

func (t *TaxonomySync) Status() map[string]any {
	return map[string]any{
		"interval_s": int(t.interval.Seconds()),
		"yaml_path":  t.yamlPath,
		"exists":     fileExists(t.yamlPath),
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func DiscoverYamlPath() string {
	candidates := []string{
		"data/model_taxonomy.yaml",
		"../../services/llm-gateway/data/model_taxonomy.yaml",
		filepath.Join(os.Getenv("PWD"), "data/model_taxonomy.yaml"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "data/model_taxonomy.yaml"
}

func InitTaxonomySync(db *pgxpool.Pool) *TaxonomySync {
	yamlPath := DiscoverYamlPath()
	svc := NewTaxonomySync(db, yamlPath)
	return svc
}
