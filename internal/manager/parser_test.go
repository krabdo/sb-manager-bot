package manager

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

const pageHead = `<html><body><a class="tab" href="/u/1777/?tab=notifications">通知</a>`

func item(kind, actor, content, target, stamp string) string {
	return `<div class="notification-item"><span class="notification-kind">` + kind + `</span><a class="post-title">` + actor + `</a><div class="notification-content">` + content + `</div><time datetime="` + stamp + `"></time><a class="notification-reply-action" href="` + target + `">go</a></div>`
}
func TestParserReplyMentionUnknownAndPagination(t *testing.T) {
	base, _ := url.Parse("https://sb.sb")
	body := pageHead + item("回复", "甲", "正文", "/t/1/?reply_id=5", "2026-01-01T00:00:00Z") + item("提及", "乙", "内容", "/t/2/?reply_id=6", "2026-01-02T00:00:00Z") + item("新类型", "", "未知", "/t/3/", "2026-01-03T00:00:00Z") + `<a href="/u/1777/page/2/?tab=notifications">next</a></body></html>`
	p, e := ParseNotificationPage(strings.NewReader(body), base, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Notifications) != 3 || !p.HasNext {
		t.Fatalf("page=%#v", p)
	}
	if p.Notifications[0].ID != "reply:/t/1/:5:回复" {
		t.Fatalf("stable ID=%q", p.Notifications[0].ID)
	}
	if !strings.HasPrefix(p.Notifications[2].ID, "hash:") {
		t.Fatal("fallback hash missing")
	}
}
func TestParserAuthAndStructure(t *testing.T) {
	base, _ := url.Parse("https://sb.sb")
	if _, e := ParseNotificationPage(strings.NewReader(`<form action="/login/"></form>`), base, 1); !errors.Is(e, ErrAuthentication) {
		t.Fatalf("auth error=%v", e)
	}
	if _, e := ParseNotificationPage(strings.NewReader(`<html></html>`), base, 1); !errors.Is(e, ErrPageStructure) {
		t.Fatalf("structure error=%v", e)
	}
	bad := pageHead + `<div class="notification-item"><span class="notification-kind">x</span></div></body></html>`
	if _, e := ParseNotificationPage(strings.NewReader(bad), base, 1); !errors.Is(e, ErrPageStructure) {
		t.Fatalf("item structure error=%v", e)
	}
}
