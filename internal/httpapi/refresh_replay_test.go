package httpapi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshReplayCacheSingleFlight(t *testing.T) {
	cache := newRefreshReplayCache(5 * time.Second)
	wanted := refreshSession{
		AccessToken:      "access-next",
		RefreshToken:     "refresh-next",
		AccessExpiresAt:  100,
		RefreshExpiresAt: 200,
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	rotate := func() (refreshSession, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return wanted, nil
	}
	type result struct {
		session refreshSession
		err     error
	}
	results := make(chan result, 2)
	go func() {
		session, err := cache.do(context.Background(), "same-client", rotate)
		results <- result{session: session, err: err}
	}()
	<-started
	go func() {
		session, err := cache.do(context.Background(), "same-client", rotate)
		results <- result{session: session, err: err}
	}()
	close(release)
	for range 2 {
		result := <-results
		if result.err != nil || result.session != wanted {
			t.Fatalf("unexpected replay result: session=%+v err=%v", result.session, result.err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh rotation calls = %d, want 1", calls.Load())
	}
}

func TestRefreshReplayCacheDoesNotCacheErrors(t *testing.T) {
	cache := newRefreshReplayCache(5 * time.Second)
	var calls atomic.Int32
	wanted := refreshSession{AccessToken: "access-next", RefreshToken: "refresh-next"}
	rotate := func() (refreshSession, error) {
		if calls.Add(1) == 1 {
			return refreshSession{}, errors.New("temporary failure")
		}
		return wanted, nil
	}
	if _, err := cache.do(context.Background(), "retryable-client", rotate); err == nil {
		t.Fatal("first refresh unexpectedly succeeded")
	}
	session, err := cache.do(context.Background(), "retryable-client", rotate)
	if err != nil || session != wanted {
		t.Fatalf("second refresh did not retry: session=%+v err=%v", session, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh rotation calls = %d, want 2", calls.Load())
	}
}

func TestRefreshReplayCacheInvalidatesUser(t *testing.T) {
	cache := newRefreshReplayCache(5 * time.Second)
	var calls atomic.Int32
	rotate := func() (refreshSession, error) {
		call := calls.Add(1)
		suffix := "first"
		if call > 1 {
			suffix = "second"
		}
		return refreshSession{
			UserID:       "user-1",
			AccessToken:  "access-" + suffix,
			RefreshToken: "refresh-" + suffix,
		}, nil
	}
	first, err := cache.do(context.Background(), "old-token", rotate)
	if err != nil {
		t.Fatal(err)
	}
	cache.invalidateUser("user-1")
	second, err := cache.do(context.Background(), "old-token", rotate)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || first == second {
		t.Fatalf("user invalidation did not evict replay: calls=%d first=%+v second=%+v", calls.Load(), first, second)
	}
}
