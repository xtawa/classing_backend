package httpapi

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
	"github.com/xtawa/classing-backend/internal/store"
)

func (s *Server) publicAnnouncements(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	if platform == "" {
		platform = model.ReleasePlatformMobile
	}
	items, err := s.store.PublicAnnouncements(r.Context(), platform, 20)
	if err != nil {
		writeStoreError(w, r, err, "ANNOUNCEMENT")
		return
	}
	payloads := make([]map[string]any, 0, len(items))
	for _, item := range items {
		payloads = append(payloads, announcementPayload(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": payloads})
}

func (s *Server) publicLatestRelease(w http.ResponseWriter, r *http.Request) {
	platform := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("platform")))
	if platform == "" {
		platform = model.ReleasePlatformMobile
	}
	channel := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("channel")))
	if channel == "" {
		channel = model.ReleaseChannelStable
	}
	if platform != model.ReleasePlatformMobile && platform != model.ReleasePlatformWear || channel != model.ReleaseChannelStable && channel != model.ReleaseChannelBeta {
		writeError(w, r, http.StatusBadRequest, "RELEASE_QUERY_INVALID", "platform and channel must be supported values")
		return
	}
	currentVersionCode := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("versionCode")); raw != "" {
		var parseErr error
		currentVersionCode, parseErr = strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || currentVersionCode < 0 {
			writeError(w, r, http.StatusBadRequest, "RELEASE_VERSION_INVALID", "versionCode must be a non-negative integer")
			return
		}
	}
	item, err := s.store.LatestRelease(r.Context(), platform, channel)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"updateAvailable": false, "release": nil})
		return
	}
	if err != nil {
		writeStoreError(w, r, err, "RELEASE")
		return
	}
	etag := `"` + item.ID + `-` + strconv.FormatInt(item.VersionCode, 10) + `"`
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), item.VersionCode) || strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updateAvailable": currentVersionCode < item.VersionCode,
		"forceUpdate":     currentVersionCode < item.VersionCode && (currentVersionCode < item.MinSupportedVersionCode || item.Mandatory != 0),
		"release":         releasePayload(item),
	})
}

func (s *Server) publicDownloadRelease(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Release(r.Context(), r.PathValue("id"))
	if err != nil || item.Status != model.ReleaseStatusPublished {
		writeError(w, r, http.StatusNotFound, "RELEASE_NOT_FOUND", "release was not found")
		return
	}
	file, err := os.Open(filepath.Join(s.cfg.ReleaseStorageDir, item.ArtifactStorageName))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "RELEASE_ARTIFACT_NOT_FOUND", "release artifact is unavailable")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "RELEASE_ARTIFACT_FAILED", "release artifact could not be read")
		return
	}
	if info.Size() != item.ArtifactSize {
		writeError(w, r, http.StatusConflict, "RELEASE_ARTIFACT_MISMATCH", "release artifact does not match recorded metadata")
		return
	}
	if err := verifyReleaseArtifact(s.cfg.ReleaseStorageDir, &item); err != nil {
		writeError(w, r, http.StatusConflict, "RELEASE_ARTIFACT_MISMATCH", err.Error())
		return
	}
	w.Header().Set("Content-Type", item.ArtifactMimeType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.ArtifactFileName}))
	w.Header().Set("ETag", `"`+item.ArtifactSHA256+`"`)
	w.Header().Set("Cache-Control", "public, max-age=3600, immutable")
	http.ServeContent(w, r, item.ArtifactFileName, info.ModTime(), file)
}

