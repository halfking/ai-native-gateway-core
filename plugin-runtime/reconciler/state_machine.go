package reconciler

// StateTransition represents a valid state transition.
type StateTransition struct {
	From   State
	To     State
	Action Action
}

// ValidTransitions defines all valid state transitions in the plugin lifecycle.
var ValidTransitions = []StateTransition{
	{StatePending, StateStarting, ActionStart},
	{StateStarting, StateRunning, ActionNone},
	{StateStarting, StateFailed, ActionNone},
	{StateRunning, StateStopping, ActionStop},
	{StateRunning, StateFailed, ActionNone},
	{StateStopping, StateStopped, ActionNone},
	{StateStopped, StateStarting, ActionStart},
	{StateFailed, StateStarting, ActionRestart},
	{StateFailed, StateDisabled, ActionDisable},
	{StateRunning, StateStarting, ActionUpgrade},
}

// IsValidTransition checks if a state transition is valid.
func IsValidTransition(from, to State) bool {
	for _, t := range ValidTransitions {
		if t.From == from && t.To == to {
			return true
		}
	}
	return false
}

// GetTransitionAction returns the action needed for a state transition.
// Returns ActionNone if the transition is invalid.
func GetTransitionAction(from, to State) Action {
	for _, t := range ValidTransitions {
		if t.From == from && t.To == to {
			return t.Action
		}
	}
	return ActionNone
}

// CanTransition checks if a plugin can transition from one state to another.
func CanTransition(from, to State) bool {
	return IsValidTransition(from, to)
}
