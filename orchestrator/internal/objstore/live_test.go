package objstore

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestPresignRoundTripLive exercises a REAL presigned PUT + GET against a live
// S3/MinIO, proving the whole D16 chain: the orchestrator (this test) signs a
// URL with the credentials, and a credential-less client (the plain http.Client
// here, standing in for the runner's curl) uploads/downloads using ONLY that
// URL. Skips unless the endpoint env is set:
//
//	OBJSTORE_LIVE_ENDPOINT=http://127.0.0.1:59000 OBJSTORE_LIVE_BUCKET=jcloud-workspaces \
//	OBJSTORE_LIVE_ACCESS=minioadmin OBJSTORE_LIVE_SECRET=minioadmin \
//	    go test ./internal/objstore/ -run Live -v
func TestPresignRoundTripLive(t *testing.T) {
	endpoint := os.Getenv("OBJSTORE_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("OBJSTORE_LIVE_ENDPOINT not set; skipping live object-storage round-trip")
	}
	c, err := New(Config{
		Endpoint:       endpoint,
		Bucket:         getenvDefault("OBJSTORE_LIVE_BUCKET", "jcloud-workspaces"),
		AccessKey:      getenvDefault("OBJSTORE_LIVE_ACCESS", "minioadmin"),
		SecretKey:      getenvDefault("OBJSTORE_LIVE_SECRET", "minioadmin"),
		ForcePathStyle: true, // MinIO
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	key := "workspaces/live-roundtrip-test.tar.zst"
	body := []byte("hello from a credential-less pod\n")

	// PUT via presigned URL (no credentials on the request).
	putURL, err := c.PresignPut(key, 10*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("PUT status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// GET via presigned URL and verify the bytes round-trip.
	getURL, err := c.PresignGet(key, 10*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	resp, err = http.Get(getURL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("GET status=%d body=%s", resp.StatusCode, got)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, body)
	}
}

func getenvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
