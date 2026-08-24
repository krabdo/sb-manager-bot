package manager

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramToken, TelegramAPIURL string
	EncryptionKey                 []byte
	AdminIDs                      map[int64]struct{}
	DatabaseFile                  string
	PollInterval, ForumTimeout    time.Duration
	ForumRate                     float64
	Workers, MaxUsers             int
	HTTPAddr, ForumBaseURL        string
}

func LoadConfig() (Config, error) {
	c := Config{DatabaseFile: "/data/sb-manager.db", PollInterval: time.Minute, ForumTimeout: 20 * time.Second, ForumRate: 3, Workers: 8, MaxUsers: 500, HTTPAddr: ":8080", ForumBaseURL: "https://sb.sb", AdminIDs: map[int64]struct{}{}}
	c.TelegramToken = strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	c.TelegramAPIURL = strings.TrimSpace(os.Getenv("TELEGRAM_API_URL"))
	if c.TelegramToken == "" {
		return c, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	key, err := decodeKey(os.Getenv("CREDENTIAL_ENCRYPTION_KEY"))
	if err != nil {
		return c, err
	}
	c.EncryptionKey = key
	admins := strings.FieldsFunc(os.Getenv("ADMIN_TELEGRAM_IDS"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	if len(admins) == 0 {
		return c, errors.New("ADMIN_TELEGRAM_IDS is required")
	}
	for _, raw := range admins {
		id, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || id <= 0 {
			return c, fmt.Errorf("invalid ADMIN_TELEGRAM_IDS entry %q", raw)
		}
		c.AdminIDs[id] = struct{}{}
	}
	if v := os.Getenv("DATABASE_FILE"); v != "" {
		c.DatabaseFile = v
	}
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		c.PollInterval, err = time.ParseDuration(v)
		if err != nil || c.PollInterval <= 0 {
			return c, errors.New("POLL_INTERVAL must be a positive duration")
		}
	}
	if v := os.Getenv("FORUM_HTTP_TIMEOUT"); v != "" {
		c.ForumTimeout, err = time.ParseDuration(v)
		if err != nil || c.ForumTimeout <= 0 {
			return c, errors.New("FORUM_HTTP_TIMEOUT must be a positive duration")
		}
	}
	if v := os.Getenv("FORUM_REQUEST_RATE"); v != "" {
		c.ForumRate, err = strconv.ParseFloat(v, 64)
		if err != nil || c.ForumRate <= 0 {
			return c, errors.New("FORUM_REQUEST_RATE must be positive")
		}
	}
	if v := os.Getenv("POLL_WORKERS"); v != "" {
		c.Workers, err = strconv.Atoi(v)
		if err != nil || c.Workers < 1 || c.Workers > 64 {
			return c, errors.New("POLL_WORKERS must be 1..64")
		}
	}
	if v := os.Getenv("MAX_USERS"); v != "" {
		c.MaxUsers, err = strconv.Atoi(v)
		if err != nil || c.MaxUsers < 1 {
			return c, errors.New("MAX_USERS must be positive")
		}
	}
	if v := os.Getenv("HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
	} else if p := os.Getenv("PORT"); p != "" {
		c.HTTPAddr = ":" + p
	}
	if v := os.Getenv("FORUM_BASE_URL"); v != "" {
		c.ForumBaseURL = v
	}
	u, e := url.Parse(c.ForumBaseURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return c, errors.New("FORUM_BASE_URL must be HTTPS")
	}
	return c, nil
}

func decodeKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("CREDENTIAL_ENCRYPTION_KEY is required")
	}
	var key []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		key, err = enc.DecodeString(raw)
		if err == nil {
			break
		}
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("CREDENTIAL_ENCRYPTION_KEY must be base64 encoding of exactly 32 bytes")
	}
	return key, nil
}
