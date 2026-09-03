package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultReadTimeout       = 20 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 60 * time.Second
	DefaultMaxHeaderBytes    = 1 << 20
)

// NewRuntimeServer applies network-level deadlines in addition to the
// per-operation contexts enforced by Server. Query execution is asynchronous,
// so a short HTTP write deadline does not terminate an accepted query job.
func NewRuntimeServer(address string, handler http.Handler) (*http.Server, error) {
	if strings.TrimSpace(address) == "" || handler == nil {
		return nil, errors.New("HTTP address and handler are required")
	}
	return &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		IdleTimeout:       DefaultIdleTimeout,
		MaxHeaderBytes:    DefaultMaxHeaderBytes,
	}, nil
}
