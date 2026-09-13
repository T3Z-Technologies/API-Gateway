package security

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestFernetEncryptAndDecrypt(t *testing.T) {
	keyBytes := make([]byte, 32)
	_, _ = rand.Read(keyBytes)
	b64Key := base64.URLEncoding.EncodeToString(keyBytes)

	encryptor, err := NewFernetEncryptor(b64Key)
	if err != nil {
		t.Fatalf("Failed to init Fernet: %v", err)
	}

	secretMessage := "my-secret-access-token-123456"
	encrypted, err := encryptor.Encrypt(secretMessage)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decrypted, err := encryptor.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if decrypted != secretMessage {
		t.Fatalf("Decrypted message mismatch: got %s, expected %s", decrypted, secretMessage)
	}
}
