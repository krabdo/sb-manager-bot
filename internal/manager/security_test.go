package manager

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func encodeTestCredential(t *testing.T, c Credential) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return "SBM1." + base64.RawURLEncoding.EncodeToString(b)
}

func TestErrorRedaction(t *testing.T) {
	got := redactError(errors.New("Post https://api.telegram.org/bot123456:very_secret-token/sendMessage SBM1.abc_DEF"))
	if strings.Contains(got, "very_secret") || strings.Contains(got, "SBM1.") {
		t.Fatalf("secret leaked: %s", got)
	}
}

func TestParseCredential(t *testing.T) {
	raw := encodeTestCredential(t, Credential{Version: 1, ForumUserID: "1777", Cookie: "sid=secret; theme=dark"})
	c, err := ParseCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.ForumUserID != "1777" || c.Cookie != "sid=secret; theme=dark" {
		t.Fatalf("unexpected credential: %#v", c)
	}
}
func TestParseCredentialRejectsInvalidAndOversized(t *testing.T) {
	tests := []string{"bad", "SBM1.bad", encodeTestCredential(t, Credential{Version: 2, ForumUserID: "1777", Cookie: "x=y"}), encodeTestCredential(t, Credential{Version: 1, ForumUserID: "name", Cookie: "x=y"}), encodeTestCredential(t, Credential{Version: 1, ForumUserID: "1", Cookie: "Cookie: x=y"}), "SBM1." + strings.Repeat("a", maxCredentialBytes+1)}
	for _, raw := range tests {
		if _, err := ParseCredential(raw); err == nil {
			t.Fatalf("accepted invalid credential of length %d", len(raw))
		}
	}
}

func TestCookieCipherRandomAndAuthenticated(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	c, _ := NewCookieCipher(key)
	a, e := c.Encrypt("sid=top-secret", 42, "1777")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := c.Encrypt("sid=top-secret", 42, "1777")
	if bytes.Equal(a, b) {
		t.Fatal("ciphertext must use a random nonce")
	}
	plain, e := c.Decrypt(a, 42, "1777")
	if e != nil || plain != "sid=top-secret" {
		t.Fatalf("decrypt: %q %v", plain, e)
	}
	if _, e = c.Decrypt(a, 43, "1777"); e == nil {
		t.Fatal("wrong Telegram AAD accepted")
	}
	if _, e = c.Decrypt(a, 42, "1778"); e == nil {
		t.Fatal("wrong forum AAD accepted")
	}
	wrong, _ := NewCookieCipher(bytes.Repeat([]byte{8}, 32))
	if _, e = wrong.Decrypt(a, 42, "1777"); e == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestMessageEscapesAndTruncatesUTF8(t *testing.T) {
	n := Notification{Kind: "提及 <x>", Actor: "a&b", Content: strings.Repeat("中文<&", 2000), TargetURL: "https://sb.sb/t/1/?x=1&y=2"}
	got := FormatNotification(n)
	if len(got) > maxMessageBytes {
		t.Fatalf("message is %d bytes", len(got))
	}
	if strings.Contains(got, "<x>") || strings.Contains(got, "a&b") || !strings.Contains(got, "&lt;x&gt;") || !strings.Contains(got, "&amp;") {
		t.Fatalf("HTML was not escaped: %s", got[:100])
	}
	if !strings.HasSuffix(got, "</a>") {
		t.Fatal("link was truncated or malformed")
	}
	if strings.ToValidUTF8(got, "?") != got {
		t.Fatal("invalid UTF-8")
	}
}

func TestMessageWithoutTargetOmitsLink(t *testing.T) {
	n := Notification{Kind: "邀请", Actor: "a&b", Content: strings.Repeat("中文<&", 2000)}
	got := FormatNotification(n)
	if len(got) > maxMessageBytes {
		t.Fatalf("message is %d bytes", len(got))
	}
	if strings.Contains(got, "查看原帖") || strings.Contains(got, `href=""`) {
		t.Fatalf("unexpected target link: %s", got)
	}
	if !strings.Contains(got, "a&amp;b") || strings.ToValidUTF8(got, "?") != got {
		t.Fatal("message was not safely escaped or truncated")
	}
}