func (s *Server) adminListAnnouncements(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.store.ListAnnouncements(r.Context(), limit, offset)
	if err != nil {
		writeStoreError(w, r, err, "ANNOUNCEMENT")
		return
	}
	payloads := make([]map[string]any, 0, len(items))
	for _, item := range items {
		payloads = append(payloads, announcementPayload(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": payloads, "total": total})
}

type announcementRequest struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	Platform  string `json:"platform"`
	Priority  int    `json:"priority"`
	Active    bool   `json:"active"`
	PublishAt int64  `json:"publishAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

func (s *Server) adminCreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	var body announcementRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	item, err := s.store.CreateAnnouncement(r.Context(), model.Announcement{Title: body.Title, Content: body.Content, Platform: body.Platform, Priority: body.Priority, Active: boolInt(body.Active), PublishAt: body.PublishAt, ExpiresAt: body.ExpiresAt, CreatedBy: principal(r).User.ID})
	if err != nil {
		writeStoreError(w, r, err, "ANNOUNCEMENT")
		return
	}
	s.audit(r, principal(r).User.ID, "ANNOUNCEMENT_CREATE", "ANNOUNCEMENT", item.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"announcement": announcementPayload(item)})
}

func (s *Server) adminUpdateAnnouncement(w http.ResponseWriter, r *http.Request) {
	var body announcementRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	item, err := s.store.UpdateAnnouncement(r.Context(), model.Announcement{ID: r.PathValue("id"), Title: body.Title, Content: body.Content, Platform: body.Platform, Priority: body.Priority, Active: boolInt(body.Active), PublishAt: body.PublishAt, ExpiresAt: body.ExpiresAt})
	if err != nil {
		writeStoreError(w, r, err, "ANNOUNCEMENT")
		return
	}
	s.audit(r, principal(r).User.ID, "ANNOUNCEMENT_UPDATE", "ANNOUNCEMENT", item.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"announcement": announcementPayload(item)})
}

func (s *Server) adminDeleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteAnnouncement(r.Context(), id); err != nil {
		writeStoreError(w, r, err, "ANNOUNCEMENT")
		return
	}
	s.audit(r, principal(r).User.ID, "ANNOUNCEMENT_DELETE", "ANNOUNCEMENT", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminListReleases(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.store.ListReleases(r.Context(), limit, offset)
	if err != nil {
		writeStoreError(w, r, err, "RELEASE")
		return
	}
	payloads := make([]map[string]any, 0, len(items))
	for _, item := range items {
		payloads = append(payloads, releasePayload(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": payloads, "total": total})
}

func (s *Server) adminUploadRelease(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ReleaseStorageDir == "" || s.cfg.MaxReleaseArtifactSize < 1 {
		writeError(w, r, http.StatusServiceUnavailable, "RELEASE_STORAGE_DISABLED", "release storage is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxReleaseArtifactSize+2*1024*1024)
	if err := r.ParseMultipartForm(8 * 1024 * 1024); err != nil {
		writeError(w, r, http.StatusBadRequest, "RELEASE_UPLOAD_INVALID", "release upload is invalid or too large")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	upload, header, err := r.FormFile("artifact")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "RELEASE_ARTIFACT_REQUIRED", "APK artifact is required")
		return
	}
	defer upload.Close()
	originalName := filepath.Base(header.Filename)
	if !strings.EqualFold(filepath.Ext(originalName), ".apk") {
		writeError(w, r, http.StatusBadRequest, "RELEASE_ARTIFACT_INVALID", "artifact must be an APK file")
		return
	}
	versionCode, err := strconv.ParseInt(r.FormValue("versionCode"), 10, 64)
	if err != nil || versionCode < 1 {
		writeError(w, r, http.StatusBadRequest, "RELEASE_VERSION_INVALID", "versionCode must be a positive integer")
		return
	}
	minVersionCode, _ := strconv.ParseInt(r.FormValue("minSupportedVersionCode"), 10, 64)
	mandatory, _ := strconv.ParseBool(r.FormValue("mandatory"))
	publish, _ := strconv.ParseBool(r.FormValue("publish"))
	releaseID := ids.New("rel")
	if err := os.MkdirAll(s.cfg.ReleaseStorageDir, 0750); err != nil {
		writeError(w, r, http.StatusInternalServerError, "RELEASE_STORAGE_FAILED", "release storage could not be prepared")
		return
	}
	if available, err := storageAvailableBytes(s.cfg.ReleaseStorageDir); err != nil || available < uint64(s.cfg.MaxReleaseArtifactSize+64*1024*1024) {
		writeError(w, r, http.StatusInsufficientStorage, "RELEASE_STORAGE_CAPACITY", "release storage does not have enough free space")
		return
	}
	storageName := releaseID + ".apk"
	temp, err := os.CreateTemp(s.cfg.ReleaseStorageDir, releaseID+"-*.upload")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "RELEASE_STORAGE_FAILED", "release artifact could not be stored")
		return
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(upload, s.cfg.MaxReleaseArtifactSize+1))
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written < 1 || written > s.cfg.MaxReleaseArtifactSize {
		writeError(w, r, http.StatusBadRequest, "RELEASE_ARTIFACT_INVALID", "artifact is empty, too large, or could not be stored")
		return
	}
	if !validAPK(tempPath) {
		writeError(w, r, http.StatusBadRequest, "RELEASE_ARTIFACT_INVALID", "artifact is not a valid APK archive")
		return
	}
	finalPath := filepath.Join(s.cfg.ReleaseStorageDir, storageName)
	if err := os.Rename(tempPath, finalPath); err != nil {
		writeError(w, r, http.StatusInternalServerError, "RELEASE_STORAGE_FAILED", "release artifact could not be finalized")
		return
	}
	if err := syncStorageDirectory(s.cfg.ReleaseStorageDir); err != nil {
		_ = os.Remove(finalPath)
		writeError(w, r, http.StatusInternalServerError, "RELEASE_STORAGE_FAILED", "release storage metadata could not be synchronized")
		return
	}
	item, err := s.store.CreateRelease(r.Context(), model.AppRelease{
		ID: releaseID, Platform: r.FormValue("platform"), Channel: r.FormValue("channel"), VersionCode: versionCode,
		VersionName: r.FormValue("versionName"), MinSupportedVersionCode: minVersionCode, Title: r.FormValue("title"),
		Changelog: r.FormValue("changelog"), Mandatory: boolInt(mandatory), ArtifactFileName: originalName,
		ArtifactStorageName: storageName, ArtifactSize: written, ArtifactSHA256: hex.EncodeToString(hash.Sum(nil)),
		ArtifactMimeType: "application/vnd.android.package-archive", CreatedBy: principal(r).User.ID,
	})
	if err != nil {
		_ = os.Remove(finalPath)
		writeStoreError(w, r, err, "RELEASE")
		return
	}
	if publish {
		item, err = s.store.PublishRelease(r.Context(), item.ID)
		if err != nil {
			writeStoreError(w, r, err, "RELEASE_PUBLISH")
			return
		}
	}
	s.audit(r, principal(r).User.ID, "RELEASE_UPLOAD", "RELEASE", item.ID, map[string]any{"versionCode": item.VersionCode, "sha256": item.ArtifactSHA256})
	writeJSON(w, http.StatusCreated, map[string]any{"release": releasePayload(item)})
}

func (s *Server) adminPublishRelease(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Release(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, r, err, "RELEASE")
		return
	}
	if err := verifyReleaseArtifact(s.cfg.ReleaseStorageDir, &item); err != nil {
		writeError(w, r, http.StatusConflict, "RELEASE_ARTIFACT_MISMATCH", err.Error())
		return
	}
	published, err := s.store.PublishRelease(r.Context(), item.ID)
	if err != nil {
		writeStoreError(w, r, err, "RELEASE")
		return
	}
	s.audit(r, principal(r).User.ID, "RELEASE_PUBLISH", "RELEASE", published.ID, map[string]any{"versionCode": published.VersionCode})
	writeJSON(w, http.StatusOK, map[string]any{"release": releasePayload(published)})
}

func (s *Server) adminDeleteRelease(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	item, err := s.store.Release(r.Context(), id)
	if err != nil {
		writeStoreError(w, r, err, "RELEASE")
		return
	}
	finalPath := filepath.Join(s.cfg.ReleaseStorageDir, item.ArtifactStorageName)
	quarantinePath := finalPath + ".quarantine-" + id
	if err := os.Rename(finalPath, quarantinePath); err != nil {
		writeError(w, r, http.StatusConflict, "RELEASE_ARTIFACT_QUARANTINE_FAILED", "release artifact could not be isolated for deletion")
		return
	}
	auditCtx := s.auditContext(r, principal(r).User.ID, "RELEASE_DELETE", "RELEASE", id, nil)
	_, err = s.store.DeleteRelease(r.Context(), id, auditCtx)
	if err != nil {
		_ = os.Rename(quarantinePath, finalPath)
		writeStoreError(w, r, err, "RELEASE")
		return
	}
	if err := os.Remove(quarantinePath); err != nil && !os.IsNotExist(err) {
		s.log.Warn("cleanup release artifact", "error", err, "releaseId", item.ID, "storageName", item.ArtifactStorageName)
	}
	w.WriteHeader(http.StatusNoContent)
}

func validAPK(path string) bool {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name == "AndroidManifest.xml" {
			return true
		}
	}
	return false
}

func verifyReleaseArtifact(storageDir string, item *model.AppRelease) error {
	file, err := os.Open(filepath.Join(storageDir, item.ArtifactStorageName))
	if err != nil {
		return errors.New("release artifact is missing on disk")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return errors.New("release artifact could not be read")
	}
	if info.Size() != item.ArtifactSize {
		return errors.New("release artifact size does not match record")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return errors.New("release artifact could not be hashed")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), item.ArtifactSHA256) {
		return errors.New("release artifact checksum does not match record")
	}
	return nil
}

func announcementPayload(item model.Announcement) map[string]any {
	return map[string]any{"announcementId": item.ID, "title": item.Title, "content": item.Content, "platform": item.Platform, "priority": item.Priority, "active": item.Active != 0, "publishAt": item.PublishAt, "expiresAt": item.ExpiresAt, "createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt}
}

func releasePayload(item model.AppRelease) map[string]any {
	return map[string]any{
		"releaseId": item.ID, "platform": item.Platform, "channel": item.Channel, "versionCode": item.VersionCode,
		"versionName": item.VersionName, "minSupportedVersionCode": item.MinSupportedVersionCode, "title": item.Title,
		"changelog": item.Changelog, "mandatory": item.Mandatory != 0, "status": item.Status,
		"artifactFileName": item.ArtifactFileName, "artifactSize": item.ArtifactSize, "sha256": item.ArtifactSHA256,
		"artifactMimeType": item.ArtifactMimeType, "downloadUrl": "/api/v1/client/releases/" + item.ID + "/download",
		"publishedAt": item.PublishedAt, "createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
