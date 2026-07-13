package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xtawa/classing-backend/internal/model"
)

func createTestUser(t *testing.T, store *Store, n int) model.User {
	t.Helper()
	user, err := store.CreateUser(context.Background(), fmt.Sprintf("user%d", n), fmt.Sprintf("user%d@test.local", n), "dummyhash", "USER")
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestRedeemConcurrent(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			store := d.open(t)
			ctx := context.Background()
			admin := createTestUser(t, store, 0)
			codes, err := store.CreateRedeemCodes(ctx, admin.ID, "CAMPAIGN", 1, 30, 5, 0)
			if err != nil {
				t.Fatal(err)
			}
			code := codes[0]

			const N = 20
			users := make([]model.User, N)
			for i := 0; i < N; i++ {
				users[i] = createTestUser(t, store, i+1)
			}

			var successes, failures atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < N; i++ {
				wg.Add(1)
				go func(userID string) {
					defer wg.Done()
					<-start
					_, err := store.Redeem(ctx, userID, code.Code)
					if err == nil {
						successes.Add(1)
					} else {
						failures.Add(1)
					}
				}(users[i].ID)
			}
			close(start)
			wg.Wait()

			if got := successes.Load(); got != 5 {
				t.Errorf("successes = %d, want 5", got)
			}
			if got := failures.Load(); got != N-5 {
				t.Errorf("failures = %d, want %d", got, N-5)
			}
			var current int
			if err := store.db.GetContext(ctx, &current, store.rebind(`SELECT current_redemptions FROM redeem_codes WHERE code = ?`), code.Code); err != nil {
				t.Fatal(err)
			}
			if current != 5 {
				t.Errorf("current_redemptions = %d, want 5", current)
			}
		})
	}
}

func TestPutCloudDocumentConcurrent(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			store := d.open(t)
			ctx := context.Background()
			user := createTestUser(t, store, 0)

			if _, err := store.PutCloudDocument(ctx, user.ID, []byte(`{"v":1}`), 0); err != nil {
				t.Fatal(err)
			}

			const N = 10
			var successes atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < N; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := store.PutCloudDocument(ctx, user.ID, []byte(`{"v":2}`), 1)
					if err == nil {
						successes.Add(1)
					}
				}()
			}
			close(start)
			wg.Wait()

			if got := successes.Load(); got != 1 {
				t.Errorf("successes = %d, want 1", got)
			}
			doc, err := store.CloudDocument(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Version != 2 {
				t.Errorf("final version = %d, want 2", doc.Version)
			}
		})
	}
}

