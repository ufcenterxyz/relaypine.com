package domain

import "strings"

// DefaultCredential supplies local guardrails, not an assertion about provider
// capacity or balance. Unknown keys on one endpoint share a conservative bucket.
func DefaultCredential(endpoint Endpoint) Credential {
	name := strings.TrimSpace(endpoint.Name)
	if name == "" {
		name = endpoint.Provider
	}
	runes := []rune(name)
	for len(string(runes)) > 116 {
		runes = runes[:len(runes)-1]
	}
	return Credential{
		Name: string(runes) + " key", EndpointID: endpoint.ID,
		QuotaGroup: "auto:" + endpoint.ID, Enabled: true,
		RPM: 60, TPM: 100000, MaxConcurrency: 10,
	}
}
