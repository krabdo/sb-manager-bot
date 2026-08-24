package manager

import (
	"errors"
	"time"
)

var (
	ErrAuthentication = errors.New("forum authentication failed")
	ErrPageStructure  = errors.New("forum notification page structure changed")
	ErrCapacity       = errors.New("user capacity reached")
	ErrBanned         = errors.New("user is banned")
)

const (
	StatusActive         = "active"
	StatusPaused         = "paused"
	StatusRebindRequired = "rebind_required"
)

type Notification struct {
	ID, Kind, Actor, Content, TargetURL string
	CreatedAt                           time.Time
}

type NotificationPage struct {
	Notifications []Notification
	HasNext       bool
}

type Account struct {
	TelegramID, ChatID int64
	ForumUserID        string
	CookieCipher       []byte
	Status             string
	NextPollAt         time.Time
	FailureCount       int
	LastSuccessAt      *time.Time
	LastErrorCode      string
	AuthAlerted        bool
}

type Stats struct {
	Active, Paused, RebindRequired, Banned, Backlog int
}
