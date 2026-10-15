package mcpbridge

import "context"

// DomainView contains logical names only. Repository URLs, credentials,
// paths and physical bindings are deliberately absent from this contract.
type DomainView struct {
	Namespace   string   `json:"namespace"`
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases,omitempty"`
}

type DomainBackend interface {
	ListBusinessDomains(context.Context) ([]DomainView, error)
}
