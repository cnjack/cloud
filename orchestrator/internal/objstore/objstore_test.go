package objstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeleteRemovesOneObjectAndTreatsMissingAsSuccess(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method=%s want DELETE", r.Method)
		}
		paths = append(paths, r.URL.Path)
		if len(paths) == 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	c, err := New(Config{Endpoint: ts.URL, Bucket: "workspaces", AccessKey: "a", SecretKey: "s", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := c.Delete(context.Background(), "workspaces/svc.tar.zst"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	if len(paths) != 2 || paths[0] != "/workspaces/workspaces/svc.tar.zst" {
		t.Fatalf("delete paths=%v", paths)
	}
}

// TestPresignGetMatchesAWSExample pins the SigV4 crypto to AWS's own published
// example from "Authenticating Requests: Using Query Parameters (AWS Signature
// Version 4)". Any drift in the canonical request, string-to-sign, signing-key
// chain, or encoding changes the signature and fails this test — it is the
// correctness anchor for the whole presigner.
func TestPresignGetMatchesAWSExample(t *testing.T) {
	c, err := New(Config{
		Endpoint:  "https://s3.amazonaws.com",
		Bucket:    "examplebucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		// virtual-hosted addressing (bucket prefixed onto host), matching the doc.
		ForcePathStyle: false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fixed := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	got, err := c.presign(http.MethodGet, "test.txt", 86400*time.Second, fixed)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	want := "https://examplebucket.s3.amazonaws.com/test.txt" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z" +
		"&X-Amz-Expires=86400" +
		"&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got != want {
		t.Fatalf("presigned URL mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestPresignPathStyleMinIO checks the MinIO addressing shape: the host is
// unchanged and the bucket is the first path segment. The exact signature is not
// re-derived here (the AWS example already locks the crypto); this asserts the
// structural difference from virtual-hosted plus that a signature is present.
func TestPresignPathStyleMinIO(t *testing.T) {
	c, err := New(Config{
		Endpoint:       "http://minio.jcloud.svc.cluster.local:9000",
		Bucket:         "jcloud-workspaces",
		AccessKey:      "minioadmin",
		SecretKey:      "minioadmin",
		ForcePathStyle: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.PresignPut("workspaces/svc123.tar.zst", 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if !strings.HasPrefix(got, "http://minio.jcloud.svc.cluster.local:9000/jcloud-workspaces/workspaces/svc123.tar.zst?") {
		t.Fatalf("path-style URL wrong prefix: %s", got)
	}
	for _, want := range []string{
		"X-Amz-Algorithm=AWS4-HMAC-SHA256",
		"X-Amz-Credential=minioadmin%2F",
		"X-Amz-Expires=1800",
		"X-Amz-SignedHeaders=host",
		"X-Amz-Signature=",
		"%2Fus-east-1%2Fs3%2Faws4_request", // default region folded into scope
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("presigned URL missing %q:\n%s", want, got)
		}
	}
}

// TestPresignDeterministicAndKeyed proves determinism (same inputs => same URL)
// and that the signature is keyed on the object key (the whole point of a
// single-object scoped URL: a URL for key A must not authorize key B).
func TestPresignDeterministicAndKeyed(t *testing.T) {
	c, _ := New(Config{
		Endpoint: "https://s3.example.com", Bucket: "b",
		AccessKey: "AK", SecretKey: "SK", ForcePathStyle: true,
	})
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	a1, _ := c.presign(http.MethodGet, "workspaces/a.tar.zst", time.Hour, now)
	a2, _ := c.presign(http.MethodGet, "workspaces/a.tar.zst", time.Hour, now)
	if a1 != a2 {
		t.Fatalf("presign not deterministic:\n%s\n%s", a1, a2)
	}
	b, _ := c.presign(http.MethodGet, "workspaces/b.tar.zst", time.Hour, now)
	if sig(a1) == sig(b) {
		t.Fatal("signature must differ per object key (URL for A signs B)")
	}
	// Method is part of the canonical request too: a GET URL must not equal a PUT.
	put, _ := c.presign(http.MethodPut, "workspaces/a.tar.zst", time.Hour, now)
	if sig(a1) == sig(put) {
		t.Fatal("signature must differ between GET and PUT")
	}
}

// TestNewRejectsIncompleteConfig asserts the fail-visible contract: a missing
// required field is an ERROR, never a silent disabled client.
func TestNewRejectsIncompleteConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no endpoint", Config{Bucket: "b", AccessKey: "a", SecretKey: "s"}, "S3_ENDPOINT"},
		{"no bucket", Config{Endpoint: "https://s3", AccessKey: "a", SecretKey: "s"}, "S3_BUCKET"},
		{"no access key", Config{Endpoint: "https://s3", Bucket: "b", SecretKey: "s"}, "S3_ACCESS_KEY"},
		{"no secret", Config{Endpoint: "https://s3", Bucket: "b", AccessKey: "a"}, "S3_SECRET_KEY"},
		{"bad scheme", Config{Endpoint: "ftp://x", Bucket: "b", AccessKey: "a", SecretKey: "s"}, "http(s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New(%s): want error containing %q, got %v", tc.name, tc.want, err)
			}
		})
	}
}

// TestPresignRejectsBadExpiry guards the TTL bounds.
func TestPresignRejectsBadExpiry(t *testing.T) {
	c, _ := New(Config{Endpoint: "https://s3", Bucket: "b", AccessKey: "a", SecretKey: "s", ForcePathStyle: true})
	if _, err := c.presign(http.MethodGet, "k", 0, time.Now()); err == nil {
		t.Fatal("want error for zero expiry")
	}
	if _, err := c.presign(http.MethodGet, "k", 8*24*time.Hour, time.Now()); err == nil {
		t.Fatal("want error for >7d expiry")
	}
	if _, err := c.PresignGet("", time.Hour); err == nil {
		t.Fatal("want error for empty key")
	}
}

// sig extracts the X-Amz-Signature value from a presigned URL.
func sig(u string) string {
	_, after, ok := strings.Cut(u, "X-Amz-Signature=")
	if !ok {
		return ""
	}
	return after
}
