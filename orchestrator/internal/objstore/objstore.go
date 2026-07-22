// Package objstore is the control-plane's S3/MinIO client for the persistent
// workspace archive layer (F10 / D23 ③). It exists to keep the D16 red line
// intact: the runner pod that reads/writes a service's workspace PVC MUST NOT
// hold the long-lived S3 credentials. Instead the orchestrator (which alone
// holds S3_ACCESS_KEY/S3_SECRET_KEY) signs a SHORT-LIVED, single-object
// presigned URL and hands only that to the archive/restore Job. Even a fully
// compromised pod gets a URL scoped to one object key and a small TTL, never
// the account credentials.
//
// The presigning is AWS Signature Version 4 (query-parameter form), implemented
// with the standard library only — no SDK dependency, so go.mod stays lean and
// the crypto is fully unit-testable against AWS's published example vector (see
// objstore_test.go). It targets both real S3 (virtual-hosted addressing) and
// MinIO (path-style addressing, S3_FORCE_PATH_STYLE=true).
package objstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const deleteTimeout = 30 * time.Second

// Config is the resolved object-storage configuration (from config.Config's
// S3_* fields). Endpoint, Bucket, AccessKey and SecretKey are all required for a
// usable client; a missing one means the archive feature is DISABLED, surfaced
// fail-visibly (never a silent no-op) at GET /api/v1/system and in the
// reconciler's archive pass.
type Config struct {
	Endpoint       string // S3_ENDPOINT, e.g. https://s3.amazonaws.com or http://minio.jcloud.svc:9000
	Bucket         string // S3_BUCKET
	AccessKey      string // S3_ACCESS_KEY
	SecretKey      string // S3_SECRET_KEY
	Region         string // S3_REGION (default us-east-1)
	ForcePathStyle bool   // S3_FORCE_PATH_STYLE — MinIO needs true (host/bucket/key)
}

// Client signs presigned URLs for a single bucket. Archive/restore payload I/O
// happens in Jobs through those URLs; the sole direct request is a control-plane
// DELETE used to erase an archive when its Service is removed.
type Client struct {
	cfg    Config
	scheme string
	host   string // endpoint host[:port], no scheme/path
}

// New validates cfg and builds a Client. It returns an error (not a nil no-op)
// when any required field is missing so the caller can surface a fail-visible
// reason — see config.Config.ArchiveEnabled for the presence gate the process
// uses to decide whether to build a Client at all.
func New(cfg Config) (*Client, error) {
	var missing []string
	if cfg.Endpoint == "" {
		missing = append(missing, "S3_ENDPOINT")
	}
	if cfg.Bucket == "" {
		missing = append(missing, "S3_BUCKET")
	}
	if cfg.AccessKey == "" {
		missing = append(missing, "S3_ACCESS_KEY")
	}
	if cfg.SecretKey == "" {
		missing = append(missing, "S3_SECRET_KEY")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("objstore: missing required config: %s", strings.Join(missing, ", "))
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("objstore: invalid S3_ENDPOINT %q: %w", cfg.Endpoint, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("objstore: S3_ENDPOINT %q must be http(s)", cfg.Endpoint)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("objstore: S3_ENDPOINT %q has no host", cfg.Endpoint)
	}
	return &Client{cfg: cfg, scheme: u.Scheme, host: u.Host}, nil
}

// Bucket returns the configured bucket (for logging / diagnostics; never a
// secret).
func (c *Client) Bucket() string { return c.cfg.Bucket }

// Endpoint returns the configured endpoint (non-secret; shown at
// GET /api/v1/system).
func (c *Client) Endpoint() string { return c.cfg.Endpoint }

// PresignPut returns a URL that authorizes a single PUT of the object at key,
// valid for expiry. The archive Job streams the workspace tarball to it.
func (c *Client) PresignPut(key string, expiry time.Duration) (string, error) {
	return c.presign(http.MethodPut, key, expiry, time.Now().UTC())
}

// PresignGet returns a URL that authorizes a single GET of the object at key,
// valid for expiry. The restore path downloads the tarball from it.
func (c *Client) PresignGet(key string, expiry time.Duration) (string, error) {
	return c.presign(http.MethodGet, key, expiry, time.Now().UTC())
}

// Delete removes one archived workspace object. Unlike archive/restore Jobs,
// this runs in the control plane where the S3 credential already lives. A 404
// is success so service-deletion retries are idempotent.
func (c *Client) Delete(ctx context.Context, key string) error {
	url, err := c.presign(http.MethodDelete, key, 5*time.Minute, time.Now().UTC())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("objstore: build delete request: %w", err)
	}
	client := &http.Client{Timeout: deleteTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("objstore: delete %q: %w", key, err)
	}
	defer resp.Body.Close()
	if (resp.StatusCode >= 200 && resp.StatusCode < 300) || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("objstore: delete %q: status %s", key, resp.Status)
}

