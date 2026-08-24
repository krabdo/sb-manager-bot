package manager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeForum struct {
	mu    sync.Mutex
	pages map[int]NotificationPage
	err   error
	calls int
}

func (f *fakeForum) FetchPage(_ context.Context, _, _ string, page int) (NotificationPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return NotificationPage{}, f.err
	}
	return f.pages[page], nil
}

type fakeUI struct {
	mu                   sync.Mutex
	deleted              bool
	deleteErr, errorSend error
	events, texts, html  []string
}

func (f *fakeUI) SendHTML(_ context.Context, _ int64, s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "send")
	if f.errorSend != nil {
		return f.errorSend
	}
	f.html = append(f.html, s)
	return nil
}
func (f *fakeUI) SendText(_ context.Context, _ int64, s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, s)
	return nil
}
func (f *fakeUI) DeleteMessage(_ context.Context, _ int64, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "delete")
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = true
	return nil
}
func (f *fakeUI) SendUnbindConfirmation(context.Context, int64) error  { return nil }
func (f *fakeUI) AnswerCallback(context.Context, string, string) error { return nil }
func testLogger() *slog.Logger                                         { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func notifications() []Notification {
	return []Notification{{ID: "new", Kind: "回复", Content: "new body", TargetURL: "https://sb.sb/t/1/"}, {ID: "old", Kind: "提及", Content: "old body", TargetURL: "https://sb.sb/t/2/"}}
}

func TestCredentialDeletesBeforeSaveAndFirstBindBaseline(t *testing.T) {
	s := testStore(t)
	cipher, _ := NewCookieCipher(make([]byte, 32))
	forum := &fakeForum{pages: map[int]NotificationPage{1: {Notifications: notifications()}}}
	ui := &fakeUI{}
	c := NewController(s, cipher, forum, ui, map[int64]struct{}{99: {}}, 500, testLogger())
	raw := encodeTestCredential(t, Credential{Version: 1, ForumUserID: "1777", Cookie: "sid=secret"})
	c.HandleMessage(context.Background(), IncomingMessage{TelegramID: 1, ChatID: 1, MessageID: 7, Private: true, Text: raw})
	if !ui.deleted || len(ui.events) == 0 || ui.events[0] != "delete" {
		t.Fatalf("events=%v", ui.events)
	}
	a, e := s.Account(context.Background(), 1)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := cipher.Decrypt(a.CookieCipher, 1, "1777")
	if e != nil || plain != "sid=secret" {
		t.Fatalf("stored cookie=%q err=%v", plain, e)
	}
	if seen, _ := s.IsSeen(context.Background(), 1, "old"); !seen {
		t.Fatal("older notification was not baselined")
	}
	if seen, _ := s.IsSeen(context.Background(), 1, "new"); seen {
		t.Fatal("latest notification must remain pending")
	}
}

func TestCredentialDeleteFailureDoesNotPersist(t *testing.T) {
	s := testStore(t)
	cipher, _ := NewCookieCipher(make([]byte, 32))
	ui := &fakeUI{deleteErr: errors.New("denied")}
	c := NewController(s, cipher, &fakeForum{}, ui, map[int64]struct{}{1: {}}, 500, testLogger())
	c.HandleMessage(context.Background(), IncomingMessage{TelegramID: 1, ChatID: 1, MessageID: 1, Private: true, Text: encodeTestCredential(t, Credential{Version: 1, ForumUserID: "1", Cookie: "sid=x"})})
	if _, e := s.Account(context.Background(), 1); e == nil {
		t.Fatal("credential persisted after delete failure")
	}
}

func TestBindCommandShowsChromeReleaseInstructions(t *testing.T) {
	s := testStore(t)
	cipher, _ := NewCookieCipher(make([]byte, 32))
	ui := &fakeUI{}
	c := NewController(s, cipher, &fakeForum{}, ui, nil, 500, testLogger())
	c.HandleMessage(context.Background(), IncomingMessage{TelegramID: 1, ChatID: 1, Private: true, Text: "/bind"})
	if len(ui.texts) != 1 || !strings.Contains(ui.texts[0], "/releases/download/v0.1.0/sb-manager-bot-chrome.zip") || !strings.Contains(ui.texts[0], "chrome://extensions") || !strings.Contains(ui.texts[0], "6. 点击扩展图标") {
		t.Fatalf("unexpected /bind instructions: %v", ui.texts)
	}
}

func TestPollFirstLatestRestartAndFailureState(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cipher, _ := NewCookieCipher(make([]byte, 32))
	blob, _ := cipher.Encrypt("sid=x", 1, "1777")
	a := testAccount(1, "1777")
	a.CookieCipher = blob
	a.NextPollAt = time.Now()
	if _, e := s.Bind(ctx, a, 10, []string{"old"}); e != nil {
		t.Fatal(e)
	}
	forum := &fakeForum{pages: map[int]NotificationPage{1: {Notifications: notifications()}}}
	ui := &fakeUI{}
	p := NewPoller(s, cipher, forum, ui, nil, time.Minute, 1, testLogger())
	p.jitter = func() time.Duration { return 0 }
	stored, _ := s.Account(ctx, 1)
	p.poll(ctx, stored)
	if len(ui.html) != 1 || !strings.Contains(ui.html[0], "new body") {
		t.Fatalf("messages=%v", ui.html)
	}
	stored, _ = s.Account(ctx, 1)
	p.poll(ctx, stored)
	if len(ui.html) != 1 {
		t.Fatal("restart/re-poll duplicated notification")
	}

	// A failed delivery for a new notification must not update seen.
	forum.pages[1] = NotificationPage{Notifications: []Notification{{ID: "newer", Kind: "回复", Content: "fail", TargetURL: "https://sb.sb/t/3/"}, {ID: "new", Kind: "回复", Content: "new body", TargetURL: "https://sb.sb/t/1/"}}}
	ui.errorSend = errors.New("telegram down")
	stored, _ = s.Account(ctx, 1)
	p.poll(ctx, stored)
	if seen, _ := s.IsSeen(ctx, 1, "newer"); seen {
		t.Fatal("failed delivery was marked seen")
	}
}

func TestPollCrossPageOldToNewAuthAndCircuitBreaker(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	cipher, _ := NewCookieCipher(make([]byte, 32))
	blob, _ := cipher.Encrypt("sid=x", 2, "2")
	a := testAccount(2, "2")
	a.CookieCipher = blob
	a.NextPollAt = time.Now()
	if _, e := s.Bind(ctx, a, 10, []string{"known"}); e != nil {
		t.Fatal(e)
	}
	forum := &fakeForum{pages: map[int]NotificationPage{1: {Notifications: []Notification{{ID: "n3", Kind: "x", Content: "three", TargetURL: "https://sb.sb/3"}, {ID: "n2", Kind: "x", Content: "two", TargetURL: "https://sb.sb/2"}}, HasNext: true}, 2: {Notifications: []Notification{{ID: "n1", Kind: "x", Content: "one", TargetURL: "https://sb.sb/1"}, {ID: "known", Kind: "x", Content: "known", TargetURL: "https://sb.sb/0"}}}}}
	ui := &fakeUI{}
	p := NewPoller(s, cipher, forum, ui, map[int64]struct{}{99: {}}, time.Minute, 1, testLogger())
	p.jitter = func() time.Duration { return 0 }
	stored, _ := s.Account(ctx, 2)
	p.poll(ctx, stored)
	if len(ui.html) != 3 || !strings.Contains(ui.html[0], "one") || !strings.Contains(ui.html[2], "three") {
		t.Fatalf("delivery order=%v", ui.html)
	}
	forum.err = ErrAuthentication
	stored, _ = s.Account(ctx, 2)
	p.poll(ctx, stored)
	stored, _ = s.Account(ctx, 2)
	if stored.Status != StatusRebindRequired {
		t.Fatalf("status=%s", stored.Status)
	}
	alerts := len(ui.texts)
	p.poll(ctx, stored)
	if len(ui.texts) != alerts {
		t.Fatal("authentication warning repeated")
	}
	// Restore active status solely to exercise the global structure breaker.
	_ = s.SetStatus(ctx, 2, StatusActive)
	forum.err = ErrPageStructure
	stored, _ = s.Account(ctx, 2)
	p.poll(ctx, stored)
	paused, _ := s.GlobalPaused(ctx)
	if !paused {
		t.Fatal("structure change did not pause globally")
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Now()
	if got := parseRetryAfter("120", now); got != 120*time.Second {
		t.Fatalf("seconds=%v", got)
	}
	if got := parseRetryAfter(now.Add(time.Minute).UTC().Format(http.TimeFormat), now); got < 59*time.Second || got > time.Minute {
		t.Fatalf("date=%v", got)
	}
}

func TestRequestGateRateAndCancellation(t *testing.T) {
	gate := NewRequestGate(3)
	ctx := context.Background()
	for range 3 {
		if err := gate.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if err := gate.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("fourth request was not globally rate limited")
	}
	gate.Cooldown(time.Minute)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}
