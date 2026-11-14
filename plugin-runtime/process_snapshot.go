package pluginruntime

// ProcessSnapshot reports copies of started process records, including the
// version actually launched rather than the latest manifest on disk.
func (s *Supervisor) ProcessSnapshot() []PluginState {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]PluginState, 0, len(s.states))
	for id, state := range s.states {
		copy := *state
		if cmd, ok := s.procs[id].(interface{ Running() bool }); ok && !cmd.Running() {
			copy.Status = "crashed"
		}
		states = append(states, copy)
	}
	return states
}

func (e *execCommand) Running() bool {
	select {
	case <-e.done:
		return false
	default:
		return true
	}
}
