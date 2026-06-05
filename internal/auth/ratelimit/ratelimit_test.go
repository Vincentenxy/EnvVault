package ratelimit

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeRedisOps 是 redisOps 的 in-memory 实现,按 sorted set 语义 + 普通 key/value 模拟。
// 仅用于 unit test,线程安全。
type fakeRedisOps struct {
	mu      sync.Mutex
	zsets   map[string]map[string]float64 // key -> member -> score
	strings map[string]string             // key -> value(lockout / probe)
	ttls    map[string]time.Duration
}

func newFakeRedisOps() *fakeRedisOps {
	return &fakeRedisOps{
		zsets:   make(map[string]map[string]float64),
		strings: make(map[string]string),
		ttls:    make(map[string]time.Duration),
	}
}

func (f *fakeRedisOps) ZAdd(_ context.Context, key string, score float64, member string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.zsets[key]; !ok {
		f.zsets[key] = make(map[string]float64)
	}
	f.zsets[key][member] = score
	return nil
}

func (f *fakeRedisOps) ZRemRangeByScore(_ context.Context, key, minStr, maxStr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	min, err := strconv.ParseFloat(minStr, 64)
	if err != nil {
		min = -1e18
	}
	max, err := strconv.ParseFloat(maxStr, 64)
	if err != nil {
		max = 1e18
	}
	members := f.zsets[key]
	for m, s := range members {
		if s >= min && s <= max {
			delete(members, m)
		}
	}
	return nil
}

func (f *fakeRedisOps) ZCard(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.zsets[key])), nil
}

func (f *fakeRedisOps) Del(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.strings, k)
		delete(f.ttls, k)
		delete(f.zsets, k)
	}
	return nil
}

func (f *fakeRedisOps) SetNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.strings[key]; ok {
		return false, nil // NX 语义:已存在则不写入
	}
	f.strings[key] = value
	f.ttls[key] = ttl
	return true, nil
}

func (f *fakeRedisOps) Exists(_ context.Context, keys ...string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, k := range keys {
		if _, ok := f.strings[k]; ok {
			n++
		}
	}
	return n, nil
}

// ---- 用例 ----

func TestCheck_AllowsUnderThreshold(t *testing.T) {
	ops := newFakeRedisOps()
	l := newRedisLimiterWithOps(ops, Options{
		Window: time.Minute, MaxAttempts: 5, LockoutPeriod: 15 * time.Minute,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	for i := 0; i < 4; i++ {
		if err := l.Check(context.Background(), "1.2.3.4"); err != nil {
			t.Fatalf("attempt %d should be allowed, got %v", i+1, err)
		}
		_ = l.Record(context.Background(), "1.2.3.4", false)
	}
}

func TestCheck_LocksOutAfterMaxAttempts(t *testing.T) {
	ops := newFakeRedisOps()
	l := newRedisLimiterWithOps(ops, Options{
		Window: time.Minute, MaxAttempts: 5, LockoutPeriod: 15 * time.Minute,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	// 5 次失败
	for i := 0; i < 5; i++ {
		_ = l.Record(context.Background(), "1.2.3.4", false)
	}
	// 第 6 次 Check → lockout
	if err := l.Check(context.Background(), "1.2.3.4"); err != ErrRateLimited {
		t.Fatalf("6th check should be rate limited, got %v", err)
	}
	// 后续 Check 都应被拒
	for i := 0; i < 3; i++ {
		if err := l.Check(context.Background(), "1.2.3.4"); err != ErrRateLimited {
			t.Fatalf("post-lockout check %d should be rate limited, got %v", i+1, err)
		}
	}
}

func TestRecord_Success_ClearsFailuresAndLockout(t *testing.T) {
	ops := newFakeRedisOps()
	l := newRedisLimiterWithOps(ops, Options{
		Window: time.Minute, MaxAttempts: 5, LockoutPeriod: 15 * time.Minute,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	// 4 次失败
	for i := 0; i < 4; i++ {
		_ = l.Record(context.Background(), "1.2.3.4", false)
	}
	// 1 次成功
	if err := l.Record(context.Background(), "1.2.3.4", true); err != nil {
		t.Fatalf("record success: %v", err)
	}
	// 失败计数应清空
	count, _ := ops.ZCard(context.Background(), l.failuresKey("1.2.3.4"))
	if count != 0 {
		t.Fatalf("after success, failures should be cleared, got %d", count)
	}
	// 此时再失败 5 次 + 1 次 Check,应正常 lockout
	for i := 0; i < 5; i++ {
		_ = l.Record(context.Background(), "1.2.3.4", false)
	}
	if err := l.Check(context.Background(), "1.2.3.4"); err != ErrRateLimited {
		t.Fatalf("after 5 fresh failures, check should be rate limited")
	}
	// 成功 → lockout 也清
	_ = l.Record(context.Background(), "1.2.3.4", true)
	exists, _ := ops.Exists(context.Background(), l.lockoutKey("1.2.3.4"))
	if exists != 0 {
		t.Fatalf("lockout key should be deleted after success")
	}
	// 此时 check 应放行
	if err := l.Check(context.Background(), "1.2.3.4"); err != nil {
		t.Fatalf("post-cleared check should be allowed, got %v", err)
	}
}

func TestCheck_EmptyIP_AlwaysAllowed(t *testing.T) {
	l := newRedisLimiterWithOps(newFakeRedisOps(), Options{})
	if err := l.Check(context.Background(), ""); err != nil {
		t.Fatalf("empty ip should be allowed, got %v", err)
	}
}

func TestCheck_DifferentIPsIsolated(t *testing.T) {
	ops := newFakeRedisOps()
	l := newRedisLimiterWithOps(ops, Options{
		Window: time.Minute, MaxAttempts: 2, LockoutPeriod: 15 * time.Minute,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	// 1.2.3.4 失败 2 次
	for i := 0; i < 2; i++ {
		_ = l.Record(context.Background(), "1.2.3.4", false)
	}
	// 1.2.3.4 锁
	if err := l.Check(context.Background(), "1.2.3.4"); err != ErrRateLimited {
		t.Fatalf("1.2.3.4 should be locked")
	}
	// 5.6.7.8 不受影响
	if err := l.Check(context.Background(), "5.6.7.8"); err != nil {
		t.Fatalf("5.6.7.8 should not be affected, got %v", err)
	}
}

func TestRecord_UniqueMembersUnderBurst(t *testing.T) {
	// 同一 ms 内连续 Record 多次,member 必须不同(否则 ZADD 覆盖 → count 错)
	ops := newFakeRedisOps()
	fixed := time.Unix(0, 0)
	l := newRedisLimiterWithOps(ops, Options{
		Window: time.Minute, MaxAttempts: 100,
		Now: func() time.Time { return fixed },
	})
	for i := 0; i < 10; i++ {
		_ = l.Record(context.Background(), "1.2.3.4", false)
	}
	count, _ := ops.ZCard(context.Background(), l.failuresKey("1.2.3.4"))
	if count != 10 {
		t.Fatalf("burst of 10 in same ms should produce 10 distinct members, got %d", count)
	}
}
