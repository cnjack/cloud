package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

func TestCipherRoundtrip(t *testing.T) {
	c, err := NewCipher(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range []string{"", "gho_sometoken", "a much longer token value with spaces 🌱"} {
		blob, err := c.EncryptString(pt)
		if err != nil {
			t.Fatalf("encrypt %q: %v", pt, err)
		}
		if bytes.Contains(blob, []byte(pt)) && pt != "" {
			t.Fatalf("ciphertext leaks plaintext for %q", pt)
		}
		got, err := c.DecryptString(blob)
		if err != nil {
			t.Fatalf("decrypt %q: %v", pt, err)
		}
		if got != pt {
			t.Fatalf("roundtrip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestCipherNonceIsRandom(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	a, _ := c.EncryptString("same")
	b, _ := c.EncryptString("same")
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonce reuse)")
	}
}

func TestCipherTamperDetected(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	blob, _ := c.EncryptString("secret-token")

	// Flip a bit in the ciphertext body — GCM auth must fail.
	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("decrypt accepted tampered ciphertext (GCM auth bypassed)")
	}

	// A different key must not decrypt.
	other, _ := NewCipher(testKey(t))
	if _, err := other.Decrypt(blob); err == nil {
		t.Fatal("decrypt accepted ciphertext under the wrong key")
	}

	// A truncated blob (shorter than the nonce) is rejected.
	if _, err := c.Decrypt(blob[:4]); err == nil {
		t.Fatal("decrypt accepted a too-short blob")
	}
}

func TestNewCipherKeyValidation(t *testing.T) {
	// Empty key => nil cipher, no error (token-less mode).
	c, err := NewCipher("")
	if err != nil || c != nil {
		t.Fatalf("empty key: cipher=%v err=%v want (nil,nil)", c, err)
	}
	// Non-base64.
	if _, err := NewCipher("not base64!!!"); err == nil {
		t.Fatal("non-base64 key should error")
	}
	// Wrong length (16 bytes).
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := NewCipher(short); err == nil {
		t.Fatal("16-byte key should error (need 32 for AES-256)")
	}
}
