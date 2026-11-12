package modelconfig

import (
	"strings"
	"testing"
)

func TestEncryptDecryptAPIKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plain := "secret-provider-key"
	ciphertext, err := Encrypt(key, []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, plain) {
		t.Fatal("ciphertext contains plaintext key")
	}
	got, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != plain {
		t.Fatalf("decrypted %q, want original", got)
	}
	wrongKey := []byte("abcdef0123456789abcdef0123456789")
	if _, err := Decrypt(wrongKey, ciphertext); err == nil {
		t.Fatal("wrong encryption key decrypted ciphertext")
	}
}
