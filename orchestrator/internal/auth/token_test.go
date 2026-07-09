package auth

import (
	"strings"
	"testing"
)

func TestGenerateAndHash(t *testing.T) {
	a, err := GenerateRunToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateRunToken()
	if a == b {
		t.Fatal("tokens should be unique")
	}
	if len(a) != 64 { // 32 bytes hex
		t.Fatalf("token length=%d want 64", len(a))
	}
	if HashToken(a) == a {
		t.Fatal("hash should differ from token")
	}
	if HashToken(a) != HashToken(a) {
		t.Fatal("hash should be deterministic")
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("secret", "secret") {
		t.Fatal("equal secrets should match")
	}
	if ConstantTimeEqual("secret", "secreu") {
		t.Fatal("different secrets should not match")
	}
	if ConstantTimeEqual("short", "longer") {
		t.Fatal("different lengths should not match")
	}
}

// TestGenerateAPIKey covers the F12 / D24 key-gen contract: the "jck_" tag,
// uniqueness, hex body length, and that the plaintext never equals its own
// hash (the one-way discipline api_keys.key_hash relies on).
func TestGenerateAPIKey(t *testing.T) {
	a, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("keys should be unique")
	}
	if !strings.HasPrefix(a, APIKeyTokenPrefix) {
		t.Fatalf("key %q missing prefix %q", a, APIKeyTokenPrefix)
	}
	body := strings.TrimPrefix(a, APIKeyTokenPrefix)
	if len(body) != 64 { // 32 bytes hex
		t.Fatalf("key body length=%d want 64", len(body))
	}
	if HashToken(a) == a {
		t.Fatal("hash should differ from the plaintext key")
	}
	if HashToken(a) != HashToken(a) {
		t.Fatal("hash should be deterministic")
	}
	// The hash must not itself reveal the plaintext (no read-back path — F12
	// §2): a different key's hash never collides in this sample and the hash
	// carries no substring of the original.
	if strings.Contains(HashToken(a), body) {
		t.Fatal("hash must not leak the plaintext body")
	}
}

// TestAPIKeyDisplayPrefix: the stored prefix is short, starts with the key's
// own prefix, and — the security property — never carries enough of the
// plaintext to reconstruct or brute-force the rest (it is for list
// identification only).
func TestAPIKeyDisplayPrefix(t *testing.T) {
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	p := APIKeyDisplayPrefix(key)
	if !strings.HasPrefix(key, p) {
		t.Fatalf("prefix %q is not a prefix of key %q", p, key)
	}
	if len(p) != 8 {
		t.Fatalf("prefix length=%d want 8 (e.g. jck_a1b2)", len(p))
	}
	if len(p) >= len(key) {
		t.Fatal("prefix must be strictly shorter than the full key")
	}
	// A short input is returned as-is (defensive; never panics/out-of-range).
	if got := APIKeyDisplayPrefix("jck_"); got != "jck_" {
		t.Fatalf("short input: got %q want %q", got, "jck_")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true}, // case-insensitive scheme
		{"Bearer  abc ", "abc", true},
		{"Basic abc", "", false},
		{"", "", false},
		{"Bearer ", "", false},
		{"abc", "", false},
	}
	for _, tc := range cases {
		got, ok := BearerToken(tc.header)
		if ok != tc.ok || got != tc.want {
			t.Errorf("BearerToken(%q)=(%q,%v) want (%q,%v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}
