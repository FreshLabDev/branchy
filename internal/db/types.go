// SPDX-License-Identifier: Apache-2.0
package db

import (
	"encoding/json"
	"time"
)

type ChatState struct {
	ID          int64
	Type        string
	Title       string
	Username    string
	BotStatus   string
	Active      bool
	AddedByUser int64
}

type OAuthState struct {
	State          string
	TelegramUserID int64
	CodeVerifier   string
	RedirectURI    string
}

type GitHubConnection struct {
	TelegramUserID       int64
	GitHubUserID         int64
	GitHubLogin          string
	EncryptedAccessToken []byte
	TokenScope           string
}

type Repository struct {
	GitHubRepoID       int64
	FullName           string
	Owner              string
	Name               string
	Private            bool
	DefaultBranch      string
	HTMLURL            string
	HasAdminPermission bool
}

type Subscription struct {
	ID                 string
	TelegramUserID     int64
	DestinationType    string
	DestinationChatID  int64
	GitHubRepoID       int64
	RepoFullName       string
	Events             []string
	BranchMode         string
	BranchName         string
	BranchNames        []string
	PullRequestActions []string
	ReleaseMode        string
	Status             string
	PauseReason        string
	DefaultBranch      string
	HTMLURL            string
}

type CallbackToken struct {
	Token          string
	TelegramUserID int64
	Action         string
	Payload        json.RawMessage
}

type NotificationJobInsert struct {
	SubscriptionID    string
	DestinationChatID int64
	Text              string
}

type NotificationJob struct {
	ID                string
	DeliveryID        string
	SubscriptionID    string
	DestinationChatID int64
	Text              string
	Attempts          int
	MaxAttempts       int
}

type NotificationJobResult struct {
	Success   bool
	Temporary bool
	RetryAt   time.Time
	Error     string
	// DisableSubscription marks a permanent failure where the destination is
	// unreachable for good (bot blocked/removed, chat gone). The owning
	// subscription is auto-paused so it stops generating dead jobs.
	DisableSubscription bool
}

// JobOutcome is the terminal/retry decision FinishNotificationJob made for a
// send attempt.
type JobOutcome string

const (
	OutcomeSent    JobOutcome = "sent"
	OutcomeRetried JobOutcome = "retried"
	OutcomeFailed  JobOutcome = "failed"
	// OutcomeSkipped means the fenced terminal/retry UPDATE matched no row: the
	// job was no longer 'processing' (a concurrent worker already finalized it,
	// or its lease was reclaimed), so the write was a safe no-op.
	OutcomeSkipped JobOutcome = "skipped"
)

type HealthStatus struct {
	OutboxPending    int64
	OutboxProcessing int64
	OutboxFailed     int64
}
