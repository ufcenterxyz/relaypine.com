package api

import (
	"context"
	"strings"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

// Pointer fields distinguish omission from an invalid, explicitly supplied zero.
// Creation may omit advanced settings; updates retain the full-object contract.
type credentialInput struct {
	domain.Credential
	SecretValue      string `json:"secret"`
	RPMValue         *int   `json:"rpm"`
	TPMValue         *int   `json:"tpm"`
	ConcurrencyValue *int   `json:"max_concurrency"`
	EnabledValue     *bool  `json:"enabled"`
}

func (s *Server) credentialValues(ctx context.Context, input credentialInput) (domain.Credential, error) {
	c := input.Credential
	if c.ID == "" {
		endpoints, err := s.Store.ListEndpoints(ctx)
		if err != nil {
			return c, err
		}
		var endpoint domain.Endpoint
		for _, e := range endpoints {
			if e.ID == c.EndpointID {
				endpoint = e
				break
			}
		}
		if endpoint.ID == "" {
			return c, store.ErrNotFound
		}
		defaults := domain.DefaultCredential(endpoint)
		if strings.TrimSpace(c.Name) == "" {
			c.Name = defaults.Name
		}
		if strings.TrimSpace(c.QuotaGroup) == "" {
			c.QuotaGroup = defaults.QuotaGroup
		}
		c.RPM, c.TPM, c.MaxConcurrency, c.Enabled = defaults.RPM, defaults.TPM, defaults.MaxConcurrency, defaults.Enabled
	}
	if input.RPMValue != nil {
		c.RPM = *input.RPMValue
	}
	if input.TPMValue != nil {
		c.TPM = *input.TPMValue
	}
	if input.ConcurrencyValue != nil {
		c.MaxConcurrency = *input.ConcurrencyValue
	}
	if input.EnabledValue != nil {
		c.Enabled = *input.EnabledValue
	}
	c.Secret = strings.TrimSpace(input.SecretValue)
	c.Name = strings.TrimSpace(c.Name)
	c.QuotaGroup = strings.TrimSpace(c.QuotaGroup)
	c.WorkspaceID = strings.TrimSpace(c.WorkspaceID)
	return c, nil
}
