package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const maxForumBody = 4 << 20

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "forum rate limited requests" }

type RequestGate struct {
	limiter  *rate.Limiter
	mu       sync.Mutex
	cooldown time.Time
	now      func() time.Time
}

func NewRequestGate(r float64) *RequestGate {
	return &RequestGate{limiter: rate.NewLimiter(rate.Limit(r), max(1, int(r))), now: time.Now}
}
func (g *RequestGate) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		d := time.Until(g.cooldown)
		g.mu.Unlock()
		if d > 0 {
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		if err := g.limiter.Wait(ctx); err != nil {
			return err
		}
		return nil
	}
}
func (g *RequestGate) Cooldown(d time.Duration) {
	if d <= 0 {
		return
	}
	g.mu.Lock()
	until := g.now().Add(d)
	if until.After(g.cooldown) {
		g.cooldown = until
	}
	g.mu.Unlock()
}

type Forum interface {
	FetchPage(context.Context, string, string, int) (NotificationPage, error)
}
type ForumClient struct {
	baseURL *url.URL
	version string
	client  *http.Client
	gate    *RequestGate
}

func NewForumClient(raw string, timeout time.Duration, version string, gate *RequestGate) (*ForumClient, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("forum base URL must be HTTPS")
	}
	client := &http.Client{Timeout: timeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many forum redirects")
		}
		if req.URL.Scheme != u.Scheme || !strings.EqualFold(req.URL.Host, u.Host) {
			return errors.New("forum redirect left configured host")
		}
		return nil
	}
	return &ForumClient{baseURL: u, version: version, client: client, gate: gate}, nil
}
func (c *ForumClient) FetchPage(ctx context.Context, userID, cookie string, page int) (NotificationPage, error) {
	if page < 1 {
		return NotificationPage{}, errors.New("page must be positive")
	}
	if err := c.gate.Wait(ctx); err != nil {
		return NotificationPage{}, err
	}
	path := "/u/" + url.PathEscape(userID) + "/"
	if page > 1 {
		path += "page/" + strconv.Itoa(page) + "/"
	}
	u := c.baseURL.ResolveReference(&url.URL{Path: path, RawQuery: "tab=notifications"})
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return NotificationPage{}, e
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.7")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; sb-manager-bot/"+c.version+"; +https://github.com/krabdo/sb-manager-bot)")
	resp, e := c.client.Do(req)
	if e != nil {
		return NotificationPage{}, fmt.Errorf("fetch forum page %d: %w", page, e)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || strings.HasPrefix(resp.Request.URL.Path, "/login/") {
		return NotificationPage{}, ErrAuthentication
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		d := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if d <= 0 {
			d = time.Minute
		}
		c.gate.Cooldown(d)
		return NotificationPage{}, &RateLimitError{RetryAfter: d}
	}
	if resp.StatusCode != http.StatusOK {
		return NotificationPage{}, fmt.Errorf("forum returned HTTP %d", resp.StatusCode)
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, maxForumBody+1))
	if e != nil {
		return NotificationPage{}, fmt.Errorf("read forum response: %w", e)
	}
	if len(body) > maxForumBody {
		return NotificationPage{}, errors.New("forum response is too large")
	}
	return ParseNotificationPage(strings.NewReader(string(body)), c.baseURL, page)
}
func parseRetryAfter(raw string, now time.Time) time.Duration {
	if seconds, e := strconv.Atoi(strings.TrimSpace(raw)); e == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, e := http.ParseTime(raw); e == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