// presign implements SigV4 query-parameter (presigned URL) signing. now is
// injectable so the crypto can be pinned to AWS's documented example in tests.
// The payload is signed as UNSIGNED-PAYLOAD, which is what lets curl PUT an
// arbitrary body (the tarball) without the orchestrator having to hash it.
func (c *Client) presign(method, key string, expiry time.Duration, now time.Time) (string, error) {
	if key == "" {
		return "", fmt.Errorf("objstore: empty object key")
	}
	seconds := int(expiry / time.Second)
	if seconds <= 0 {
		return "", fmt.Errorf("objstore: expiry must be positive, got %s", expiry)
	}
	// SigV4 presigned URLs may not exceed 7 days.
	if seconds > 7*24*3600 {
		return "", fmt.Errorf("objstore: expiry %s exceeds the 7-day SigV4 maximum", expiry)
	}

	const service = "s3"
	const algorithm = "AWS4-HMAC-SHA256"
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	// Addressing. Path-style (MinIO): host stays the endpoint host and the URI is
	// /<bucket>/<key>. Virtual-hosted (real S3): the bucket is prefixed onto the
	// host and the URI is /<key>. The Host header (the only signed header) must
	// match whatever the client will actually send, so both branches sign exactly
	// the host they build the URL with.
	host := c.host
	var canonicalURI string
	if c.cfg.ForcePathStyle {
		canonicalURI = "/" + encodePath(c.cfg.Bucket) + "/" + encodePath(key)
	} else {
		host = c.cfg.Bucket + "." + c.host
		canonicalURI = "/" + encodePath(key)
	}

	credentialScope := strings.Join([]string{dateStamp, c.cfg.Region, service, "aws4_request"}, "/")

	// Canonical query string: the SigV4 params, URI-encoded and sorted by key.
	// X-Amz-Signature is deliberately NOT here — it is appended AFTER signing.
	q := map[string]string{
		"X-Amz-Algorithm":     algorithm,
		"X-Amz-Credential":    c.cfg.AccessKey + "/" + credentialScope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.Itoa(seconds),
		"X-Amz-SignedHeaders": "host",
	}
	canonicalQuery := canonicalizeQuery(q)

	canonicalHeaders := "host:" + host + "\n"
	signedHeaders := "host"
	payloadHash := "UNSIGNED-PAYLOAD"

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		credentialScope,
		hexSHA256(canonicalRequest),
	}, "\n")

	signingKey := sigV4SigningKey(c.cfg.SecretKey, dateStamp, c.cfg.Region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	return c.scheme + "://" + host + canonicalURI + "?" + canonicalQuery +
		"&X-Amz-Signature=" + signature, nil
}

// canonicalizeQuery URI-encodes each key and value and joins them sorted by key,
// per the SigV4 canonical query string rules.
func canonicalizeQuery(q map[string]string) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(q))
	for _, k := range keys {
		parts = append(parts, encodeRFC3986(k)+"="+encodeRFC3986(q[k]))
	}
	return strings.Join(parts, "&")
}

// encodePath URI-encodes a path segment set, preserving '/' as a path separator
// (each segment is RFC3986-encoded). AWS treats the object key's slashes as
// literal path separators in the canonical URI.
func encodePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = encodeRFC3986(s)
	}
	return strings.Join(segs, "/")
}

// encodeRFC3986 percent-encodes s per RFC 3986 (AWS's required encoding): every
// byte except the unreserved set A-Z a-z 0-9 - _ . ~ is encoded, and the
// hex digits are UPPERCASE. net/url is intentionally NOT used here because it
// does not match AWS's rules (it leaves some sub-delims unencoded and would
// break the signature).
func encodeRFC3986(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[ch>>4])
		b.WriteByte(upperhex[ch&0x0f])
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// sigV4SigningKey derives the SigV4 signing key by the documented HMAC chain:
// kSecret -> kDate -> kRegion -> kService -> kSigning.
func sigV4SigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}
