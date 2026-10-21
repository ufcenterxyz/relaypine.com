package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestVerifySignature(t *testing.T) {
	now := time.Unix(1800000000, 0)
	payload := []byte(`{"id":"evt_one"}`)
	sign := func(ts int64) string {
		m := hmac.New(sha256.New, []byte("whsec_test"))
		fmt.Fprintf(m, "%d.", ts)
		m.Write(payload)
		return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(m.Sum(nil)))
	}
	cases := []struct {
		name           string
		body           []byte
		header, secret string
		want           bool
	}{{"valid", payload, sign(now.Unix()), "whsec_test", true}, {"modified", []byte(`{"id":"evt_two"}`), sign(now.Unix()), "whsec_test", false}, {"expired", payload, sign(now.Unix() - 301), "whsec_test", false}, {"future", payload, sign(now.Unix() + 301), "whsec_test", false}, {"wrong secret", payload, sign(now.Unix()), "another", false}, {"empty secret", payload, sign(now.Unix()), "", false}, {"malformed", payload, "t=bad,v1=bad", "whsec_test", false}, {"rotation signatures", payload, sign(now.Unix()) + ",v1=deadbeef", "whsec_test", true}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := VerifySignature(c.body, c.header, c.secret, now); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
