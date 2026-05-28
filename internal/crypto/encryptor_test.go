package secretcrypto

import (
	"bytes"
	"context"
	"testing"
)

func TestAESGCMEncryptorRoundTrip(t *testing.T) {
	encryptor, err := NewAESGCMEncryptor(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("NewAESGCMEncryptor() error = %v", err)
	}

	plaintext := []byte("database-password")
	ciphertext, err := encryptor.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if ciphertext.Algorithm != AES256GCMAlgorithm {
		t.Fatalf("Algorithm = %q, want %q", ciphertext.Algorithm, AES256GCMAlgorithm)
	}
	if bytes.Contains(ciphertext.Data, plaintext) {
		t.Fatal("ciphertext contains plaintext")
	}

	decrypted, err := encryptor.Decrypt(context.Background(), ciphertext)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("Decrypt() = %q, want %q", decrypted, plaintext)
	}
}

func TestNewAESGCMEncryptorRejectsInvalidKeyLength(t *testing.T) {
	_, err := NewAESGCMEncryptor([]byte("short"))
	if err == nil {
		t.Fatal("NewAESGCMEncryptor() error = nil, want error")
	}
}
