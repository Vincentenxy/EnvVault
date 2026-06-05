// Package ratelimit 实现 v9 登录 / 自注册的 IP 频控。
//
// 算法:Redis sliding window + hard lockout key。
//   - 每 IP 维护一个 sorted set,score = epoch ms,member = "<ts>-<rand>"
//     (rand 用于避免同 ms 并发写时 member 冲突,导致 ZADD 覆盖)
//   - 每次失败: ZADD <ts>; ZREMRANGEBYSCORE [0, now-window]; EXPIRE window
//   - 成功: DEL failures set; DEL lockout key(清空所有失败记录 + 解除封禁)
//   - Check 流程(每个失败都触发一次,所以第 6 次尝试进入时 lockout 一定已存在):
//     1) lockout key 存在 → ErrRateLimited
//     2) 过去 window 内失败次数 >= max → 设 lockout key (LockoutPeriod TTL) → ErrRateLimited
//     3) 否则 OK
//   - 这样 5 次失败后,第 6 次 Check 触发 lockout,后续 15min 内所有 Check 都被拒。
//   - 成功登录清空失败 + lockout,符合「密码对了就解除封禁」的直觉。
//
// 为什么不用 GCRA / token bucket:
//   - v9 只关心 login,流量低、状态简单,sliding window 直观且够用;
//   - GCRA 节省内存但代码复杂,无必要。
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/go-redis/redis/v8"
)

// ErrRateLimited 当 ip 触发频控时返回。
var ErrRateLimited = errors.New("rate limited")

// Limiter 是 rate limit 抽象。auth_service 只依赖此接口,
// 便于测试时注入 fake / miniredis。
type Limiter interface {
	// Check 返回 nil 即放行,ErrRateLimited 即拒绝。
	// 不写状态(纯读);若 ip 因 Check 触发 lockout,会让本调用顺便设上。
	Check(ctx context.Context, ip string) error
	// Record 记录一次尝试结果。success=true 时清空该 ip 的失败记录 + 解除封禁。
	Record(ctx context.Context, ip string, success bool) error
}

// Options 配置 sliding window 参数。
type Options struct {
	// Window 失败计数滑动窗口,默认 1min。
	Window time.Duration
	// MaxAttempts 窗口内允许的最大失败次数,默认 5。
	MaxAttempts int
	// LockoutPeriod 触发 lockout 后的封禁时长,默认 15min。
	LockoutPeriod time.Duration
	// KeyPrefix redis key 前缀,默认 "ratelimit:login"。
	KeyPrefix string
	// Now 时间源;为 nil 时默认 time.Now,测试可注入。
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.Window <= 0 {
		o.Window = time.Minute
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.LockoutPeriod <= 0 {
		o.LockoutPeriod = 15 * time.Minute
	}
	if o.KeyPrefix == "" {
		o.KeyPrefix = "ratelimit:login"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// redisOps 抽象出 rate limit 真正用到的一小撮 Redis op。
// 生产实现:redisClientAdapter(包 *goredis.UniversalClient)。
// 测试实现:in-memory fake。
type redisOps interface {
	ZAdd(ctx context.Context, key string, score float64, member string) error
	ZRemRangeByScore(ctx context.Context, key string, min, max string) error
	ZCard(ctx context.Context, key string) (int64, error)
	Del(ctx context.Context, keys ...string) error
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Exists(ctx context.Context, keys ...string) (int64, error)
}

// RedisLimiter 是 Limiter 的 Redis sliding window 实现。
// 零值不可用;必须经 NewRedisLimiter 构造。
type RedisLimiter struct {
	opts Options
	ops  redisOps
}

// NewRedisLimiter 构造生产用 Limiter。keyPrefix 自动追加 ":ip:" 与 IP。
func NewRedisLimiter(client goredis.UniversalClient, opts Options) *RedisLimiter {
	return &RedisLimiter{
		opts: opts.withDefaults(),
		ops:  &redisAdapter{client: client},
	}
}

// newRedisLimiterWithOps 测试入口,允许注入 fake redisOps。
func newRedisLimiterWithOps(ops redisOps, opts Options) *RedisLimiter {
	return &RedisLimiter{opts: opts.withDefaults(), ops: ops}
}

func (l *RedisLimiter) failuresKey(ip string) string {
	return fmt.Sprintf("%s:ip:%s:failures", l.opts.KeyPrefix, ip)
}

func (l *RedisLimiter) lockoutKey(ip string) string {
	return fmt.Sprintf("%s:ip:%s:lockout", l.opts.KeyPrefix, ip)
}

func (l *RedisLimiter) Check(ctx context.Context, ip string) error {
	if ip == "" {
		return nil
	}
	// 1) 已被 lockout → 立即拒
	exists, err := l.ops.Exists(ctx, l.lockoutKey(ip))
	if err != nil {
		return err
	}
	if exists > 0 {
		return ErrRateLimited
	}

	// 2) 失败计数超阈值 → 触发 lockout
	count, err := l.ops.ZCard(ctx, l.failuresKey(ip))
	if err != nil {
		return err
	}
	if int(count) >= l.opts.MaxAttempts {
		// SetNX 防止并发:即使两个请求同时跨阈值,lockout 也只被设一次,
		// TTL = LockoutPeriod,过期后自动解除。
		_, _ = l.ops.SetNX(ctx, l.lockoutKey(ip), "1", l.opts.LockoutPeriod)
		return ErrRateLimited
	}
	return nil
}

func (l *RedisLimiter) Record(ctx context.Context, ip string, success bool) error {
	if ip == "" {
		return nil
	}
	if success {
		// 清空失败计数 + lockout
		return l.ops.Del(ctx, l.failuresKey(ip), l.lockoutKey(ip))
	}
	// 失败:加一个唯一 member,score = epoch ms
	now := l.opts.Now()
	score := float64(now.UnixMilli())
	member := fmt.Sprintf("%d-%s", now.UnixNano(), randHex(8))
	if err := l.ops.ZAdd(ctx, l.failuresKey(ip), score, member); err != nil {
		return err
	}
	// 顺便按 [0, now-window] 清掉过期成员(降内存)
	minScore := "0"
	maxScore := strconv.FormatInt(now.Add(-l.opts.Window).UnixMilli(), 10)
	if err := l.ops.ZRemRangeByScore(ctx, l.failuresKey(ip), minScore, maxScore); err != nil {
		return err
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- redisOps adapter for production ----

type redisAdapter struct {
	client goredis.UniversalClient
}

func (a *redisAdapter) ZAdd(ctx context.Context, key string, score float64, member string) error {
	return a.client.ZAdd(ctx, key, &goredis.Z{Score: score, Member: member}).Err()
}

func (a *redisAdapter) ZRemRangeByScore(ctx context.Context, key, min, max string) error {
	return a.client.ZRemRangeByScore(ctx, key, min, max).Err()
}

func (a *redisAdapter) ZCard(ctx context.Context, key string) (int64, error) {
	return a.client.ZCard(ctx, key).Result()
}

func (a *redisAdapter) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return a.client.Del(ctx, keys...).Err()
}

func (a *redisAdapter) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	return a.client.SetNX(ctx, key, value, ttl).Result()
}

func (a *redisAdapter) Exists(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	return a.client.Exists(ctx, keys...).Result()
}
