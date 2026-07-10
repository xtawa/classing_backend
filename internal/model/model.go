package model

type User struct {
	ID            string `db:"id" json:"userId"`
	Username      string `db:"username" json:"username"`
	Email         string `db:"email" json:"email"`
	PasswordHash  string `db:"password_hash" json:"-"`
	Role          string `db:"role" json:"role"`
	Status        string `db:"status" json:"status"`
	EmailVerified int    `db:"email_verified" json:"emailVerified"`
	AuthEpoch     int64  `db:"auth_epoch" json:"-"`
	CreatedAt     int64  `db:"created_at" json:"createdAt"`
	UpdatedAt     int64  `db:"updated_at" json:"updatedAt"`
}

type Membership struct {
	UserID    string `db:"user_id" json:"-"`
	Tier      string `db:"tier" json:"tier"`
	ExpiresAt int64  `db:"expires_at" json:"expiresAt"`
	UpdatedAt int64  `db:"updated_at" json:"lastCheckedAt"`
	Source    string `db:"source" json:"source,omitempty"`
}

type TimetableProject struct {
	ID            string `db:"id" json:"projectId"`
	OwnerID       string `db:"owner_id" json:"ownerId"`
	Name          string `db:"name" json:"name"`
	Timezone      string `db:"timezone" json:"timezone"`
	SemesterStart string `db:"semester_start" json:"semesterStart"`
	WeekCount     int    `db:"week_count" json:"weekCount"`
	Document      string `db:"document" json:"-"`
	Version       int64  `db:"version" json:"version"`
	CreatedAt     int64  `db:"created_at" json:"createdAt"`
	UpdatedAt     int64  `db:"updated_at" json:"updatedAt"`
}

type BriefingSubscription struct {
	UserID          string `db:"user_id" json:"-"`
	Enabled         int    `db:"enabled" json:"enabled"`
	Channel         string `db:"channel" json:"channel"`
	Time            string `db:"delivery_time" json:"time"`
	Timezone        string `db:"timezone" json:"timezone"`
	LastScheduledAt int64  `db:"last_scheduled_at" json:"lastScheduledAt"`
	UpdatedAt       int64  `db:"updated_at" json:"updatedAt"`
}

type Mailbox struct {
	ID                string `db:"id" json:"mailboxId"`
	Name              string `db:"name" json:"name"`
	SMTPHost          string `db:"smtp_host" json:"smtpHost"`
	SMTPPort          int    `db:"smtp_port" json:"smtpPort"`
	Username          string `db:"username" json:"username"`
	PasswordSecretRef string `db:"password_secret_ref" json:"passwordSecretRef"`
	FromAddress       string `db:"from_address" json:"fromAddress"`
	DailyQuota        int    `db:"daily_quota" json:"dailyQuota"`
	UsedToday         int    `db:"used_today" json:"usedToday"`
	UsageDate         string `db:"usage_date" json:"usageDate"`
	Enabled           int    `db:"enabled" json:"enabled"`
	CreatedAt         int64  `db:"created_at" json:"createdAt"`
	UpdatedAt         int64  `db:"updated_at" json:"updatedAt"`
}

type BriefingJob struct {
	ID                string `db:"id" json:"jobId"`
	UserID            string `db:"user_id" json:"userId"`
	TargetDate        string `db:"target_date" json:"targetDate"`
	Channel           string `db:"channel" json:"channel"`
	Status            string `db:"status" json:"status"`
	ProviderMailboxID string `db:"provider_mailbox_id" json:"providerMailboxId,omitempty"`
	RetryCount        int    `db:"retry_count" json:"retryCount"`
	LastError         string `db:"last_error" json:"lastError,omitempty"`
	ScheduledAt       int64  `db:"scheduled_at" json:"scheduledAt"`
	CreatedAt         int64  `db:"created_at" json:"createdAt"`
	UpdatedAt         int64  `db:"updated_at" json:"updatedAt"`
}

type AuditLog struct {
	ID         string `db:"id" json:"auditId"`
	ActorID    string `db:"actor_id" json:"actorId,omitempty"`
	Action     string `db:"action" json:"action"`
	TargetType string `db:"target_type" json:"targetType"`
	TargetID   string `db:"target_id" json:"targetId,omitempty"`
	RequestID  string `db:"request_id" json:"requestId,omitempty"`
	IPAddress  string `db:"ip_address" json:"ipAddress,omitempty"`
	UserAgent  string `db:"user_agent" json:"userAgent,omitempty"`
	Metadata   string `db:"metadata" json:"metadata,omitempty"`
	CreatedAt  int64  `db:"created_at" json:"createdAt"`
}

const (
	RoleAdmin = "ADMIN"
	RoleUser  = "USER"

	StatusActive   = "ACTIVE"
	StatusDisabled = "DISABLED"
)
