package relay

import (
	"encoding/json"
	"regexp"
	"strings"
)

const upstreamRefused = "The selected upstream rejected or failed the request"

// Account, project, key and request identifiers an upstream message may name,
// such as org-…, proj_…, user-…, sk-… and req_…. A match without a digit or a
// mask character is an ordinary word like "user-supplied" and is kept.
var upstreamIdentifiers = regexp.MustCompile(`\b(?:org|proj|user|sk|req|acct)[-_][A-Za-z0-9_*.\-]{2,}`)

func redactIdentifiers(s string) string {
	return upstreamIdentifiers.ReplaceAllStringFunc(s, func(m string) string {
		if !strings.ContainsAny(m, "0123456789*") {
			return m
		}
		id := strings.TrimRight(m, ".-")
		return "[redacted]" + m[len(id):]
	})
}

// upstreamError keeps a provider's explanation of a request it refused as
// invalid, so the client can fix the request. OpenAI and Anthropic both put
// it in error.message. Identifiers of the upstream account are removed, and
// the message is bounded. Without a usable message the reply stays generic.
func upstreamError(raw []byte) string {
	var body struct {
		Error struct {
			Message string          `json:"message"`
			Param   json.RawMessage `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return upstreamRefused
	}
	message := strings.Join(strings.Fields(redactIdentifiers(body.Error.Message)), " ")
	if message == "" {
		return upstreamRefused
	}
	if runes := []rune(message); len(runes) > 500 {
		message = string(runes[:500]) + "…"
	}
	var param string
	if json.Unmarshal(body.Error.Param, &param) == nil && param != "" && len(param) <= 64 && !strings.ContainsAny(param, " \n") {
		message += " (parameter: " + param + ")"
	}
	return "The upstream rejected the request: " + message
}
