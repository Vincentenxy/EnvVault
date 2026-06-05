package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokensCache_GetMiss_LoaderNil_ReturnsEpoch(t *testing.T) {
	c := NewTokensCache(TokensCacheOptions{})
	tva, ok := c.Get(context.Background(), "user-1")
	if ok {
		t.Fatalf("expected ok=false on cache miss + nil loader, got ok=true")
	}
	if !tva.IsZero() {
		t.Fatalf("expected zero time, got %v", tva)
	}
}

func TestTokensCache_GetMiss_LoaderSuccess_StoresAndReturns(t *testing.T) {
	want := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	c := NewTokensCache(TokensCacheOptions{
		PerUserLoader: func(_ context.Context, userId string) (time.Time, error) {
			if userId != "user-1" {
				t.Fatalf("unexpected userId: %s", userId)
			}
			return want, nil
		},
	})
	tva, ok := c.Get(context.Background(), "user-1")
	if !ok || !tva.Equal(want) {
		t.Fatalf("first Get: got (%v, %v), want (%v, true)", tva, ok, want)
	}
	// 第二次 Get 应当走 cache(loader 不应被再调)
	calls := 0
	c.perUserLoader = func(_ context.Context, _ string) (time.Time, error) {
		calls++
		return time.Time{}, nil
	}
	tva, _ = c.Get(context.Background(), "user-1")
	if calls != 0 {
		t.Fatalf("loader should not be called on cache hit, got %d calls", calls)
	}
	if !tva.Equal(want) {
		t.Fatalf("cached value mismatch: got %v want %v", tva, want)
	}
}

func TestTokensCache_GetMiss_LoaderError_DegradesToEpoch(t *testing.T) {
	c := NewTokensCache(TokensCacheOptions{
		PerUserLoader: func(_ context.Context, _ string) (time.Time, error) {
			return time.Time{}, errors.New("db down")
		},
	})
	tva, ok := c.Get(context.Background(), "user-1")
	if ok {
		t.Fatalf("expected ok=false on loader error, got ok=true")
	}
	if !tva.IsZero() {
		t.Fatalf("expected epoch on loader error, got %v", tva)
	}
}

func TestTokensCache_Set_ImmediatelyVisible(t *testing.T) {
	c := NewTokensCache(TokensCacheOptions{})
	c.Set("user-1", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	tva, ok := c.Get(context.Background(), "user-1")
	if !ok {
		t.Fatalf("Set then Get should hit, got miss")
	}
	if tva.Year() != 2026 {
		t.Fatalf("unexpected tva: %v", tva)
	}
}

func TestTokensCache_Invalidate_RemovesEntry(t *testing.T) {
	c := NewTokensCache(TokensCacheOptions{})
	c.Set("user-1", time.Now())
	c.Invalidate("user-1")
	if c.Size() != 0 {
		t.Fatalf("Invalidate should remove entry, size=%d", c.Size())
	}
}

func TestTokensCache_SetBatch_ReplacesAll(t *testing.T) {
	c := NewTokensCache(TokensCacheOptions{})
	c.Set("user-1", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	c.Set("user-2", time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC))
	c.SetBatch(map[string]time.Time{
		"user-3": time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		"user-4": time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
	})
	if c.Size() != 2 {
		t.Fatalf("SetBatch should replace all, size=%d", c.Size())
	}
	if _, ok := c.Get(context.Background(), "user-1"); ok {
		t.Fatalf("user-1 should be gone after SetBatch")
	}
	if _, ok := c.Get(context.Background(), "user-3"); !ok {
		t.Fatalf("user-3 should be present after SetBatch")
	}
}

func TestTokensCache_Refresher_PeriodicLoad(t *testing.T) {
	var calls atomic.Int32
	c := NewTokensCache(TokensCacheOptions{
		Refresher: 30 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.RunRefresher(ctx, func(_ context.Context) (map[string]time.Time, error) {
		calls.Add(1)
		return map[string]time.Time{
			"u1": time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		}, nil
	})
	// 等 3 个周期
	time.Sleep(120 * time.Millisecond)
	cancel()
	if got := calls.Load(); got < 2 {
		t.Fatalf("refresher should have run >= 2 times, got %d", got)
	}
	if c.Size() != 1 {
		t.Fatalf("refresher should have populated 1 entry, got %d", c.Size())
	}
}

func TestTokensCache_Refresher_ContinuesOnError(t *testing.T) {
	var calls atomic.Int32
	c := NewTokensCache(TokensCacheOptions{
		Refresher: 30 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.RunRefresher(ctx, func(_ context.Context) (map[string]time.Time, error) {
		if calls.Add(1) <= 1 {
			return nil, errors.New("transient")
		}
		return map[string]time.Time{"u1": time.Now()}, nil
	})
	time.Sleep(100 * time.Millisecond)
	cancel()
	if c.Size() != 1 {
		t.Fatalf("refresher should have recovered from error, size=%d", c.Size())
	}
}

func TestTokensCache_Refresher_StopsOnContextCancel(t *testing.T) {
	var calls atomic.Int32
	c := NewTokensCache(TokensCacheOptions{
		Refresher: 20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	c.RunRefresher(ctx, func(_ context.Context) (map[string]time.Time, error) {
		calls.Add(1)
		return nil, nil
	})
	time.Sleep(50 * time.Millisecond)
	cancel()
	// 给 goroutine 退出时间
	time.Sleep(50 * time.Millisecond)
	afterCancel := calls.Load()
	time.Sleep(60 * time.Millisecond)
	if calls.Load() != afterCancel {
		t.Fatalf("refresher should stop after ctx cancel: before=%d after=%d", afterCancel, calls.Load())
	}
}

func TestTokensCache_NilSafe(t *testing.T) {
	var c *TokensCache
	if _, ok := c.Get(context.Background(), "u1"); ok {
		t.Fatalf("nil cache Get should return false")
	}
	c.Set("u1", time.Now()) // should not panic
	c.Invalidate("u1")
	c.SetBatch(nil)
	c.RunRefresher(context.Background(), nil)
	if c.Size() != 0 {
		t.Fatalf("nil cache Size should be 0, got %d", c.Size())
	}
}
