package auth

import (
	"strings"
	"testing"
)

func TestArgon2idHasher_HashVerify_RoundTrip(t *testing.T) {
	h := NewArgon2idHasher(DefaultPasswordParams())
	password := "correct-horse-battery-staple-12+chars"
	encoded, err := h.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=") {
		t.Fatalf("encoded prefix mismatch: %s", encoded)
	}
	ok, err := h.Verify(encoded, password)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatalf("verify roundtrip failed for %q", password)
	}
}

func TestArgon2idHasher_Verify_WrongPassword(t *testing.T) {
	h := NewArgon2idHasher(DefaultPasswordParams())
	encoded, err := h.Hash("correct-horse-battery-staple-12+chars")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := h.Verify(encoded, "wrong-horse-battery-staple-12+chars")
	if err != nil {
		t.Fatalf("verify should not error on wrong password: %v", err)
	}
	if ok {
		t.Fatalf("verify should return false on wrong password")
	}
}

func TestArgon2idHasher_Verify_MalformedEncoded(t *testing.T) {
	h := NewArgon2idHasher(DefaultPasswordParams())
	cases := []struct {
		name    string
		encoded string
	}{
		{"too few segments", "$argon2id$v=19$m=65536,t=3,p=2$abc"},
		{"too many segments", "$argon2id$v=19$m=65536,t=3,p=2$abc$def$extra"},
		{"wrong algo", "$argon2i$v=19$m=65536,t=3,p=2$YWJj$ZGVm"},
		{"bad params", "$argon2id$v=19$m=abc,t=3,p=2$YWJj$ZGVm"},
		{"bad salt b64", "$argon2id$v=19$m=65536,t=3,p=2$!!!$ZGVm"},
		{"bad hash b64", "$argon2id$v=19$m=65536,t=3,p=2$YWJj$!!!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := h.Verify(tc.encoded, "anything-here-12+chars")
			if err == nil {
				t.Fatalf("verify should return error for %q, got ok=%v", tc.encoded, ok)
			}
			if ok {
				t.Fatalf("verify should return false alongside error")
			}
		})
	}
}

func TestArgon2idHasher_Hash_EmptyPasswordRejected(t *testing.T) {
	h := NewArgon2idHasher(DefaultPasswordParams())
	if _, err := h.Hash(""); err == nil {
		t.Fatalf("hash of empty password should be rejected")
	}
}

func TestArgon2idHasher_Hash_DifferentSaltEachTime(t *testing.T) {
	h := NewArgon2idHasher(DefaultPasswordParams())
	password := "correct-horse-battery-staple-12+chars"
	a, err := h.Hash(password)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	b, err := h.Hash(password)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if a == b {
		t.Fatalf("hashes of same password must differ (random salt), got identical")
	}
	// 但两个都应当能 verify 成功
	if ok, _ := h.Verify(a, password); !ok {
		t.Fatalf("verify a failed")
	}
	if ok, _ := h.Verify(b, password); !ok {
		t.Fatalf("verify b failed")
	}
}

func TestArgon2idHasher_DefaultParamsPath(t *testing.T) {
	// 显式传 0 参数 → 走默认
	h := NewArgon2idHasher(PasswordParams{})
	if h.params.Memory == 0 {
		t.Fatalf("default params not applied (memory=0)")
	}
	def := DefaultPasswordParams()
	if h.params.Memory != def.Memory {
		t.Fatalf("memory: got %d want %d", h.params.Memory, def.Memory)
	}
	if h.params.Iterations != def.Iterations {
		t.Fatalf("iterations: got %d want %d", h.params.Iterations, def.Iterations)
	}
	if h.params.Parallelism != def.Parallelism {
		t.Fatalf("parallelism: got %d want %d", h.params.Parallelism, def.Parallelism)
	}
}

func TestArgon2idHasher_Algo(t *testing.T) {
	h := NewArgon2idHasher(DefaultPasswordParams())
	if h.Algo() != AlgoArgon2id {
		t.Fatalf("algo: got %s want %s", h.Algo(), AlgoArgon2id)
	}
}

func TestArgon2idHasher_Verify_InteropAcrossHasherInstances(t *testing.T) {
	// 同一 password 在两个 hasher 实例上 hash,都应当被另一个 verify 通过
	// (因为 PHC 字符串内嵌 params,verify 不依赖 hasher 状态)
	h1 := NewArgon2idHasher(DefaultPasswordParams())
	h2 := NewArgon2idHasher(DefaultPasswordParams())
	password := "correct-horse-battery-staple-12+chars"
	encoded, err := h1.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if ok, _ := h2.Verify(encoded, password); !ok {
		t.Fatalf("h2 should verify hash produced by h1")
	}
}
