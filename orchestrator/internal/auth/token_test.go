package auth

import "testing"

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
