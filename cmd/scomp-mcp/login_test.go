package main

import (
	"errors"
	"testing"
	"time"

	"github.com/smart-bit-me/scomp-mcp/internal/api"
)

func TestPollOutcome(t *testing.T) {
	for _, tc := range []struct {
		err  error
		wait time.Duration
		fail bool
	}{
		{&api.APIError{Status: 400, Code: "authorization_pending"}, 0, false},
		{&api.APIError{Status: 400, Code: "slow_down"}, 5 * time.Second, false},
		{&api.APIError{Status: 429, Code: "rate_limited"}, 5 * time.Second, false},
		{&api.APIError{Status: 429}, 5 * time.Second, false}, // older relays reply in plain text
		{&api.APIError{Status: 400, Code: "access_denied"}, 0, true},
		{&api.APIError{Status: 400, Code: "expired_token"}, 0, true},
		{&api.APIError{Status: 500}, 0, true},
		{errors.New("network"), 0, true},
	} {
		wait, err := pollOutcome(tc.err)
		if wait != tc.wait || (err != nil) != tc.fail {
			t.Errorf("pollOutcome(%v) = %s, %v", tc.err, wait, err)
		}
	}
}
