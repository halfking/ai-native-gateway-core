package errorsx

import "testing"

func TestDecideNextActionUsesHistoryToPreventLoops(t *testing.T) {
	base := DecisionContext{
		ResolvedModel: "m", ProviderID: 1, CredentialID: 7,
		Kind: KindEmptyResponse, MaxSameNodeRetries: 2, RemainingAttempts: 3, HasAlternateNode: true,
	}
	got := DecideNextAction(base)
	if got.Action != ActionRetrySameNode || got.AlreadyAttempted {
		t.Fatalf("fresh node decision = %+v, want retry without prior attempt", got)
	}

	base.History = DecisionHistory{
		LastSeq: 2,
		PriorAttempts: []PriorAttempt{
			{Seq: 1, AttemptNo: 1, Model: "m", ProviderID: 1, CredentialID: 7, Kind: KindEmptyResponse, Action: ActionRetrySameNode},
		},
	}
	got = DecideNextAction(base)
	if got.Action != ActionRetrySameNode || !got.AlreadyAttempted || got.LoopDetected {
		t.Fatalf("prior retry decision = %+v, want bounded same-node retry", got)
	}

	base.History.TriedNodes = []ActionNode{{Model: "m", ProviderID: 1, CredentialID: 7}}
	got = DecideNextAction(base)
	if got.Action != ActionSwitchNode || !got.LoopDetected || !got.AvoidCurrentNode {
		t.Fatalf("exhausted node decision = %+v, want switch with loop guard", got)
	}
}

func TestDecideNextActionRejectsTerminalOrInvalidHistory(t *testing.T) {
	got := DecideNextAction(DecisionContext{
		Kind:    KindNetwork,
		History: DecisionHistory{Terminal: true, LastSeq: 1},
	})
	if got.Action != ActionFailClosed || got.ReasonCode != "history_terminal" {
		t.Fatalf("terminal history decision = %+v", got)
	}
	got = DecideNextAction(DecisionContext{
		Kind:    KindNetwork,
		History: DecisionHistory{PriorAttempts: []PriorAttempt{{Seq: 2}, {Seq: 1}}},
	})
	if got.Action != ActionFailClosed || got.ReasonCode != "history_seq_not_monotonic" {
		t.Fatalf("invalid history decision = %+v", got)
	}
}
