package relay

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// capacityError names the bucket and the limit that refused a request.
type capacityError struct {
	bucket limits
	// limit is "cooldown", "rpm", "tpm" or "concurrency".
	limit string
	// wait is how long until the bucket admits again: the rest of its
	// cooldown or of the minute. A concurrency slot frees whenever a running
	// request finishes, so its wait is zero.
	wait time.Duration
}

func (e *capacityError) Error() string {
	return errCapacity.Error() + ": " + e.limit
}
func (e *capacityError) Is(target error) bool { return target == errCapacity }

// refusal returns a capacity error's details. A bare errCapacity has no
// bucket and no wait.
func refusal(err error) *capacityError {
	var full *capacityError
	if errors.As(err, &full) {
		return full
	}
	return &capacityError{}
}

// retryAfter is a Retry-After value in whole seconds, at least one.
func retryAfter(d time.Duration) int64 {
	seconds := int64((d + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func sharedBucket(b limits, shared []limits) bool {
	for _, s := range shared {
		if b.scope != "" && b.scope == s.scope {
			return true
		}
	}
	return false
}

// refuseShared answers a request refused by its workspace or API key limit.
// Every route includes those buckets, so no upstream can take the request,
// and the message names the customer's own limit rather than upstream
// capacity.
func refuseShared(w http.ResponseWriter, full *capacityError) {
	b := full.bucket
	wait := retryAfter(full.wait)
	retry := "Retry in 1 second."
	if wait > 1 {
		retry = fmt.Sprintf("Retry in %d seconds.", wait)
	}
	owner := "This API key"
	if strings.HasPrefix(b.scope, "tenant:") {
		owner = "This workspace"
	}
	var message string
	switch full.limit {
	case "concurrency":
		message = fmt.Sprintf("%s already has %d requests in progress, its concurrency limit. Retry when one finishes.", owner, b.concurrency)
	case "rpm":
		message = fmt.Sprintf("%s has reached its limit of %d requests per minute. %s", owner, b.rpm, retry)
		if owner == "This API key" {
			message += " A key with a higher limit can be created in the console."
		}
	default:
		message = fmt.Sprintf("%s has reached its rate limit. %s", owner, retry)
	}
	w.Header().Set("Retry-After", strconv.FormatInt(wait, 10))
	fail(w, 429, "rate_limit_exceeded", message)
}
