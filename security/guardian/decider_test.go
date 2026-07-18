package guardian

import (
	"testing"
)

func TestGuardDecider_DefaultMode(t *testing.T) {
	d := NewGuardDecider(ModeObserve)
	mode := d.Mode("any-tenant", "any-guard")
	if mode != ModeObserve {
		t.Fatalf("mode = %v, want observe", mode)
	}
}

func TestGuardDecider_DefaultMode_Empty(t *testing.T) {
	d := NewGuardDecider("")
	mode := d.Mode("x", "y")
	if mode != ModeObserve {
		t.Fatalf("mode = %v, want observe", mode)
	}
}

func TestGuardDecider_TenantOverride(t *testing.T) {
	d := NewGuardDecider(ModeObserve)
	d.SetTenantPolicy("t-1", &TenantGuardPolicy{Mode: ModeBlock})

	if got := d.Mode("t-1", "guard-a"); got != ModeBlock {
		t.Fatalf("t-1 mode = %v, want block", got)
	}
	if got := d.Mode("t-2", "guard-a"); got != ModeObserve {
		t.Fatalf("t-2 mode = %v, want observe", got)
	}
}

func TestGuardDecider_CheckOverride(t *testing.T) {
	d := NewGuardDecider(ModeObserve)
	d.SetTenantPolicy("t-1", &TenantGuardPolicy{
		Mode:           ModeBlock,
		CheckOverrides: map[string]GuardMode{"skip-check": ModeObserve},
	})

	if got := d.Mode("t-1", "guard-a"); got != ModeBlock {
		t.Fatalf("guard-a mode = %v, want block", got)
	}
	if got := d.Mode("t-1", "skip-check"); got != ModeObserve {
		t.Fatalf("skip-check mode = %v, want observe", got)
	}
}

func TestGuardDecider_SkipChecks(t *testing.T) {
	d := NewGuardDecider(ModeBlock)
	d.SetTenantPolicy("t-1", &TenantGuardPolicy{
		Mode:       ModeBlock,
		SkipChecks: []string{"expensive-check"},
	})

	if got := d.Mode("t-1", "expensive-check"); got != ModeObserve {
		t.Fatalf("skipped check mode = %v, want observe", got)
	}
}

func TestGuardDecider_DeleteTenant(t *testing.T) {
	d := NewGuardDecider(ModeObserve)
	d.SetTenantPolicy("t-1", &TenantGuardPolicy{Mode: ModeBlock})
	d.DeleteTenantPolicy("t-1")

	if got := d.Mode("t-1", "guard-a"); got != ModeObserve {
		t.Fatalf("after delete, mode = %v, want observe", got)
	}
}

func TestGuardDecider_SetDefaultMode(t *testing.T) {
	d := NewGuardDecider(ModeObserve)
	d.SetDefaultMode(ModeBlock)

	if got := d.Mode("any", "any"); got != ModeBlock {
		t.Fatalf("mode = %v, want block", got)
	}
}

func TestGuardDecider_Concurrency(t *testing.T) {
	d := NewGuardDecider(ModeObserve)

	done := make(chan struct{})
	go func() {
		d.SetTenantPolicy("t-1", &TenantGuardPolicy{Mode: ModeBlock})
		done <- struct{}{}
	}()
	go func() {
		d.Mode("t-1", "guard-a")
		done <- struct{}{}
	}()
	go func() {
		d.DeleteTenantPolicy("t-1")
		done <- struct{}{}
	}()

	for i := 0; i < 3; i++ {
		<-done
	}
}

func TestTenantPolicy_ShouldSkip(t *testing.T) {
	p := &TenantGuardPolicy{
		SkipChecks: []string{"a", "b"},
	}
	if !p.ShouldSkip("a") {
		t.Fatal("should skip a")
	}
	if p.ShouldSkip("c") {
		t.Fatal("should not skip c")
	}
}

func TestTenantPolicy_EffectiveMode_Nil(t *testing.T) {
	var p *TenantGuardPolicy
	if mode := p.EffectiveMode("any"); mode != ModeObserve {
		t.Fatalf("nil policy mode = %v, want observe", mode)
	}
}
