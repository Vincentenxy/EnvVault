package secretcrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const AES256GCMAlgorithm = "AES-256-GCM"

var ErrInvalidMasterKey = errors.New("invalid encryption master key")

type Ciphertext struct {
	Algorithm string `json:"algorithm"`
	Nonce     []byte `json:"nonce"`
	Data      []byte `json:"data"`
}

type Encryptor interface {
	Encrypt(ctx context.Context, plaintext []byte) (Ciphertext, error)
	Decrypt(ctx context.Context, ciphertext Ciphertext) ([]byte, error)
}

type AESGCMEncryptor struct {
	aead cipher.AEAD
}

func NewAESGCMEncryptor(masterKey []byte) (*AESGCMEncryptor, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("%w: AES-256-GCM requires 32 bytes", ErrInvalidMasterKey)
	}

	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &AESGCMEncryptor{aead: aead}, nil
}

func NewAESGCMEncryptorFromBase64(masterKey string) (*AESGCMEncryptor, error) {
	decoded, err := base64.StdEncoding.DecodeString(masterKey)
	if err != nil {
		return nil, fmt.Errorf("%w: expected base64 encoded 32-byte key", ErrInvalidMasterKey)
	}
	return NewAESGCMEncryptor(decoded)
}

func (e *AESGCMEncryptor) Encrypt(ctx context.Context, plaintext []byte) (Ciphertext, error) {
	if err := ctx.Err(); err != nil {
		return Ciphertext{}, err
	}

	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Ciphertext{}, err
	}

	return Ciphertext{
		Algorithm: AES256GCMAlgorithm,
		Nonce:     nonce,
		Data:      e.aead.Seal(nil, nonce, plaintext, nil),
	}, nil
}

func (e *AESGCMEncryptor) Decrypt(ctx context.Context, ciphertext Ciphertext) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ciphertext.Algorithm != AES256GCMAlgorithm {
		return nil, fmt.Errorf("unsupported encryption algorithm: %s", ciphertext.Algorithm)
	}

	return e.aead.Open(nil, ciphertext.Nonce, ciphertext.Data, nil)
}
