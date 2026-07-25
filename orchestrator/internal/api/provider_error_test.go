package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/cnjack/jcloud/internal/provider"
)

func TestSummarizeProviderErrNeverSerializesUpstreamOrCredentialText(t *testing.T) {
	const secret = "provider-token-must-not-leak"
	for _, err := range []error{
		&provider.HTTPStatusError{Method: "GET", StatusCode: 502},
		errors.New("upstream reflected Bearer " + secret),
	} {
		got := summarizeProviderErr(err)
		if strings.Contains(got, secret) || strings.Contains(got, "Bearer") {
			t.Fatalf("summary leaked upstream error detail: %q", got)
		}
	}
	if got := summarizeProviderErr(&provider.HTTPStatusError{Method: "GET", StatusCode: 502}); got != "provider returned HTTP 502" {
		t.Fatalf("status summary=%q", got)
	}
	if got := summarizeProviderErr(errors.New("untrusted")); got != "provider request failed" {
		t.Fatalf("opaque summary=%q", got)
	}
}
