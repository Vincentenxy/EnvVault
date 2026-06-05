package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"sync"
	"testing"
	"time"

	"envVault/internal/auth"
	"envVault/internal/auth/ratelimit"
	"envVault/internal/domain"
)

// ---- recording auth repo (fakes) ----

type recordingAuthRepo struct {
	mu sync.Mutex

	// 可控:GetUserByEmail 返回
	getByEmailUser domain.User
	getByEmailErr  error

	// 可控:CreatePasswordUser 返回
	createdUser domain.User
	createErr   error

	// 可控:GetUserByExternalId 返回
	getByExtUser domain.User
	getByExtErr  error

	// 调用记录
	createCalls []createCall
	bumpCalls   []string
	updCalls    []updateCall

	// 简单状态:tokens_valid_after
	tva time.Time
}

type createCall struct {
	Email, Name, Hash, Algo string
}

type updateCall struct {
	ExternalUserId, Hash, Algo string
}

func (r *recordingAuthRepo) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getByEmailUser, r.getByEmailErr
}

func (r *recordingAuthRepo) GetUserById(_ context.Context, _ string) (domain.User, error) {
	return domain.User{}, errors.New("not used")
}

func (r *recordingAuthRepo) GetUserByExternalId(_ context.Context, _ string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getByExtUser, r.getByExtErr
}

func (r *recordingAuthRepo) CreatePasswordUser(_ context.Context, email, name, hash, algo string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls = append(r.createCalls, createCall{email, name, hash, algo})
	if r.createErr != nil {
		return domain.User{}, r.createErr
	}
	return r.createdUser, nil
}

func (r *recordingAuthRepo) UpdatePasswordHash(_ context.Context, _, _, _ string) (domain.User, error) {
	return domain.User{}, errors.New("not used")
}

func (r *recordingAuthRepo) UpdatePasswordHashByExternalId(_ context.Context, extId, hash, algo string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updCalls = append(r.updCalls, updateCall{extId, hash, algo})
	r.tva = time.Now()
	u := r.getByExtUser
	ptr := r.tva
	u.TokensValidAfter = &ptr
	return u, nil
}

func (r *recordingAuthRepo) BumpTokensValidAfter(_ context.Context, _ string) (time.Time, error) {
	return time.Time{}, errors.New("not used")
}

func (r *recordingAuthRepo) BumpTokensValidAfterByExternalId(_ context.Context, extId string) (time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bumpCalls = append(r.bumpCalls, extId)
	r.tva = time.Now()
	return r.tva, nil
}

