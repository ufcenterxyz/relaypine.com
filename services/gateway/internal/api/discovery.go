package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/relay"
	"relaypine.com/gateway/internal/store"
)

type discoveryInput struct {
	ID          string `json:"id"`
	EndpointID  string `json:"endpoint_id"`
	Secret      string `json:"secret"`
	WorkspaceID string `json:"workspace_id"`
	ProxyID     string `json:"proxy_id"`
}

// The parent admin dispatcher has already enforced mailbox-authenticated
// superadministrator access, POST, and the console's exact Origin.
func (s *Server) discoverCredential(w http.ResponseWriter, r *http.Request, user domain.User) {
	var input discoveryInput
	if read(r, &input) != nil || input.EndpointID == "" || len(input.EndpointID) > 64 || len(input.ID) > 64 || len(input.ProxyID) > 64 || len(input.Secret) > 4096 || strings.ContainsAny(input.Secret, "\r\n\x00") {
		fail(w, 400, "invalid_discovery", "Choose an endpoint and enter a valid API key, or select an existing key.")
		return
	}
	input.Secret, input.WorkspaceID = strings.TrimSpace(input.Secret), strings.TrimSpace(input.WorkspaceID)
	if !domain.ValidAnthropicWorkspaceID(input.WorkspaceID) {
		s.report(w, store.ErrInvalidWorkspaceID)
		return
	}
	if input.Secret == "" && input.ID == "" {
		fail(w, 400, "api_key_required", "Enter an API key before detecting its configuration.")
		return
	}
	if s.Redis == nil {
		fail(w, 503, "discovery_unavailable", "Discovery is temporarily unavailable. Try again later.")
		return
	}
	// Discovery is metadata-only, but bounded independently of inference so it
	// cannot become an unrestricted authenticated network probing endpoint.
	count, err := s.Redis.Eval(r.Context(), `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`, []string{"rp:discovery:" + user.ID}).Int()
	if err != nil {
		fail(w, 503, "discovery_unavailable", "Discovery is temporarily unavailable. Try again later.")
		return
	}
	if count > 10 {
		rateLimited(w, 60)
		return
	}
	endpoint, credential, proxyURL, err := s.discoveryValues(r.Context(), input)
	if err != nil {
		var problem *relay.DiscoveryError
		if errors.As(err, &problem) {
			fail(w, problem.Status, problem.Code, problem.Message)
		} else {
			s.report(w, err)
		}
		return
	}
	result, err := relay.DiscoverCredential(r.Context(), endpoint, credential, proxyURL, s.AllowPrivate)
	if err != nil {
		var problem *relay.DiscoveryError
		if errors.As(err, &problem) {
			fail(w, problem.Status, problem.Code, problem.Message)
		} else {
			fail(w, 502, "discovery_failed", "The upstream configuration could not be detected. Check the endpoint and proxy, or use Advanced settings.")
		}
		return
	}
	write(w, 200, result)
}

func (s *Server) discoveryValues(ctx context.Context, input discoveryInput) (domain.Endpoint, domain.Credential, string, error) {
	var endpoint domain.Endpoint
	credential := domain.Credential{ID: input.ID, EndpointID: input.EndpointID, WorkspaceID: input.WorkspaceID, Secret: input.Secret, ProxyID: input.ProxyID}
	endpoints, err := s.Store.ListEndpoints(ctx)
	if err != nil {
		return endpoint, credential, "", err
	}
	for _, candidate := range endpoints {
		if candidate.ID == input.EndpointID {
			endpoint = candidate
			break
		}
	}
	if endpoint.ID == "" {
		return endpoint, credential, "", store.ErrNotFound
	}
	if input.WorkspaceID != "" && endpoint.Provider != "anthropic" {
		return endpoint, credential, "", store.ErrWorkspaceProviderMismatch
	}
	if credential.Secret == "" {
		credentials, err := s.Store.ListCredentials(ctx)
		if err != nil {
			return endpoint, credential, "", err
		}
		for _, saved := range credentials {
			if saved.ID != input.ID {
				continue
			}
			if saved.EndpointID != input.EndpointID {
				return endpoint, credential, "", &relay.DiscoveryError{Status: 400, Code: "stored_key_endpoint_mismatch", Message: "To detect a different endpoint, enter its API key explicitly. A stored key can only be reused with its saved endpoint."}
			}
			credential.Secret = saved.Secret
			break
		}
		if credential.Secret == "" {
			return endpoint, credential, "", store.ErrNotFound
		}
	}
	proxyURL := ""
	if input.ProxyID != "" {
		proxies, err := s.Store.ListProxies(ctx)
		if err != nil {
			return endpoint, credential, "", err
		}
		for _, proxy := range proxies {
			if proxy.ID == input.ProxyID {
				if !proxy.Enabled {
					return endpoint, credential, "", &relay.DiscoveryError{Status: 400, Code: "proxy_disabled", Message: "Enable the selected proxy or choose another exit before detecting this key."}
				}
				proxyURL = proxy.URL
				break
			}
		}
		if proxyURL == "" {
			return endpoint, credential, "", store.ErrNotFound
		}
	}
	return endpoint, credential, proxyURL, nil
}
