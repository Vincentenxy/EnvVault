package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// AlgoArgon2id 是 password_algo 列存储的算法标识。
// 留作未来切到 bcrypt / scrypt 时的判别字段。
const AlgoArgon2id = "argon2id"

// PasswordParams 描述 argon2id 的计算参数。
//
// 选型依据(OWASP Password Storage Cheat Sheet 2023):
//   - m=64 MiB,t=3,p=2 是 argon2id 在「中等强度」档位的推荐值,
//     单次 hash 在现代 CPU 上约 50-100ms,既能扛 GPU 暴力,
//     又不会把登录 RT 拉到 1s+。
//   - salt 16 byte 足够防 rainbow table;
//   - key 32 byte(256 bit)与 AES-256 key 等长,语义对称。
type PasswordParams struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultPasswordParams 返回 v9 选定的推荐参数。
func DefaultPasswordParams() PasswordParams {
	return PasswordParams{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// PasswordHasher 是 password hash / verify 的接口;service 层只依赖这个
// 接口,不直接 import argon2,便于测试时注入 mock。
type PasswordHasher interface {
	// Hash 返回 PHC 字符串:"$argon2id$v=19$m=...,t=...,p=...$<salt-b64>$<hash-b64>"
	// 每次调用产生新随机 salt,同样的 password 两次 hash 输出必然不同。
	Hash(password string) (encoded string, err error)
	// Verify 把 PHC 字符串按其内嵌的 params 重新计算并 constant-time 比对。
	// 密码错误返 (false, nil);PHC 格式错误返 (false, err)。
	Verify(encoded, password string) (match bool, err error)
	// Algo 返回当前 hasher 的算法标识(用于写回 users.password_algo)。
	Algo() string
}

type Argon2idHasher struct {
	params PasswordParams
}

// NewArgon2idHasher 构造 argon2id hasher。params 全 0 时退化到默认值,
// 便于配置层「未填就用默认」。
func NewArgon2idHasher(params PasswordParams) *Argon2idHasher {
	if params.Memory == 0 && params.Iterations == 0 && params.Parallelism == 0 {
		params = DefaultPasswordParams()
	}
	if params.SaltLength == 0 {
		params.SaltLength = DefaultPasswordParams().SaltLength
	}
	if params.KeyLength == 0 {
		params.KeyLength = DefaultPasswordParams().KeyLength
	}
	return &Argon2idHasher{params: params}
}

func (h *Argon2idHasher) Algo() string { return AlgoArgon2id }

func (h *Argon2idHasher) Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	hash := argon2.IDKey(
		[]byte(password),
		salt,
		h.params.Iterations,
		h.params.Memory,
		h.params.Parallelism,
		h.params.KeyLength,
	)
	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.Memory, h.params.Iterations, h.params.Parallelism, saltB64, hashB64,
	), nil
}

func (h *Argon2idHasher) Verify(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// 期望 6 段:["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 {
		return false, fmt.Errorf("invalid encoded hash: expected 6 segments, got %d", len(parts))
	}
	if parts[1] != AlgoArgon2id {
		return false, fmt.Errorf("unsupported algo: %s", parts[1])
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("parse version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version: %d", version)
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, fmt.Errorf("parse params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}
	actual := argon2.IDKey(
		[]byte(password),
		salt,
		iterations,
		memory,
		parallelism,
		uint32(len(expected)),
	)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}
