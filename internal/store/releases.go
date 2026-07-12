package store

import (
	"context"
	"strings"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

func validPlatform(value string) bool {
	return value == model.ReleasePlatformMobile || value == model.ReleasePlatformWear
}

func normalizeChannel(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return model.ReleaseChannelStable
	}
	return value
}

func (s *Store) CreateAnnouncement(ctx context.Context, item model.Announcement) (model.Announcement, error) {
	item.Title = strings.TrimSpace(item.Title)
	item.Content = strings.TrimSpace(item.Content)
	item.Platform = strings.ToUpper(strings.TrimSpace(item.Platform))
	if item.Title == "" || item.Content == "" || (item.Platform != "" && !validPlatform(item.Platform)) {
		return model.Announcement{}, ErrInvalid
	}
	if item.ID == "" {
		item.ID = ids.New("ann")
	}
	if item.Active != 0 {
		item.Active = 1
	}
	now := nowMillis()
	if item.PublishAt == 0 {
		item.PublishAt = now
	}
	item.CreatedAt = now
	item.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO announcements (id, title, content, platform, priority, active, publish_at, expires_at, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), item.ID, item.Title, item.Content, item.Platform, item.Priority, item.Active, item.PublishAt, item.ExpiresAt, item.CreatedBy, item.CreatedAt, item.UpdatedAt)
	return item, normalizeDBError(err)
}

func (s *Store) UpdateAnnouncement(ctx context.Context, item model.Announcement) (model.Announcement, error) {
	item.Title = strings.TrimSpace(item.Title)
	item.Content = strings.TrimSpace(item.Content)
	item.Platform = strings.ToUpper(strings.TrimSpace(item.Platform))
	if item.ID == "" || item.Title == "" || item.Content == "" || (item.Platform != "" && !validPlatform(item.Platform)) {
		return model.Announcement{}, ErrInvalid
	}
	if item.Active != 0 {
		item.Active = 1
	}
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE announcements SET title = ?, content = ?, platform = ?, priority = ?, active = ?, publish_at = ?, expires_at = ?, updated_at = ? WHERE id = ?`), item.Title, item.Content, item.Platform, item.Priority, item.Active, item.PublishAt, item.ExpiresAt, nowMillis(), item.ID)
	if err != nil {
		return model.Announcement{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.Announcement{}, ErrNotFound
	}
	return s.Announcement(ctx, item.ID)
}

func (s *Store) Announcement(ctx context.Context, id string) (model.Announcement, error) {
	var item model.Announcement
	err := s.db.GetContext(ctx, &item, s.rebind(`SELECT * FROM announcements WHERE id = ?`), id)
	return item, normalizeDBError(err)
}

func (s *Store) DeleteAnnouncement(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM announcements WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListAnnouncements(ctx context.Context, limit, offset int) ([]model.Announcement, int, error) {
	var total int
	if err := s.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM announcements`); err != nil {
		return nil, 0, err
	}
	items := []model.Announcement{}
	err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM announcements ORDER BY publish_at DESC, created_at DESC LIMIT ? OFFSET ?`), limit, offset)
	return items, total, err
}

func (s *Store) PublicAnnouncements(ctx context.Context, platform string, limit int) ([]model.Announcement, error) {
	platform = strings.ToUpper(strings.TrimSpace(platform))
	if !validPlatform(platform) {
		return nil, ErrInvalid
	}
	now := nowMillis()
	items := []model.Announcement{}
	err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM announcements WHERE active = 1 AND publish_at <= ? AND (expires_at = 0 OR expires_at > ?) AND (platform = '' OR platform = ?) ORDER BY priority DESC, publish_at DESC LIMIT ?`), now, now, platform, limit)
	return items, err
}

func (s *Store) CreateRelease(ctx context.Context, item model.AppRelease) (model.AppRelease, error) {
	item.Platform = strings.ToUpper(strings.TrimSpace(item.Platform))
	item.Channel = normalizeChannel(item.Channel)
	item.VersionName = strings.TrimSpace(item.VersionName)
	item.Title = strings.TrimSpace(item.Title)
	if item.ID == "" || !validPlatform(item.Platform) || item.VersionCode < 1 || item.VersionName == "" || item.Title == "" || item.ArtifactStorageName == "" || item.ArtifactSize < 1 || item.ArtifactSHA256 == "" {
		return model.AppRelease{}, ErrInvalid
	}
	if item.Mandatory != 0 {
		item.Mandatory = 1
	}
	item.Status = model.ReleaseStatusDraft
	now := nowMillis()
	item.CreatedAt = now
	item.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO app_releases (id, platform, channel, version_code, version_name, min_supported_version_code, title, changelog, mandatory, status, artifact_file_name, artifact_storage_name, artifact_size, artifact_sha256, artifact_mime_type, created_by, published_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`), item.ID, item.Platform, item.Channel, item.VersionCode, item.VersionName, item.MinSupportedVersionCode, item.Title, item.Changelog, item.Mandatory, item.Status, item.ArtifactFileName, item.ArtifactStorageName, item.ArtifactSize, item.ArtifactSHA256, item.ArtifactMimeType, item.CreatedBy, item.CreatedAt, item.UpdatedAt)
	return item, normalizeDBError(err)
}

func (s *Store) Release(ctx context.Context, id string) (model.AppRelease, error) {
	var item model.AppRelease
	err := s.db.GetContext(ctx, &item, s.rebind(`SELECT * FROM app_releases WHERE id = ?`), id)
	return item, normalizeDBError(err)
}

func (s *Store) PublishRelease(ctx context.Context, id string) (model.AppRelease, error) {
	now := nowMillis()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE app_releases SET status = ?, published_at = ?, updated_at = ? WHERE id = ?`), model.ReleaseStatusPublished, now, now, id)
	if err != nil {
		return model.AppRelease{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.AppRelease{}, ErrNotFound
	}
	return s.Release(ctx, id)
}

func (s *Store) DeleteRelease(ctx context.Context, id string, audit AuditContext) (model.AppRelease, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return model.AppRelease{}, err
	}
	defer tx.Rollback()
	var item model.AppRelease
	if err := tx.GetContext(ctx, &item, s.rebind(`SELECT * FROM app_releases WHERE id = ?`), id); err != nil {
		return model.AppRelease{}, normalizeDBError(err)
	}
	result, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM app_releases WHERE id = ?`), id)
	if err != nil {
		return model.AppRelease{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.AppRelease{}, ErrNotFound
	}
	if err := s.auditInTx(ctx, tx, audit); err != nil {
		return model.AppRelease{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.AppRelease{}, err
	}
	return item, nil
}

func (s *Store) ListReleases(ctx context.Context, limit, offset int) ([]model.AppRelease, int, error) {
	var total int
	if err := s.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM app_releases`); err != nil {
		return nil, 0, err
	}
	items := []model.AppRelease{}
	err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM app_releases ORDER BY created_at DESC LIMIT ? OFFSET ?`), limit, offset)
	return items, total, err
}

func (s *Store) LatestRelease(ctx context.Context, platform, channel string) (model.AppRelease, error) {
	platform = strings.ToUpper(strings.TrimSpace(platform))
	channel = normalizeChannel(channel)
	if !validPlatform(platform) {
		return model.AppRelease{}, ErrInvalid
	}
	var item model.AppRelease
	err := s.db.GetContext(ctx, &item, s.rebind(`SELECT * FROM app_releases WHERE platform = ? AND channel = ? AND status = ? ORDER BY version_code DESC, published_at DESC LIMIT 1`), platform, channel, model.ReleaseStatusPublished)
	return item, normalizeDBError(err)
}
