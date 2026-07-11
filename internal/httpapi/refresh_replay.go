package httpapi

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/xtawa/classing-backend/internal/auth"
)

var (
	errRefreshSessionIssue      = errors.New("issue refreshed access token")
	errRefreshReplayInvalidated = errors.New("refresh replay invalidated")
)

type refreshSession struct {
	UserID           string `json:"-"`
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	AccessExpiresAt  int64  `json:"accessExpiresAt"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt"`
}

func (c *refreshReplayCache) invalidateUser(userID string) {
	if userID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if entry.completed && entry.err == nil && entry.session.UserID == userID {
			entry.invalidated = true
			delete(c.entries, key)
		}
	}
}

type refreshReplayEntry struct {
	done        chan struct{}
	session     refreshSession
	err         error
	completed   bool
	invalidated bool
	expiresAt   time.Time
}

type refreshReplayCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*refreshReplayEntry
}

func newRefreshReplayCache(ttl time.Duration) *refreshReplayCache {
	return &refreshReplayCache{
		ttl:     ttl,
		entries: map[string]*refreshReplayEntry{},
	}
}

func refreshReplayKey(refreshToken, ipAddress, userAgent string) string {
	return auth.HashOpaqueToken(refreshToken + "\x00" + ipAddress + "\x00" + userAgent)
}

func (c *refreshReplayCache) do(
	ctx context.Context,
	key string,
	rotate func() (refreshSession, error),
) (refreshSession, error) {
	now := time.Now()
	c.mu.Lock()
	for entryKey, entry := range c.entries {
		if entry.completed && !entry.expiresAt.After(now) {
			delete(c.entries, entryKey)
		}
	}
	if existing, ok := c.entries[key]; ok {
		done := existing.done
		c.mu.Unlock()
		select {
		case <-done:
			c.mu.Lock()
			session, err := existing.session, existing.err
			if existing.invalidated {
				session = refreshSession{}
				err = errRefreshReplayInvalidated
			}
			c.mu.Unlock()
			return session, err
		case <-ctx.Done():
			return refreshSession{}, ctx.Err()
		}
	}

	entry := &refreshReplayEntry{done: make(chan struct{})}
	c.entries[key] = entry
	c.mu.Unlock()

	session, err := rotate()
	c.mu.Lock()
	entry.session = session
	entry.err = err
	entry.completed = true
	if err == nil {
		entry.expiresAt = time.Now().Add(c.ttl)
	} else {
		delete(c.entries, key)
	}
	close(entry.done)
	c.mu.Unlock()
	return session, err
}
