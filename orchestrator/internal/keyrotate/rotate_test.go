package keyrotate

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/cnjack/jcloud/internal/auth"
)

func rotationKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func TestReencryptMovesCiphertextToNewKey(t *testing.T) {
	oldCipher, err := auth.NewCipher(rotationKey(1))
	if err != nil {
		t.Fatal(err)
	}
	newCipher, err := auth.NewCipher(rotationKey(2))
	if err != nil {
		t.Fatal(err)
	}
	original, err := oldCipher.EncryptString("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := reencrypt(oldCipher, newCipher, original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldCipher.Decrypt(rotated); err == nil {
		t.Fatal("old key decrypted rotated ciphertext")
	}
	plaintext, err := newCipher.DecryptString(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "provider-secret" {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestReencryptFailsClosedWithWrongOldKey(t *testing.T) {
	cipherA, _ := auth.NewCipher(rotationKey(1))
	cipherB, _ := auth.NewCipher(rotationKey(2))
	cipherC, _ := auth.NewCipher(rotationKey(3))
	blob, _ := cipherA.EncryptString("secret")
	if _, err := reencrypt(cipherB, cipherC, blob); err == nil {
		t.Fatal("wrong old key accepted")
	}
}
