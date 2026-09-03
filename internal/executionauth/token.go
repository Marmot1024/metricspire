// Package executionauth carries an analytical-engine credential through one
// in-memory execution context. The credential is deliberately absent from
// request models, job snapshots, audit events, and persistence contracts.
package executionauth

import (
	"context"
	"errors"
)

const maximumAccessTokenBytes = 16 << 10

type accessTokenKey struct{}
type accessTokenRemoved struct{}

// WithAccessToken returns a derived context containing one validated opaque
// access token. Callers must never log, format, serialize, or persist the token.
func WithAccessToken(ctx context.Context, token string) (context.Context, error) {
	if ctx == nil {
		return nil, errors.New("execution context is required")
	}
	if err := validateAccessToken(token); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, accessTokenKey{}, token), nil
}

// AccessToken returns the opaque access token carried by ctx, if any.
func AccessToken(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	token, ok := ctx.Value(accessTokenKey{}).(string)
	return token, ok && token != ""
}

// WithoutAccessToken masks an inherited execution token while preserving
// cancellation, deadlines, and non-secret request values. Use it before
// invoking control-plane, policy, or audit dependencies.
func WithoutAccessToken(ctx context.Context) context.Context {
	if ctx == nil {
		panic("executionauth: nil context")
	}
	return context.WithValue(ctx, accessTokenKey{}, accessTokenRemoved{})
}

// PropagateAccessToken copies only the execution token from source to parent.
// Request cancellation, deadlines, and unrelated request values are not
// propagated into the manager-owned asynchronous job context.
func PropagateAccessToken(parent, source context.Context) context.Context {
	if parent == nil {
		panic("executionauth: nil parent context")
	}
	token, ok := AccessToken(source)
	if !ok {
		return parent
	}
	return context.WithValue(parent, accessTokenKey{}, token)
}

func validateAccessToken(token string) error {
	if len(token) < 1 || len(token) > maximumAccessTokenBytes {
		return errors.New("execution access token has an invalid size")
	}
	for index := range len(token) {
		character := token[index]
		if character <= 0x20 || character >= 0x7f {
			return errors.New("execution access token must contain only visible ASCII characters")
		}
	}
	return nil
}
