package policy

import "testing"

func TestDenyWins(t *testing.T) {
	engine := New([]Rule{
		{ID: "allow", Effect: EffectAllow, OrganizationID: "org", PrincipalID: "agent", OperationPrefix: "linear.", Priority: 100},
		{ID: "deny-write", Effect: EffectDeny, OrganizationID: "org", Risk: "write"},
	})
	got := engine.Evaluate(Input{OrganizationID: "org", PrincipalID: "agent", RequesterID: "user", Operation: "linear.update", Risk: "write"})
	if got.Effect != EffectDeny || got.Reason != "policy.explicit_deny" {
		t.Fatalf("deny did not win: %#v", got)
	}
}

func TestAmbientCannotAuthorizeWrite(t *testing.T) {
	engine := New([]Rule{{ID: "allow", Effect: EffectAllow, OrganizationID: "org", PrincipalID: "agent", OperationPrefix: "linear."}})
	got := engine.Evaluate(Input{OrganizationID: "org", PrincipalID: "agent", Operation: "linear.update", Risk: "write", Ambient: true})
	if got.Effect != EffectDeny || got.Reason != "policy.ambient_write_denied" {
		t.Fatalf("ambient write allowed: %#v", got)
	}
}

func TestDefaultDenyAndApproval(t *testing.T) {
	engine := New([]Rule{{ID: "approve", Effect: EffectRequireApproval, OrganizationID: "org", PrincipalID: "agent", OperationPrefix: "deploy."}})
	if got := engine.Evaluate(Input{OrganizationID: "org", PrincipalID: "agent", Operation: "unknown.read", Risk: "read"}); got.Effect != EffectDeny {
		t.Fatalf("default allowed: %#v", got)
	}
	if got := engine.Evaluate(Input{OrganizationID: "org", PrincipalID: "agent", RequesterID: "user", Operation: "deploy.start", Risk: "write"}); got.Effect != EffectRequireApproval {
		t.Fatalf("approval not required: %#v", got)
	}
}
