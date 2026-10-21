package relay

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const maxAnthropicBetaBytes = 1024
const maxAnthropicBetas = 16

// Beta capabilities can change the model, price or number of inference runs.
// Only flags covered by our text/function-tool validation and metering belong
// here. Unknown flags are refused explicitly instead of silently losing them.
func anthropicBetaHeader(header http.Header) (string, error) {
	values := header.Values("Anthropic-Beta")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) > maxAnthropicBetas {
		return "", errors.New("too many anthropic-beta features")
	}
	bytes, count := len(values)-1, 0
	seen := make(map[string]bool)
	betas := make([]string, 0, len(values))
	for _, value := range values {
		bytes += len(value)
		if bytes > maxAnthropicBetaBytes {
			return "", errors.New("anthropic-beta header exceeds 1024 bytes")
		}
		for _, field := range strings.Split(value, ",") {
			count++
			if count > maxAnthropicBetas {
				return "", errors.New("too many anthropic-beta features")
			}
			beta := strings.Trim(field, " \t")
			switch beta {
			case "task-budgets-2026-03-13", "thinking-display-updates-2026-08-18", "thinking-binding-controls-2026-08-01",
				"interleaved-thinking-2025-05-14", "fine-grained-tool-streaming-2025-05-14":
				if !seen[beta] {
					seen[beta] = true
					betas = append(betas, beta)
				}
			case "":
				return "", errors.New("anthropic-beta must contain nonempty comma-separated feature names")
			default:
				if strings.HasPrefix(beta, "server-side-fallback-") {
					return "", errors.New("server-side model fallbacks are not supported; send a separate request for each authorized model")
				}
				return "", fmt.Errorf("unsupported anthropic-beta feature: %q", beta)
			}
		}
	}
	return strings.Join(betas, ","), nil
}