func TestPutCloudDocumentIdempotentCommitsWriteResponseAndAudit(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			data := d.open(t)
			ctx := context.Background()
			user := createTestUser(t, data, 0)
			payload := []byte(`{"format":"classing_cloud_sync_v2","updatedAt":0,"records":{},"changes":[],"devices":[]}`)
			hash := HashRequest(payload)
			audit := AuditContext{ActorID: user.ID, Action: "OFFICIAL_CLOUD_WRITE", TargetType: "CLOUD_DOCUMENT", TargetID: user.ID}

			item, replay, err := data.PutCloudDocumentIdempotent(ctx, user.ID, payload, 0, "request-1", hash, audit)
			if err != nil || replay != nil || item.Version != 1 {
				t.Fatalf("first write = %+v replay=%+v err=%v", item, replay, err)
			}
			_, replay, err = data.PutCloudDocumentIdempotent(ctx, user.ID, payload, 0, "request-1", hash, audit)
			if err != nil || replay == nil || replay.ResponseBody != `{"success":true,"version":1}` {
				t.Fatalf("replay = %+v err=%v", replay, err)
			}
			changed := []byte(`{"format":"classing_cloud_sync_v2","updatedAt":1,"records":{},"changes":[],"devices":[]}`)
			if _, _, err = data.PutCloudDocumentIdempotent(ctx, user.ID, changed, 1, "request-1", HashRequest(changed), audit); !errors.Is(err, ErrIdempotencyKeyReused) {
				t.Fatalf("different payload error = %v, want ErrIdempotencyKeyReused", err)
			}
			var audits int
			if err = data.db.GetContext(ctx, &audits, data.rebind(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND target_id = ?`), "OFFICIAL_CLOUD_WRITE", user.ID); err != nil {
				t.Fatal(err)
			}
			if audits != 1 {
				t.Fatalf("audit rows = %d, want 1", audits)
			}
		})
	}
}

func TestRuntimeEventsResumeInOrderForUserAndGlobalSettings(t *testing.T) {
	data := newSQLiteTestStore(t)
	ctx := context.Background()
	user := createTestUser(t, data, 0)
	payload := []byte(`{"format":"classing_cloud_sync_v2","updatedAt":0,"records":{},"changes":[],"devices":[]}`)
	audit := AuditContext{ActorID: user.ID, Action: "OFFICIAL_CLOUD_WRITE", TargetType: "CLOUD_DOCUMENT", TargetID: user.ID}
	if _, _, err := data.PutCloudDocumentIdempotent(ctx, user.ID, payload, 0, "", HashRequest(payload), audit); err != nil {
		t.Fatal(err)
	}
	if err := data.SetSettingsAudited(ctx, user.ID, map[string]string{"maintenance.message": "planned"}, AuditContext{ActorID: user.ID, Action: "SYSTEM_SETTINGS_UPDATE", TargetType: "SYSTEM_SETTINGS"}); err != nil {
		t.Fatal(err)
	}
	events, err := data.RuntimeEvents(ctx, user.ID, "", 100)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %+v err=%v", events, err)
	}
	resumed, err := data.RuntimeEvents(ctx, user.ID, events[0].ID, 100)
	if err != nil || len(resumed) != 1 || resumed[0].ID != events[1].ID {
		t.Fatalf("resumed events = %+v err=%v", resumed, err)
	}
}

func TestRotateRefreshTokenConcurrent(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			store := d.open(t)
			ctx := context.Background()
			user := createTestUser(t, store, 0)

			oldHash := hashToken("old-token-value")
			if _, err := store.CreateRefreshToken(ctx, user.ID, oldHash, nowMillis()+3600000, "127.0.0.1", "test"); err != nil {
				t.Fatal(err)
			}

			var successes, forbidden atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					<-start
					newHash := hashToken(fmt.Sprintf("new-token-%d", idx))
					_, err := store.RotateRefreshToken(ctx, oldHash, newHash, nowMillis()+3600000, "127.0.0.1", "test")
					if err == nil {
						successes.Add(1)
					} else if errors.Is(err, ErrForbidden) {
						forbidden.Add(1)
					}
				}(i)
			}
			close(start)
			wg.Wait()

			if got := successes.Load(); got != 1 {
				t.Errorf("successes = %d, want 1", got)
			}
			if got := forbidden.Load(); got != 1 {
				t.Errorf("forbidden = %d, want 1", got)
			}
		})
	}
}

func TestSetMembershipCreatesEvent(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			store := d.open(t)
			ctx := context.Background()
			admin := createTestUser(t, store, 0)
			target := createTestUser(t, store, 1)

			_, err := store.SetMembership(ctx, admin.ID, target.ID, "REDEEMED", nowMillis()+86400000, "GRANT")
			if err != nil {
				t.Fatal(err)
			}

			var eventCount int
			if err := store.db.GetContext(ctx, &eventCount, store.rebind(`SELECT COUNT(*) FROM membership_events WHERE user_id = ? AND action = 'GRANT'`), target.ID); err != nil {
				t.Fatal(err)
			}
			if eventCount != 1 {
				t.Errorf("membership_events count = %d, want 1", eventCount)
			}
			mship, err := store.Membership(ctx, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mship.Tier != "REDEEMED" {
				t.Errorf("tier = %q, want REDEEMED", mship.Tier)
			}
		})
	}
}

func TestDeleteReleaseWritesAudit(t *testing.T) {
	for _, d := range testDialects(t) {
		t.Run(d.name, func(t *testing.T) {
			store := d.open(t)
			ctx := context.Background()
			admin := createTestUser(t, store, 0)

			release, err := store.CreateRelease(ctx, model.AppRelease{
				ID: "rel-test-1", Platform: model.ReleasePlatformMobile, Channel: model.ReleaseChannelStable,
				VersionCode: 1, VersionName: "1.0.0", Title: "Test",
				ArtifactFileName: "app.apk", ArtifactStorageName: "stored-apk", ArtifactSize: 1024,
				ArtifactSHA256: "abc123", ArtifactMimeType: "application/vnd.android.package-archive",
				CreatedBy: admin.ID,
			})
			if err != nil {
				t.Fatal(err)
			}

			audit := AuditContext{
				ActorID: admin.ID, Action: "RELEASE_DELETE", TargetType: "RELEASE", TargetID: release.ID,
				RequestID: "req-test", IPAddress: "127.0.0.1", UserAgent: "test",
			}
			_, err = store.DeleteRelease(ctx, release.ID, audit)
			if err != nil {
				t.Fatal(err)
			}

			var auditCount int
			if err := store.db.GetContext(ctx, &auditCount, store.rebind(`SELECT COUNT(*) FROM audit_logs WHERE action = 'RELEASE_DELETE' AND target_id = ?`), release.ID); err != nil {
				t.Fatal(err)
			}
			if auditCount != 1 {
				t.Errorf("audit_logs count = %d, want 1", auditCount)
			}

			var releaseCount int
			if err := store.db.GetContext(ctx, &releaseCount, store.rebind(`SELECT COUNT(*) FROM app_releases WHERE id = ?`), release.ID); err != nil {
				t.Fatal(err)
			}
			if releaseCount != 0 {
				t.Errorf("release still exists after delete, count = %d", releaseCount)
			}
		})
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
