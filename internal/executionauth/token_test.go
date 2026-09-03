package executionauth_test

import (
	"context"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/executionauth"
)

func TestAccessTokenIsValidatedAndPropagatedWithoutRequestValues(t *testing.T) {
	type unrelatedKey struct{}
	requestContext := context.WithValue(context.Background(), unrelatedKey{}, "request-only")
	requestContext, err := executionauth.WithAccessToken(requestContext, "opaque.token-value")
	if err != nil {
		t.Fatal(err)
	}
	jobContext := executionauth.PropagateAccessToken(context.Background(), requestContext)
	token, ok := executionauth.AccessToken(jobContext)
	if !ok || token != "opaque.token-value" {
		t.Fatalf("AccessToken() = %q, %v", token, ok)
	}
	if jobContext.Value(unrelatedKey{}) != nil {
		t.Fatal("request-only context value leaked into the job context")
	}
	masked := executionauth.WithoutAccessToken(requestContext)
	if token, ok := executionauth.AccessToken(masked); ok || token != "" {
		t.Fatalf("masked AccessToken() = %q, %v", token, ok)
	}
	if masked.Value(unrelatedKey{}) != "request-only" {
		t.Fatal("masking the token removed a non-secret request value")
	}
}

func TestAccessTokenRejectsUnsafeValues(t *testing.T) {
	for _, token := range []string{"", " leading", "trailing ", "line\nbreak", "non-ascii-令牌", strings.Repeat("x", (16<<10)+1)} {
		if _, err := executionauth.WithAccessToken(context.Background(), token); err == nil {
			t.Fatalf("unsafe access token of length %d was accepted", len(token))
		}
	}
}