func (r *recordingAuthRepo) GetTokensValidAfter(_ context.Context, _ string) (time.Time, error) {
	return time.Time{}, nil
}
func (r *recordingAuthRepo) ListUsersWithTokensValidAfter(_ context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (r *recordingAuthRepo) TouchLastSeen(_ context.Context, _ string) error { return nil }
func (r *recordingAuthRepo) RecordLoginAttempt(_ context.Context, _, _ string, _ bool, _ string) error {
	return nil
}
func (r *recordingAuthRepo) CountRecentFailedByIP(_ context.Context, _ string, _ time.Duration) (int, error) {
	return 0, nil
}

// ---- fake ratelimit (always allow) ----

type noopLimiter struct{}

func (noopLimiter) Check(_ context.Context, _ string) error          { return nil }
func (noopLimiter) Record(_ context.Context, _ string, _ bool) error { return nil }

// ---- helpers ----

func testRSAKeyPair(t *testing.T) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func newTestAuthService(t *testing.T, repo *recordingAuthRepo) AuthService {
	t.Helper()
	return NewAuthService(AuthServiceOptions{
		AuthRepo:       repo,
		PasswordHasher: auth.NewArgon2idHasher(auth.DefaultPasswordParams()),
		Limiter:        noopLimiter{},
		TokensCache:    nil,
		PrivateKeyPEM:  testRSAKeyPair(t),
		TokenTTL:       time.Hour,
		PasswordMinLen: 12,
	})
}

// ---- 用例 ----

func TestRegister_HappyPath(t *testing.T) {
	repo := &recordingAuthRepo{
		createdUser: domain.User{
			Id:             "uuid-1",
			ExternalUserId: "email:test@example.com",
			Name:           "Test",
			Email:          "test@example.com",
			Source:         "password",
		},
	}
	svc := newTestAuthService(t, repo)

	tok, err := svc.Register(context.Background(), "test@example.com", "Test", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if tok.Token == "" {
		t.Fatalf("Register returned empty token")
	}
	if tok.Email != "test@example.com" {
		t.Fatalf("email mismatch: %s", tok.Email)
	}
	if len(repo.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(repo.createCalls))
	}
	c := repo.createCalls[0]
	if c.Email != "test@example.com" || c.Name != "Test" || c.Algo != "argon2id" || c.Hash == "" {
		t.Fatalf("create call fields wrong: %+v", c)
	}
}

func TestRegister_InvalidEmail(t *testing.T) {
	repo := &recordingAuthRepo{}
	svc := newTestAuthService(t, repo)
	_, err := svc.Register(context.Background(), "not-an-email", "Test", "correct-horse-battery-staple")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
	if len(repo.createCalls) != 0 {
		t.Fatalf("create should not be called on validation failure")
	}
}

func TestRegister_ShortPassword(t *testing.T) {
	repo := &recordingAuthRepo{}
	svc := newTestAuthService(t, repo)
	_, err := svc.Register(context.Background(), "test@example.com", "Test", "short")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestRegister_EmptyName(t *testing.T) {
	repo := &recordingAuthRepo{}
	svc := newTestAuthService(t, repo)
	_, err := svc.Register(context.Background(), "test@example.com", "  ", "correct-horse-battery-staple")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestRegister_EmailAlreadyExists(t *testing.T) {
	repo := &recordingAuthRepo{createErr: domain.ErrConflict}
	svc := newTestAuthService(t, repo)
	_, err := svc.Register(context.Background(), "dup@example.com", "Dup", "correct-horse-battery-staple")
	if !errors.Is(err, ErrEmailAlreadyExists) {
		t.Fatalf("expected ErrEmailAlreadyExists, got %v", err)
	}
}

func TestLogin_HappyPath(t *testing.T) {
	hasher := auth.NewArgon2idHasher(auth.DefaultPasswordParams())
	hash, _ := hasher.Hash("correct-horse-battery-staple")
	repo := &recordingAuthRepo{
		getByEmailUser: domain.User{
			Id:             "uuid-1",
			ExternalUserId: "email:test@example.com",
			Name:           "Test",
			Email:          "test@example.com",
			Source:         "password",
			PasswordHash:   hash,
			PasswordAlgo:   "argon2id",
		},
	}
	svc := newTestAuthService(t, repo)
	tok, err := svc.Login(context.Background(), "test@example.com", "correct-horse-battery-staple", "1.2.3.4")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok.Token == "" {
		t.Fatalf("expected non-empty token")
	}
}

func TestLogin_EmailNotFound(t *testing.T) {
	repo := &recordingAuthRepo{getByEmailErr: domain.ErrNotFound}
	svc := newTestAuthService(t, repo)
	_, err := svc.Login(context.Background(), "missing@example.com", "anything-here-12+chars", "1.2.3.4")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("expected ErrBadCredentials, got %v", err)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	hasher := auth.NewArgon2idHasher(auth.DefaultPasswordParams())
	hash, _ := hasher.Hash("correct-horse-battery-staple")
	repo := &recordingAuthRepo{
		getByEmailUser: domain.User{
			Email:        "test@example.com",
			Source:       "password",
			PasswordHash: hash,
		},
	}
	svc := newTestAuthService(t, repo)
	_, err := svc.Login(context.Background(), "test@example.com", "wrong-horse-battery-staple", "1.2.3.4")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("expected ErrBadCredentials, got %v", err)
	}
}

func TestLogin_NotPasswordSource(t *testing.T) {
	repo := &recordingAuthRepo{
		getByEmailUser: domain.User{
			Email:        "test@example.com",
			Source:       "jwt",
			PasswordHash: "",
		},
	}
	svc := newTestAuthService(t, repo)
	_, err := svc.Login(context.Background(), "test@example.com", "anything-here-12+chars", "1.2.3.4")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("expected ErrBadCredentials for non-password user, got %v", err)
	}
}

func TestLogin_RateLimited(t *testing.T) {
	repo := &recordingAuthRepo{
		getByEmailUser: domain.User{
			Email:        "test@example.com",
			Source:       "password",
			PasswordHash: "any-hash-here",
		},
	}
	blockingLimiter := blockingLimiter{}
	svc := NewAuthService(AuthServiceOptions{
		AuthRepo:       repo,
		PasswordHasher: auth.NewArgon2idHasher(auth.DefaultPasswordParams()),
		Limiter:        blockingLimiter,
		PrivateKeyPEM:  testRSAKeyPair(t),
		TokenTTL:       time.Hour,
		PasswordMinLen: 12,
	})
	_, err := svc.Login(context.Background(), "test@example.com", "wrong-horse-battery-staple", "1.2.3.4")
	if !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("expected ratelimit.ErrRateLimited, got %v", err)
	}
}

