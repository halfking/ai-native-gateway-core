package routingopt

// ml_manifest.go — P2.5: ML模型清单（manifest）定义与加载。
//
// manifest.json由训练端（ml-training/src/export_model.py write_manifest）
// 随model.onnx一同导出，是训练端与Go推理端之间的唯一契约：
//   - ONNX输入的名称与顺序（categorical → boolean → numeric 拼接）
//   - 标签类目（probabilities张量的列序）
//   - 缺失值哨兵（与 ml-training/src/data_loader.normalize_features 一致）
//
// 契约文档: docs/ml/p2.5-go-onnx-inference.md

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MLManifestSchemaV1 is the manifest schema this code understands.
const MLManifestSchemaV1 = "v1"

// mlMissingCategory is the sentinel for missing/unknown categorical values.
// Must match ml-training MISSING_CATEGORY.
const mlMissingCategory = "__missing__"

// mlBooleanMissing is the sentinel for a missing boolean feature.
// Must match ml-training normalize_features (True=1, False=0, null=-1).
const mlBooleanMissing = int64(-1)

// MLManifest describes a trained ONNX routing model.
type MLManifest struct {
	// SchemaVersion is the feature/manifest schema version ("v1").
	SchemaVersion string `json:"schema_version"`

	// ModelFile is the ONNX file name, resolved relative to the manifest.
	ModelFile string `json:"model_file"`

	// LabelClasses lists the predicted labels in probability column order.
	LabelClasses []string `json:"label_classes"`

	// Features lists the ONNX input columns per type, in export order.
	Features MLFeatureColumns `json:"features"`

	// OutputLabelName / OutputProbabilityName override the ONNX output
	// tensor names when present (defaults: "label", "probabilities").
	OutputLabelName       string `json:"output_label_name,omitempty"`
	OutputProbabilityName string `json:"output_probability_name,omitempty"`

	// MissingSentinels documents the training-side missing-value contract.
	MissingSentinels struct {
		Categorical     string `json:"categorical"`
		BooleanMissing  int64  `json:"boolean_missing"`
		Numeric         string `json:"numeric"`
	} `json:"missing_sentinels"`
}

// MLFeatureColumns mirrors the training config's features section.
// ONNX input order is categorical → boolean → numeric.
type MLFeatureColumns struct {
	Categorical []string `json:"categorical"`
	Boolean     []string `json:"boolean"`
	Numeric     []string `json:"numeric"`
}

// InputNames returns all ONNX input names in export order.
func (m *MLManifest) InputNames() []string {
	out := make([]string, 0, m.NumInputs())
	out = append(out, m.Features.Categorical...)
	out = append(out, m.Features.Boolean...)
	out = append(out, m.Features.Numeric...)
	return out
}

// NumInputs returns the total ONNX input count (must be 17 for schema v1).
func (m *MLManifest) NumInputs() int {
	return len(m.Features.Categorical) + len(m.Features.Boolean) + len(m.Features.Numeric)
}

// ModelPath resolves the ONNX file path relative to the manifest location.
func (m *MLManifest) ModelPath(manifestPath string) string {
	if filepath.IsAbs(m.ModelFile) {
		return m.ModelFile
	}
	return filepath.Join(filepath.Dir(manifestPath), m.ModelFile)
}

// validate checks the manifest is structurally sound for inference.
func (m *MLManifest) validate() error {
	if m.SchemaVersion != MLManifestSchemaV1 {
		return fmt.Errorf("unsupported manifest schema_version %q (want %q)",
			m.SchemaVersion, MLManifestSchemaV1)
	}
	if m.ModelFile == "" {
		return fmt.Errorf("manifest missing model_file")
	}
	if len(m.LabelClasses) < 2 {
		return fmt.Errorf("manifest needs >= 2 label_classes, got %d", len(m.LabelClasses))
	}
	if m.NumInputs() == 0 {
		return fmt.Errorf("manifest lists no feature inputs")
	}
	return nil
}

// LoadMLManifest reads and validates a manifest.json.
func LoadMLManifest(path string) (*MLManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m MLManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.OutputLabelName == "" {
		m.OutputLabelName = "label"
	}
	if m.OutputProbabilityName == "" {
		m.OutputProbabilityName = "probabilities"
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	return &m, nil
}
