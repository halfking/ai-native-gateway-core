package settings

// AutoControlSpecs returns all configuration specs for automatic session control features.
func AutoControlSpecs() []Spec {
	specs := make([]Spec, 0)
	specs = append(specs, HandoffSpecs()...)
	specs = append(specs, GoalSpecs()...)
	return specs
}
