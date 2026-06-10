package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"envVault/internal/auth"
	"envVault/internal/auth/ratelimit"
	"envVault/internal/domain"
	"envVault/internal/store"
)

// Auth 业务层错误。
//
// 错误码映射(在 HTTP controller 层统一处理):
//   - ErrInvalidArgument  → 1000(参数错)
//   - ErrEmailAlreadyExists → 1001(email 已被注册)
//   - ErrBadCredentials   → 1003(邮箱或密码错)
//   - ratelimit.ErrRateLimited → 1002(频控,HTTP 429)
//
// ErrEmailNotFound 也归到 ErrBadCredentials(避免泄漏「邮箱是否存在」信息)。
var (
	ErrInvalidArgument    = errors.New("invalid argument")
	ErrEmailAlreadyExists = errors.New("email already exists")
	ErrBadCredentials     = errors.New("bad credentials")
)

// AuthToken 是 Register / Login 成功后下发的 token 响应。
type AuthToken struct {
	UserId    string    `json:"userId"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// AuthService v9 自注册 / 登录 / 强制登出 / 改密的业务编排。
//
// 与 RBACService 的边界:
//   - RBACService 操作 role binding;
//   - AuthService 操作 password + tokens_valid_after,首用户自动 grant platform_admin
//     是 AuthService 在事务里委托给 store.AuthRepository 完成的(走 user_role_bindings)。
//
// 与 store.AuthRepository 的边界:
//   - store 只做 SQL 持久化(insert user / update password / bump tokens_valid_after);
//   - service 做业务校验(email 格式、密码长度、first-user grant、ratelimit、签 token)。
type AuthService interface {
	// Register 自助注册。首用户自动获得 platform_admin(global)权限。
	// 校验:email 非空 + 格式合法,name 非空,password ≥ PasswordMinLen。
	// 失败:ErrInvalidArgument / ErrEmailAlreadyExists / ratelimit.ErrRateLimited。
	Register(ctx context.Context, email, name, password string) (AuthToken, error)
	// Login 邮箱 + 密码登录。ratelimit 限流,失败次数超阈值返 ratelimit.ErrRateLimited。
	// 失败:ErrBadCredentials(包含「邮箱不存在」与「密码错」,防止 user enumeration);
	//       ratelimit.ErrRateLimited。
	Login(ctx context.Context, email, password, ip string) (AuthToken, error)
	// Logout 强制登出。UPDATE tokens_valid_after = NOW() + cache.Set,
	// 旧 token 立即失效(本进程)+ 最迟 1min 内全集群同步(refresher)。
	Logout(ctx context.Context, userId string) error
	// ChangePassword 改密。校验旧密码正确性 + 新密码 ≥ PasswordMinLen。
	// 成功后 UPDATE password_hash + bump tokens_valid_after(等同 logout)。
	ChangePassword(ctx context.Context, userId, oldPassword, newPassword string) error
}

// AuthServiceOptions 装配参数。零值不直接用,NewAuthService 会填默认。
type AuthServiceOptions struct {
	AuthRepo       store.AuthRepository
	PasswordHasher auth.PasswordHasher
	Limiter        ratelimit.Limiter
	TokensCache    *auth.TokensCache
	// PrivateKeyPEM 用于 SignToken。必填。
	PrivateKeyPEM string
	// TokenTTL JWT 有效期,默认 24h。
	TokenTTL time.Duration
	// PasswordMinLen 密码最小长度,默认 12。
	PasswordMinLen int
}

func (o AuthServiceOptions) withDefaults() AuthServiceOptions {
	if o.TokenTTL <= 0 {
		o.TokenTTL = 24 * time.Hour
	}
	if o.PasswordMinLen <= 0 {
		o.PasswordMinLen = 12
	}
	return o
}

type authService struct {
	opts AuthServiceOptions
}

// NewAuthService 构造 AuthService。AuthRepo / PasswordHasher / Limiter / PrivateKeyPEM 必填。
func NewAuthService(opts AuthServiceOptions) AuthService {
	return &authService{opts: opts.withDefaults()}
}

// ---- 占位实现(#28 / #29 会替换为真实逻辑) ----

func (s *authService) Register(ctx context.Context, email, name, password string) (AuthToken, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if err := validateEmail(email); err != nil {
		return AuthToken{}, err
	}
	if name == "" {
		return AuthToken{}, ErrInvalidArgument
	}
	if err := validatePassword(password, s.opts.PasswordMinLen); err != nil {
		return AuthToken{}, err
	}
	hash, err := s.opts.PasswordHasher.Hash(password)
	if err != nil {
		return AuthToken{}, err
	}
	user, err := s.opts.AuthRepo.CreatePasswordUser(ctx, email, name, hash, s.opts.PasswordHasher.Algo())
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return AuthToken{}, ErrEmailAlreadyExists
		}
		return AuthToken{}, err
	}
	// 记录首次登录尝试(success,无 ip 上下文:register 不走 rate limit)
	_ = s.opts.AuthRepo.RecordLoginAttempt(ctx, email, "", true, user.Id)
	token, err := s.issueToken(user)
	if err != nil {
		return AuthToken{}, err
	}
	return token, nil
}

func (s *authService) Login(ctx context.Context, email, password, ip string) (AuthToken, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return AuthToken{}, ErrBadCredentials
	}
	// 1) ratelimit:Check 决定是否放行进入 verify 流程
	if s.opts.Limiter != nil {
		if err := s.opts.Limiter.Check(ctx, ip); err != nil {
			return AuthToken{}, err
		}
	}
	// 2) 查 user
	user, err := s.opts.AuthRepo.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// 找不到 → bad cred,不泄漏「邮箱是否存在」
			_ = s.opts.AuthRepo.RecordLoginAttempt(ctx, email, ip, false, "")
			if s.opts.Limiter != nil {
				_ = s.opts.Limiter.Record(ctx, ip, false)
			}
			return AuthToken{}, ErrBadCredentials
		}
		return AuthToken{}, err
	}
	// user 找到但邮箱登录未启用(source != 'password' / 无 hash)→ bad cred
	if user.Source != "password" || user.PasswordHash == "" {
		_ = s.opts.AuthRepo.RecordLoginAttempt(ctx, email, ip, false, user.Id)
		if s.opts.Limiter != nil {
			_ = s.opts.Limiter.Record(ctx, ip, false)
		}
		return AuthToken{}, ErrBadCredentials
	}
	// 3) 校验密码
	ok, err := s.opts.PasswordHasher.Verify(user.PasswordHash, password)
	if err != nil || !ok {
		_ = s.opts.AuthRepo.RecordLoginAttempt(ctx, email, ip, false, user.Id)
		if s.opts.Limiter != nil {
			_ = s.opts.Limiter.Record(ctx, ip, false)
		}
		return AuthToken{}, ErrBadCredentials
	}
	// 4) 账户是否被禁用
	if user.IsDisabled {
		_ = s.opts.AuthRepo.RecordLoginAttempt(ctx, email, ip, false, user.Id)
		return AuthToken{}, ErrBadCredentials
	}
	// 5) 成功:清 ratelimit 失败计数,记成功,更新 last_seen
	if s.opts.Limiter != nil {
		_ = s.opts.Limiter.Record(ctx, ip, true)
	}
	_ = s.opts.AuthRepo.RecordLoginAttempt(ctx, email, ip, true, user.Id)
	_ = s.opts.AuthRepo.TouchLastSeen(ctx, user.Id)
	token, err := s.issueToken(user)
	if err != nil {
		return AuthToken{}, err
	}
	return token, nil
}

func (s *authService) Logout(ctx context.Context, userId string) error {
	if userId == "" {
		return ErrInvalidArgument
	}
	tva, err := s.opts.AuthRepo.BumpTokensValidAfter(ctx, userId)
	if err != nil {
		return err
	}
	// 立即让本进程 cache 看到新值,避免下一个请求被「cache 还显示旧 tva → iat 通过」放行
	if s.opts.TokensCache != nil {
		s.opts.TokensCache.Set(userId, tva)
	}
	return nil
}

func (s *authService) ChangePassword(ctx context.Context, userId, oldPassword, newPassword string) error {
	if userId == "" {
		return ErrInvalidArgument
	}
	if oldPassword == "" || newPassword == "" {
		return ErrInvalidArgument
	}
	if err := validatePassword(newPassword, s.opts.PasswordMinLen); err != nil {
		return err
	}
	user, err := s.opts.AuthRepo.GetUserById(ctx, userId)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrInvalidArgument
		}
		return err
	}
	// 旧密码校验
	ok, err := s.opts.PasswordHasher.Verify(user.PasswordHash, oldPassword)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadCredentials
	}
	// 新 hash + 一次性 bump tokens_valid_after
	newHash, err := s.opts.PasswordHasher.Hash(newPassword)
	if err != nil {
		return err
	}
	updated, err := s.opts.AuthRepo.UpdatePasswordHash(ctx, userId, newHash, s.opts.PasswordHasher.Algo())
	if err != nil {
		return err
	}
	// cache 同步:UpdatePasswordHash 内部已 bump,但进程内 cache 需主动 Set
	if s.opts.TokensCache != nil && updated.TokensValidAfter != nil {
		s.opts.TokensCache.Set(userId, *updated.TokensValidAfter)
	}
	return nil
}

// ---- helpers ----

// emailRegex 是 RFC 5322 的轻量子集。覆盖绝大多数现实 email,故意不追求 100% 标准
// (那会非常长且实际收益小),只拦截明显错误的输入。
var emailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func validateEmail(email string) error {
	if !emailRegex.MatchString(email) {
		return ErrInvalidArgument
	}
	return nil
}

func validatePassword(password string, minLen int) error {
	if len(password) < minLen {
		return ErrInvalidArgument
	}
	return nil
}

func (s *authService) issueToken(user domain.User) (AuthToken, error) {
	now := time.Now()
	exp := now.Add(s.opts.TokenTTL)
	claims := auth.Claims{
		UserId:           user.Id,
		Name:             user.Name,
		RegisteredClaims: auth.JWTRegisteredClaimsAt(now, exp, user.Id),
	}
	tokenStr, err := auth.SignToken(s.opts.PrivateKeyPEM, claims)
	if err != nil {
		return AuthToken{}, err
	}
	return AuthToken{
		UserId:    user.Id,
		Email:     user.Email,
		Name:      user.Name,
		Token:     tokenStr,
		IssuedAt:  now,
		ExpiresAt: exp,
	}, nil
}
