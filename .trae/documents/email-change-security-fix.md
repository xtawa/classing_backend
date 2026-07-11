# Fix: Email Change Account-Takeover Vulnerability

## Context

`PATCH /api/v1/account/me` (`updateAccount` handler) accepts a new `email` with no current-password requirement. The store method `UpdateProfile` writes the new email directly to the `users` table without resetting `email_verified`, bumping `auth_epoch`, or revoking sessions.

**Attack chain:** stolen access token (15min TTL) → change email to attacker's address → password reset (reset email goes to attacker) → set new password → full account takeover.

## Approach

Implement a two-step verified email change flow mirroring the existing registration email verification pattern. Username updates remain passwordless; only email changes require re-authentication.

## Files to Modify

### 1. `internal/store/migrations.go` — new table

Append migration for `email_change_requests` table (schema mirrors `email_verification_challenges` + `new_email` column) and an index on `(user_id, created_at)`.

### 2. `internal/store/users.go` — store methods

- **`UpdateUsername(ctx, id, username)`** — username-only UPDATE (replaces `UpdateProfile` for the account handler; `UpdateProfile` becomes unused and is removed)
- **`CreateEmailChangeRequest(ctx, userID, newEmail, codeHash, expiresAt, ip, ua)`** — mirrors `CreateEmailVerificationChallenge`: 60s rate limit, invalidates previous unused requests, all in one transaction
- **`ConsumeEmailChangeRequest(ctx, requestID, codeHash)`** — mirrors `ConsumeResetToken`: atomic consume (`WHERE used_at = 0` + RowsAffected), then `UPDATE users SET email = new_email, email_verified = 1, auth_epoch = now` + revoke all refresh tokens, all in one transaction. Unique-constraint violation on email → `ErrConflict` (race-condition guard)
- **`PendingEmailChange(ctx, userID)`** — returns active pending request's `new_email` + `expires_at`, or `ErrNotFound`

### 3. `internal/store/briefings.go` — queue methods

- **`QueueEmailChangeVerificationJob(ctx, userID, code, expiresAt, newEmail)`** — channel `EMAIL_CHANGE_VERIFY`, payload includes `toEmail` override
- **`QueueEmailChangeNotifyJob(ctx, userID, newEmail, oldEmail)`** — channel `EMAIL_CHANGE_NOTIFY`, payload includes `toEmail` override

### 4. `internal/worker/worker.go` — email delivery

- **`send()`**: parse payload for `toEmail` field; use it as SMTP recipient instead of `job.Email` when present. Existing channels don't have `toEmail` → no regression
- **`mailContent()`**: add `EMAIL_CHANGE_VERIFY` case (verification code to new email) and `EMAIL_CHANGE_NOTIFY` case (change notice to old email)

### 5. `internal/httpapi/handlers_account.go` — handler logic

- **`updateAccount`** (rewrite):
  - Validate username + email format
  - If email unchanged → `UpdateUsername` only, return 200 (as before)
  - If email changed → require `currentPassword`, verify via `auth.VerifyPassword` (403 if wrong), check new email not in use (409 if conflict), `UpdateUsername` (for username), `CreateEmailChangeRequest` (60s rate limit → 429), queue verification email to new address + notification to old address, audit `ACCOUNT_EMAIL_CHANGE_REQUEST`, return 202 with challenge info + `devVerificationCode` in test mode
- **`confirmEmailChange`** (new): `ConsumeEmailChangeRequest` → `refreshReplays.invalidateUser` → audit `ACCOUNT_EMAIL_CHANGE_CONFIRM` → return 200 with updated account + `sessionsRevoked: true`. Handle `ErrConflict` → 409 (duplicate email at confirm time)
- **`accountMe`** (modify): include `pendingEmailChange` in response if active request exists
- Add `time` import

### 6. `internal/httpapi/server.go` — routing

Add: `mux.Handle("POST /api/v1/account/email/confirm", s.requireAuth(http.HandlerFunc(s.confirmEmailChange)))`

### 7. `internal/httpapi/server_test.go` — tests

New test `TestAccountEmailChangeSecurity` covering:

| Case | Scenario | Assertions |
|---|---|---|
| Stolen session | Change email without `currentPassword` | 403 `ACCOUNT_PASSWORD_CURRENT_INVALID` |
| Stolen session | Change email with wrong password | 403 `ACCOUNT_PASSWORD_CURRENT_INVALID` |
| Full flow | Change email with correct password → confirm | 202 → 200, email updated, sessions revoked |
| Session revocation | Old access token after confirm | 401 (auth_epoch bumped) |
| Session revocation | Old refresh token after confirm | 401 (revoked) |
| Old email for login | Login with old email before confirm | 200 OK |
| Old email for reset | Password reset with new email before confirm | No reset job queued (email not in DB) |
| Old email for reset | Password reset with old email before confirm | Reset job queued + token returned |
| Duplicate email | Change to another user's email | 409 `ACCOUNT_EMAIL_CONFLICT` |
| Code expiry | Expire the challenge manually, then confirm | 400 `ACCOUNT_EMAIL_VERIFICATION_INVALID` |
| Code reuse | Confirm twice with same request | 400 (already used) |

## Key Patterns Reused

- `numericVerificationCode()` from `handlers_auth.go:131` — 6-digit crypto/rand code
- `auth.HashOpaqueToken(code)` — SHA-256 hash for code storage
- `auth.VerifyPassword(hash, password)` — bcrypt compare
- `s.refreshReplays.invalidateUser(userID)` — clear replay cache
- `s.audit(r, actorID, action, targetType, targetID, metadata)` — audit logging
- `writeStoreError` / `writeError` / `decodeJSON` / `clientIP` — HTTP helpers
- `ExposeVerificationCode` config flag — dev/test code exposure

## Verification

```bash
go test ./internal/httpapi/ -run TestAccountEmailChangeSecurity -v
go test ./internal/store/ -v
go build ./...
```