type blockingLimiter struct{}

func (blockingLimiter) Check(_ context.Context, _ string) error          { return ratelimit.ErrRateLimited }
func (blockingLimiter) Record(_ context.Context, _ string, _ bool) error { return nil }

func TestLogout_BumpTokensValidAfter(t *testing.T) {
	repo := &recordingAuthRepo{}
	svc := newTestAuthService(t, repo)
	if err := svc.Logout(context.Background(), "email:test@example.com"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if len(repo.bumpCalls) != 1 || repo.bumpCalls[0] != "email:test@example.com" {
		t.Fatalf("expected 1 bump call for email:test@example.com, got %+v", repo.bumpCalls)
	}
}

func TestChangePassword_WrongOldPassword(t *testing.T) {
	hasher := auth.NewArgon2idHasher(auth.DefaultPasswordParams())
	hash, _ := hasher.Hash("correct-horse-battery-staple")
	repo := &recordingAuthRepo{
		getByExtUser: domain.User{
			Id:             "uuid-1",
			ExternalUserId: "email:test@example.com",
			Email:          "test@example.com",
			Source:         "password",
			PasswordHash:   hash,
		},
	}
	svc := newTestAuthService(t, repo)
	err := svc.ChangePassword(context.Background(), "email:test@example.com", "wrong-old-pass-12+chars", "new-correct-horse-12+chars")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("expected ErrBadCredentials, got %v", err)
	}
	if len(repo.updCalls) != 0 {
		t.Fatalf("update should not be called when old password wrong")
	}
}

func TestChangePassword_HappyPath(t *testing.T) {
	hasher := auth.NewArgon2idHasher(auth.DefaultPasswordParams())
	hash, _ := hasher.Hash("correct-horse-battery-staple")
	repo := &recordingAuthRepo{
		getByExtUser: domain.User{
			Id:             "uuid-1",
			ExternalUserId: "email:test@example.com",
			Email:          "test@example.com",
			Source:         "password",
			PasswordHash:   hash,
		},
	}
	svc := newTestAuthService(t, repo)
	err := svc.ChangePassword(context.Background(), "email:test@example.com", "correct-horse-battery-staple", "new-correct-horse-12+chars")
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if len(repo.updCalls) != 1 {
		t.Fatalf("expected 1 update call, got %d", len(repo.updCalls))
	}
	if repo.updCalls[0].Hash == "" || repo.updCalls[0].Algo != "argon2id" {
		t.Fatalf("update call fields wrong: %+v", repo.updCalls[0])
	}
}

func TestChangePassword_ShortNewPassword(t *testing.T) {
	repo := &recordingAuthRepo{}
	svc := newTestAuthService(t, repo)
	err := svc.ChangePassword(context.Background(), "email:test@example.com", "old-pass-12+chars", "short")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestChangePassword_UserNotFound(t *testing.T) {
	repo := &recordingAuthRepo{getByExtErr: domain.ErrNotFound}
	svc := newTestAuthService(t, repo)
	err := svc.ChangePassword(context.Background(), "missing-user", "old-pass-12+chars", "new-pass-12+chars")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument (user not found), got %v", err)
	}
}
