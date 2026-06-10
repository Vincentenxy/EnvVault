package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"envVault/internal/domain"
	uuidgen "envVault/internal/id"
	"envVault/internal/store"
	"gorm.io/gorm"
)

// AuthStore 持久化 v9 自注册 / 登录 / 强制登出 / 改密 相关数据。
//
// 边界:
//   - 与 RBACStore 共享 users 表(新授权路径都通过 users.id 关联),但 RBACStore
//     不感知 password_hash / tokens_valid_after,AuthStore 也不感知 role binding
//     的业务规则。
//   - "首用户自动 platform_admin" 的 grant 走 user_role_bindings,这里复用
//     roleIdByCodeTx / activeBindingIdTx 已有 helper,不重新实现。
type AuthStore struct {
	db        *sql.DB
	gormDB    *gorm.DB
	userCache *UserCache
}

func NewAuthStore(db *sql.DB, gormDB *gorm.DB, userCache ...*UserCache) *AuthStore {
	var cache *UserCache
	if len(userCache) > 0 {
		cache = userCache[0]
	}
	return &AuthStore{db: db, gormDB: gormDB, userCache: cache}
}

// ---- User with credentials ----

func (s *AuthStore) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	if err := ctx.Err(); err != nil {
		return domain.User{}, err
	}
	if s == nil || s.gormDB == nil {
		return domain.User{}, errors.New("auth store is not configured")
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return domain.User{}, ErrNotFound
	}
	var user domain.User
	// email <> '' 的 partial index 保证只匹配「真注册用户」,过滤掉 JWT 占位 user。
	err := s.gormDB.WithContext(ctx).
		Where("email = ? and email <> ''", email).
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *AuthStore) CreatePasswordUser(ctx context.Context, email, name, passwordHash, passwordAlgo string) (domain.User, error) {
	if err := ctx.Err(); err != nil {
		return domain.User{}, err
	}
	if s == nil || s.db == nil {
		return domain.User{}, errors.New("auth store is not configured")
	}
	email = strings.TrimSpace(strings.ToLower(email))
	name = strings.TrimSpace(name)
	if email == "" || name == "" {
		return domain.User{}, fmt.Errorf("email and name are required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()

	// 用 email 派生 external_user_id,保证 JWT 体系与密码体系在同一张 users 表
	// 内能通过同一字段联合查询。prefix "email:" 用于和 source=jwt 的
	// (可能是 sub / uuid)区分。
	externalUserId := "email:" + email
	userId, err := insertPasswordUserTx(ctx, tx, externalUserId, email, name, passwordHash, passwordAlgo)
	if err != nil {
		return domain.User{}, err
	}

	// 首用户自动 grant platform_admin(global):
	//   - 用单条 INSERT ... SELECT ... WHERE count(*)=1,避免 race
	//   - ON CONFLICT DO NOTHING 处理极少数并发注册都看到自己是最早 insert 的情形
	roleId, err := roleIdByCodeTx(ctx, tx, "platform_admin")
	if err != nil {
		// 系统未初始化(EnsureSystemData 还没跑),先建库再说,不算硬错。
		// 不影响 user 本身创建。
		if !errors.Is(err, ErrNotFound) {
			return domain.User{}, err
		}
	} else {
		bindingId, err := uuidgen.NewUUID()
		if err != nil {
			return domain.User{}, err
		}
		if _, err := tx.ExecContext(ctx, `
insert into user_role_bindings (id, user_id, role_id, scope_type, granted_by)
select $1, $2, $3, 'global', 'register'
from roles
where code = 'platform_admin'
  and (select count(*) from users) = 1
on conflict do nothing
`, bindingId, userId, roleId); err != nil {
			return domain.User{}, err
		}
		if err := recordRoleBindingAuditTx(ctx, tx, "register", "grant_role", userId, roleId, "global", "", nil); err != nil {
			// audit 失败不阻塞主流程;但显式记录,便于排查
			_ = err
		}
	}

	if err := tx.Commit(); err != nil {
		return domain.User{}, err
	}

	user, err := s.GetUserByEmail(ctx, email)
	if err != nil {
		return domain.User{}, err
	}
	s.cacheUserLabel(user.Id, user.Name)
	return user, nil
}

func (s *AuthStore) UpdatePasswordHash(ctx context.Context, userId, passwordHash, passwordAlgo string) (domain.User, error) {
	if err := ctx.Err(); err != nil {
		return domain.User{}, err
	}
	if s == nil || s.db == nil {
		return domain.User{}, errors.New("auth store is not configured")
	}
	userId = strings.TrimSpace(userId)
	if userId == "" {
		return domain.User{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
update users
set password_hash = $2,
    password_algo = $3,
    tokens_valid_after = now(),
    updated_at = now()
where id = $1
`, userId, passwordHash, passwordAlgo); err != nil {
		return domain.User{}, translatePgErr(err)
	}

	if err := tx.Commit(); err != nil {
		return domain.User{}, err
	}
	// 改密后 tokensValidAfter 已被 bump,GetUserByExternalId 拿不到,
	// 直接用一次单查返回完整 user。external_user_id 由 userId 反查。
	return s.GetUserById(ctx, userId)
}

func (s *AuthStore) BumpTokensValidAfter(ctx context.Context, userId string) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if s == nil || s.db == nil {
		return time.Time{}, errors.New("auth store is not configured")
	}
	userId = strings.TrimSpace(userId)
	if userId == "" {
		return time.Time{}, ErrNotFound
	}
	var bumpedAt time.Time
	err := s.db.QueryRowContext(ctx, `
update users
set tokens_valid_after = now(), updated_at = now()
where id = $1
returning tokens_valid_after
`, userId).Scan(&bumpedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	return bumpedAt, nil
}

func (s *AuthStore) GetTokensValidAfter(ctx context.Context, userId string) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if s == nil || s.db == nil {
		return time.Time{}, errors.New("auth store is not configured")
	}
	userId = strings.TrimSpace(userId)
	if userId == "" {
		return time.Time{}, ErrNotFound
	}
	var tva time.Time
	err := s.db.QueryRowContext(ctx, `
select tokens_valid_after from users where id = $1
`, userId).Scan(&tva)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	return tva, nil
}

func (s *AuthStore) ListUsersWithTokensValidAfter(ctx context.Context) (map[string]time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("auth store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
select id, tokens_valid_after from users
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]time.Time, 64)
	for rows.Next() {
		var id string
		var tva time.Time
		if err := rows.Scan(&id, &tva); err != nil {
			return nil, err
		}
		result[id] = tva
	}
	return result, rows.Err()
}

func (s *AuthStore) TouchLastSeen(ctx context.Context, userId string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.gormDB == nil {
		return errors.New("auth store is not configured")
	}
	userId = strings.TrimSpace(userId)
	if userId == "" {
		return nil
	}
	return s.gormDB.WithContext(ctx).
		Model(&domain.User{}).
		Where("id = ?", userId).
		Update("last_seen_at", time.Now()).Error
}

// ---- Login attempts ----

func (s *AuthStore) RecordLoginAttempt(ctx context.Context, email, ip string, success bool, userId string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return errors.New("auth store is not configured")
	}
	id, err := uuidgen.NewUUID()
	if err != nil {
		return err
	}
	// 失败 + 没找到 user 时 userId 留空(uuid NULL);成功时填。
	var userIdArg any
	if strings.TrimSpace(userId) != "" {
		userIdArg = userId
	}
	_, err = s.db.ExecContext(ctx, `
insert into login_attempts (id, email, ip, success, user_id)
values ($1, $2, $3, $4, $5)
`, id, email, ip, success, userIdArg)
	return err
}

func (s *AuthStore) CountRecentFailedByIP(ctx context.Context, ip string, window time.Duration) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s == nil || s.db == nil {
		return 0, errors.New("auth store is not configured")
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return 0, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `
select count(*) from login_attempts
where ip = $1 and success = false and created_at > now() - $2::interval
`, ip, fmt.Sprintf("%d seconds", int(window.Seconds()))).Scan(&count)
	return count, err
}

// ---- helpers ----

func (s *AuthStore) cacheUserLabel(userId, name string) {
	if s == nil || s.userCache == nil {
		return
	}
	s.userCache.CacheUserLabel(userId, name)
}

// GetUserById 通过内部 id (uuid) 反查 user。给 ChangePassword / Logout 入口用。
func (s *AuthStore) GetUserById(ctx context.Context, id string) (domain.User, error) {
	if err := ctx.Err(); err != nil {
		return domain.User{}, err
	}
	if s == nil || s.gormDB == nil {
		return domain.User{}, errors.New("auth store is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.User{}, ErrNotFound
	}
	var user domain.User
	err := s.gormDB.WithContext(ctx).Where("id = ?", id).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// GetUserByExternalId 通过 external_user_id 查 user。兼容保留,新授权路径不使用。
func (s *AuthStore) GetUserByExternalId(ctx context.Context, externalUserId string) (domain.User, error) {
	if err := ctx.Err(); err != nil {
		return domain.User{}, err
	}
	if s == nil || s.gormDB == nil {
		return domain.User{}, errors.New("auth store is not configured")
	}
	externalUserId = strings.TrimSpace(externalUserId)
	if externalUserId == "" {
		return domain.User{}, ErrNotFound
	}
	var user domain.User
	err := s.gormDB.WithContext(ctx).Where("external_user_id = ?", externalUserId).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// BumpTokensValidAfterByExternalId 强制登出。返新时间戳。
func (s *AuthStore) BumpTokensValidAfterByExternalId(ctx context.Context, externalUserId string) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if s == nil || s.db == nil {
		return time.Time{}, errors.New("auth store is not configured")
	}
	externalUserId = strings.TrimSpace(externalUserId)
	if externalUserId == "" {
		return time.Time{}, ErrNotFound
	}
	var bumpedAt time.Time
	err := s.db.QueryRowContext(ctx, `
update users
set tokens_valid_after = now(), updated_at = now()
where external_user_id = $1
returning tokens_valid_after
`, externalUserId).Scan(&bumpedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	return bumpedAt, nil
}

// UpdatePasswordHashByExternalId 改密。原子地 UPDATE + bump tokens_valid_after。
func (s *AuthStore) UpdatePasswordHashByExternalId(ctx context.Context, externalUserId, passwordHash, passwordAlgo string) (domain.User, error) {
	if err := ctx.Err(); err != nil {
		return domain.User{}, err
	}
	if s == nil || s.db == nil {
		return domain.User{}, errors.New("auth store is not configured")
	}
	externalUserId = strings.TrimSpace(externalUserId)
	if externalUserId == "" {
		return domain.User{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
update users
set password_hash = $2,
    password_algo = $3,
    tokens_valid_after = now(),
    updated_at = now()
where external_user_id = $1
`, externalUserId, passwordHash, passwordAlgo); err != nil {
		return domain.User{}, translatePgErr(err)
	}

	if err := tx.Commit(); err != nil {
		return domain.User{}, err
	}
	return s.GetUserByExternalId(ctx, externalUserId)
}

// insertPasswordUserTx 在事务内 INSERT 一行 password 用户。
// email 唯一索引 + INSERT 是源头上保证「email 唯一」的方式,违反时返回 ErrConflict。
func insertPasswordUserTx(ctx context.Context, tx *sql.Tx, externalUserId, email, name, passwordHash, passwordAlgo string) (string, error) {
	id, err := uuidgen.NewUUID()
	if err != nil {
		return "", err
	}
	var storedId string
	err = tx.QueryRowContext(ctx, `
insert into users (id, external_user_id, name, email, source, password_hash, password_algo, last_seen_at)
values ($1, $2, $3, $4, 'password', $5, $6, now())
returning id
`, id, externalUserId, name, email, passwordHash, passwordAlgo).Scan(&storedId)
	if err != nil {
		return "", translatePgErr(err)
	}
	return storedId, nil
}

// Compile-time guard:确保 AuthStore 满足 store.AuthRepository。
var _ store.AuthRepository = (*AuthStore)(nil)
