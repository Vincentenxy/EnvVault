package auth

import (
	"context"
	"sync"
	"time"
)

// TokensCache 维护进程内 userId → tokensValidAfter 缓存。
//
// 为什么需要(而不是每次请求都查 DB):
//   - JWT middleware 在每条受保护请求上都要比对 tokens_valid_after > iat。
//     走 DB 一次 round-trip ≈ 1-5ms,高 QPS 下会成为热点。
//   - tokensValidAfter 的更新频率低(只在 logout / changePassword 时改),
//     完全可以靠 1min 进程内缓存兜底。
//
// 失效策略:
//   - Set(userId, tva):被 Logout / ChangePassword 主动调,立即生效。
//   - Get(userId):cache miss → perUserLoader 拉一次 DB;loader 失败时返 epoch(放行)。
//   - RunRefresher:后台 goroutine 周期性调 fullLoader 灌全量。
//
// 一致性:
//   - 不依赖分布式缓存,多实例部署时各进程独立,极端情况可有 1min 滞后。
//     Logout 调用所在实例会 Set,其他实例最迟 1min 内通过 refresher 同步。
//   - 这是 v9 选定的 web 单设备场景下的可接受 trade-off;若以后跨设备,需引入 Redis 共享层。
type TokensCache struct {
	mu     sync.RWMutex
	values map[string]time.Time

	perUserLoader func(ctx context.Context, userId string) (time.Time, error)
	refresher     time.Duration
}

// TokensCacheOptions 构造参数。
type TokensCacheOptions struct {
	// PerUserLoader 是 cache miss 时的 DB 拉取函数(拉单个 user)。
	// 若为 nil,cache miss 直接返 (epoch, false),middleware 视作放行。
	PerUserLoader func(ctx context.Context, userId string) (time.Time, error)
	// Refresher 是后台拉全量的周期,默认 1min。
	Refresher time.Duration
}

func (o TokensCacheOptions) withDefaults() TokensCacheOptions {
	if o.Refresher <= 0 {
		o.Refresher = time.Minute
	}
	return o
}

// NewTokensCache 构造缓存。perUserLoader 可选(nil 时 cache miss 直接返 epoch)。
func NewTokensCache(opts TokensCacheOptions) *TokensCache {
	opts = opts.withDefaults()
	return &TokensCache{
		values:        make(map[string]time.Time),
		perUserLoader: opts.PerUserLoader,
		refresher:     opts.Refresher,
	}
}

// Get 拿 userId 的 tokensValidAfter。
// 返 (tva, true) 当 cache hit / loader 成功;返 (epoch, false) 当 cache miss 且无 loader 或 loader 失败。
func (c *TokensCache) Get(ctx context.Context, userId string) (time.Time, bool) {
	if c == nil || userId == "" {
		return time.Time{}, false
	}
	c.mu.RLock()
	tva, ok := c.values[userId]
	c.mu.RUnlock()
	if ok {
		return tva, true
	}
	if c.perUserLoader == nil {
		return time.Time{}, false
	}
	loaded, err := c.perUserLoader(ctx, userId)
	if err != nil {
		// 拉取失败 → 不阻塞请求,放行(zero time)
		return time.Time{}, false
	}
	c.mu.Lock()
	c.values[userId] = loaded
	c.mu.Unlock()
	return loaded, true
}

// Set 直接设置某 user 的 tokensValidAfter。Logout / ChangePassword 流程用。
func (c *TokensCache) Set(userId string, tva time.Time) {
	if c == nil || userId == "" {
		return
	}
	c.mu.Lock()
	c.values[userId] = tva
	c.mu.Unlock()
}

// Invalidate 清除某 user 的缓存。Logout 流程在 Set 之后调,确保本进程下一次 Get
// 一定读到最新值(refresher 下一次循环前都不会被覆盖)。
// 实际上 Set 已经做了同样的事,这里保留为显式语义别名,便于 future Redis 共享层接入。
func (c *TokensCache) Invalidate(userId string) {
	if c == nil || userId == "" {
		return
	}
	c.mu.Lock()
	delete(c.values, userId)
	c.mu.Unlock()
}

// SetBatch 一次性灌入全量(供 Refresher 用)。
func (c *TokensCache) SetBatch(values map[string]time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.values = make(map[string]time.Time, len(values))
	for k, v := range values {
		c.values[k] = v
	}
	c.mu.Unlock()
}

// RunRefresher 启动后台 goroutine 周期调 fullLoader 灌全量。
// ctx 取消时退出。
func (c *TokensCache) RunRefresher(ctx context.Context, fullLoader func(ctx context.Context) (map[string]time.Time, error)) {
	if c == nil || fullLoader == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(c.refresher)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				values, err := fullLoader(ctx)
				if err != nil {
					continue // 单次失败不阻塞,下个周期重试
				}
				c.SetBatch(values)
			}
		}
	}()
}

// Size 返回当前缓存条目数。给 metrics / 测试用。
func (c *TokensCache) Size() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.values)
}
