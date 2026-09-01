package policy

import (
	"errors"
	"testing"

	"github.com/marmot1024/metricspire/internal/model"
)

func TestAuthorizeFailsClosedAndHonorsExplicitDeny(t *testing.T) {
	t.Parallel()
	query := model.SemanticQuery{
		ManifestFingerprint: "manifest", Metrics: []string{"revenue"}, GroupBy: []string{"region"},
	}
	context := model.RequestContext{Tenant: "demo", Principal: "alice", Roles: []string{"analyst"}, RequestID: "r1"}
	base := model.PolicyBundle{
		ManifestFingerprint: "manifest", Tenant: "demo",
		Rules: []model.PolicyRule{{Name: "allow", Effect: model.EffectAllow, Roles: []string{"analyst"}, Metrics: []string{"revenue"}, Dimensions: []string{"region"}}},
	}

	t.Run("allow", func(t *testing.T) {
		decision, err := Authorize(context, base, query)
		if err != nil || !decision.Allowed {
			t.Fatalf("Authorize() = %#v, %v", decision, err)
		}
	})
	t.Run("no matching rule", func(t *testing.T) {
		candidate := base
		candidate.Rules = nil
		_, err := Authorize(context, candidate, query)
		assertDenied(t, err)
	})
	t.Run("tenant mismatch", func(t *testing.T) {
		candidate := context
		candidate.Tenant = "other"
		_, err := Authorize(candidate, base, query)
		assertDenied(t, err)
	})
	t.Run("incomplete trusted context", func(t *testing.T) {
		candidate := context
		candidate.RequestID = ""
		_, err := Authorize(candidate, base, query)
		assertDenied(t, err)
	})
	t.Run("explicit deny wins", func(t *testing.T) {
		candidate := base
		candidate.Rules = append(candidate.Rules, model.PolicyRule{
			Name: "deny_region", Effect: model.EffectDeny, Principals: []string{"alice"}, Metrics: []string{"other"}, Dimensions: []string{"region"},
		})
		_, err := Authorize(context, candidate, query)
		assertDenied(t, err)
	})
}

func assertDenied(t *testing.T, err error) {
	t.Helper()
	var problem *model.Problem
	if !errors.As(err, &problem) || problem.Code != "permission_denied" {
		t.Fatalf("error = %v, want permission_denied", err)
	}
}
