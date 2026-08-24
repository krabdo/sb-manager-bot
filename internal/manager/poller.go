package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

type Messenger interface {
	SendHTML(context.Context, int64, string) error
	SendText(context.Context, int64, string) error
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)/bot[0-9]+:[A-Za-z0-9_-]+`),
	regexp.MustCompile(`SBM1\.[A-Za-z0-9_-]+`),
}

type Poller struct {
	store     *Store
	cipher    *CookieCipher
	forum     Forum
	messenger Messenger
	admins    map[int64]struct{}
	interval  time.Duration
	workers   int
	log       *slog.Logger
	running   atomic.Bool
	wg        sync.WaitGroup
	inMu      sync.Mutex
	inFlight  map[int64]bool
	jobs      chan Account
	jitter    func() time.Duration
}

func NewPoller(s *Store, c *CookieCipher, f Forum, m Messenger, admins map[int64]struct{}, interval time.Duration, workers int, log *slog.Logger) *Poller {
	return &Poller{store: s, cipher: c, forum: f, messenger: m, admins: admins, interval: interval, workers: workers, log: log, inFlight: map[int64]bool{}, jobs: make(chan Account, workers*2), jitter: func() time.Duration { return time.Duration(rand.IntN(16)) * time.Second }}
}
func (p *Poller) Running() bool { return p.running.Load() }
func (p *Poller) Run(ctx context.Context) {
	p.running.Store(true)
	defer p.running.Store(false)
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case a := <-p.jobs:
					p.poll(ctx, a)
					p.inMu.Lock()
					delete(p.inFlight, a.TelegramID)
					p.inMu.Unlock()
				}
			}
		}()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	p.enqueue(ctx)
	for {
		select {
		case <-ctx.Done():
			p.wg.Wait()
			return
		case <-ticker.C:
			p.enqueue(ctx)
		}
	}
}
func (p *Poller) enqueue(ctx context.Context) {
	paused, e := p.store.GlobalPaused(ctx)
	if e != nil || paused {
		return
	}
	due, e := p.store.Due(ctx, time.Now(), p.workers*2)
	if e != nil {
		p.log.Error("load due accounts", "error", e)
		return
	}
	for _, a := range due {
		p.inMu.Lock()
		if p.inFlight[a.TelegramID] {
			p.inMu.Unlock()
			continue
		}
		p.inFlight[a.TelegramID] = true
		p.inMu.Unlock()
		select {
		case p.jobs <- a:
		case <-ctx.Done():
			return
		default:
			p.inMu.Lock()
			delete(p.inFlight, a.TelegramID)
			p.inMu.Unlock()
			return
		}
	}
}
func (p *Poller) poll(ctx context.Context, a Account) {
	cookie, e := p.cipher.Decrypt(a.CookieCipher, a.TelegramID, a.ForumUserID)
	if e != nil {
		p.log.Error("decrypt account credential", "telegram_id", a.TelegramID, "error", e)
		return
	}
	var fresh []Notification
	known := false
	for page := 1; page <= 10; page++ {
		result, e := p.forum.FetchPage(ctx, a.ForumUserID, cookie, page)
		if e != nil {
			p.handleError(ctx, a, e)
			return
		}
		for _, n := range result.Notifications {
			seen, se := p.store.IsSeen(ctx, a.TelegramID, n.ID)
			if se != nil {
				p.handleError(ctx, a, se)
				return
			}
			if seen {
				known = true
				break
			}
			fresh = append(fresh, n)
		}
		if known || !result.HasNext {
			break
		}
	}
	for i := len(fresh) - 1; i >= 0; i-- {
		n := fresh[i]
		if e := p.messenger.SendHTML(ctx, a.ChatID, FormatNotification(n)); e != nil {
			p.handleError(ctx, a, fmt.Errorf("telegram delivery failed: %w", e))
			return
		}
		if e := p.store.AddSeen(ctx, a.TelegramID, n.ID); e != nil {
			p.log.Error("persist notification state", "telegram_id", a.TelegramID, "error", e)
			return
		}
	}
	now := time.Now()
	if e := p.store.Schedule(ctx, a.TelegramID, now.Add(p.interval+p.jitter()), 0, &now, ""); e != nil {
		p.log.Error("schedule account", "telegram_id", a.TelegramID, "error", e)
	}
}
func (p *Poller) handleError(ctx context.Context, a Account, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, ErrAuthentication) {
		first, e := p.store.MarkRebind(ctx, a.TelegramID)
		if e != nil {
			p.log.Error("mark account for rebind", "telegram_id", a.TelegramID, "error", e)
			return
		}
		if first {
			_ = p.messenger.SendText(ctx, a.ChatID, "⚠️ sb.sb 登录凭据已失效，请使用 /bind 重新绑定。")
		}
		return
	}
	if errors.Is(err, ErrPageStructure) {
		activated, e := p.store.ActivateGlobalPause(ctx)
		if e != nil {
			p.log.Error("activate global circuit breaker", "error", e)
		}
		if activated {
			for id := range p.admins {
				_ = p.messenger.SendText(ctx, id, "⚠️ sb.sb 通知页结构发生变化，已自动暂停全局轮询。请检查服务后使用 /admin_resume 恢复。")
			}
			p.log.Error("forum page structure changed; global polling paused")
		}
		return
	}
	failures := a.FailureCount + 1
	backoffs := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute}
	idx := failures - 1
	if idx >= len(backoffs) {
		idx = len(backoffs) - 1
	}
	delay := backoffs[idx]
	var rateErr *RateLimitError
	if errors.As(err, &rateErr) && rateErr.RetryAfter > delay {
		delay = rateErr.RetryAfter
	}
	if e := p.store.Schedule(ctx, a.TelegramID, time.Now().Add(delay), failures, nil, "transient"); e != nil {
		p.log.Error("schedule retry", "telegram_id", a.TelegramID, "error", e)
	}
	p.log.Warn("account poll failed", "telegram_id", a.TelegramID, "failure_count", failures, "error", redactError(err))
}
func redactError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	for _, pattern := range secretPatterns {
		s = pattern.ReplaceAllString(s, "[REDACTED]")
	}
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}

func BaselineFirstPage(ctx context.Context, s *Store, telegramID int64, page NotificationPage) error {
	if len(page.Notifications) <= 1 {
		return nil
	}
	for _, n := range page.Notifications[1:] {
		if e := s.AddSeen(ctx, telegramID, n.ID); e != nil {
			return e
		}
	}
	return nil
}
