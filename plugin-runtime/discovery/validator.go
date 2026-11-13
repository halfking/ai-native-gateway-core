package discovery

import (
	"fmt"
	"regexp"
)

// Validator validates plugin manifests.
type Validator struct {
	namePattern    *regexp.Regexp
	versionPattern *regexp.Regexp
}

// NewValidator creates a new manifest validator.
func NewValidator() *Validator {
	return &Validator{
		namePattern:    regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`),
		versionPattern: regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[a-z0-9.]+)?$`),
	}
}

// Validate validates a manifest.
func (v *Validator) Validate(m *Manifest) error {
	// Check API version
	if m.APIVersion == "" {
		return fmt.Errorf("apiVersion is required")
	}
	if m.APIVersion != "llm-gateway.io/v1" {
		return fmt.Errorf("unsupported apiVersion: %s", m.APIVersion)
	}
	
	// Check kind
	if m.Kind != "Plugin" {
		return fmt.Errorf("kind must be 'Plugin', got: %s", m.Kind)
	}
	
	// Validate metadata
	if err := v.validateMetadata(&m.Metadata); err != nil {
		return fmt.Errorf("metadata validation failed: %w", err)
	}
	
	// Validate spec
	if err := v.validateSpec(&m.Spec); err != nil {
		return fmt.Errorf("spec validation failed: %w", err)
	}
	
	return nil
}

func (v *Validator) validateMetadata(m *Metadata) error {
	// Validate name
	if m.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !v.namePattern.MatchString(m.Name) {
		return fmt.Errorf("invalid name format: %s (must match [a-z0-9]([a-z0-9-]*[a-z0-9])?)", m.Name)
	}
	
	// Validate version
	if m.Version == "" {
		return fmt.Errorf("version is required")
	}
	if !v.versionPattern.MatchString(m.Version) {
		return fmt.Errorf("invalid version format: %s (must be semantic version)", m.Version)
	}
	
	// Description is required
	if m.Description == "" {
		return fmt.Errorf("description is required")
	}
	
	return nil
}

func (v *Validator) validateSpec(s *Spec) error {
	// Validate plugin type
	switch s.Type {
	case TypeBuiltin, TypeExternal, TypeRemote:
		// Valid types
	default:
		return fmt.Errorf("invalid type: %s", s.Type)
	}
	
	// Type-specific validation
	switch s.Type {
	case TypeExternal:
		if s.Executable == "" {
			return fmt.Errorf("executable is required for external plugins")
		}
	case TypeRemote:
		if s.Remote == nil || s.Remote.URL == "" {
			return fmt.Errorf("remote.url is required for remote plugins")
		}
	}
	
	// Validate extension points
	if len(s.ExtensionPoints) == 0 {
		return fmt.Errorf("at least one extension point is required")
	}
	
	for i, ep := range s.ExtensionPoints {
		if ep.Name == "" {
			return fmt.Errorf("extensionPoints[%d].name is required", i)
		}
		if ep.Phase == "" {
			return fmt.Errorf("extensionPoints[%d].phase is required", i)
		}
		if ep.Interface == "" {
			return fmt.Errorf("extensionPoints[%d].interface is required", i)
		}
		
		// Validate phase (must be one of the 16 phases)
		if !isValidPhase(ep.Phase) {
			return fmt.Errorf("extensionPoints[%d].phase is invalid: %s", i, ep.Phase)
		}
	}
	
	return nil
}

// isValidPhase checks if a phase name is valid (one of the 16 pipeline phases).
func isValidPhase(phase string) bool {
	validPhases := map[string]bool{
		"pre_authentication":  true,
		"authentication":      true,
		"post_authentication": true,
		"pre_routing":         true,
		"routing":             true,
		"post_routing":        true,
		"governance":          true,
		"pre_transform":       true,
		"transform":           true,
		"post_transform":      true,
		"pre_upstream":        true,
		"upstream":            true,
		"post_upstream":       true,
		"pre_response":        true,
		"response":            true,
		"post_response":       true,
	}
	return validPhases[phase]
}
