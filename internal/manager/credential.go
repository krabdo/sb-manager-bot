package manager

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

const maxCredentialBytes = 16 << 10

var forumIDPattern = regexp.MustCompile(`^[0-9]+$`)

type Credential struct {
	Version     int    `json:"v"`
	ForumUserID string `json:"forum_user_id"`
	Cookie      string `json:"cookie"`
}

func ParseCredential(raw string) (Credential, error) {
	var c Credential
	raw = strings.TrimSpace(raw)
	if len(raw) > maxCredentialBytes {
		return c, errors.New("credential exceeds 16 KiB")
	}
	if !strings.HasPrefix(raw, "SBM1.") {
		return c, errors.New("credential prefix is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, "SBM1."))
	if err != nil {
		return c, errors.New("credential base64url is invalid")
	}
	if len(payload) > maxCredentialBytes {
		return c, errors.New("credential payload exceeds 16 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, errors.New("credential JSON is invalid")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return c, errors.New("credential JSON has trailing data")
	}
	if c.Version != 1 || !forumIDPattern.MatchString(c.ForumUserID) || c.Cookie == "" {
		return c, errors.New("credential fields are invalid")
	}
	if strings.ContainsAny(c.Cookie, "\r\n\x00") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.Cookie)), "cookie:") {
		return c, errors.New("cookie value is invalid")
	}
	return c, nil
}
