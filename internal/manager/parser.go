package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

func ParseNotificationPage(r io.Reader, baseURL *url.URL, pageNumber int) (NotificationPage, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return NotificationPage{}, fmt.Errorf("parse forum HTML: %w", err)
	}
	if isLoginDocument(doc) {
		return NotificationPage{}, ErrAuthentication
	}
	if !hasNotificationPageMarker(doc) {
		return NotificationPage{}, ErrPageStructure
	}
	nodes := findAll(doc, func(n *html.Node) bool { return hasClass(n, "notification-item") })
	result := NotificationPage{Notifications: make([]Notification, 0, len(nodes))}
	for i, node := range nodes {
		n, e := parseNotification(node, baseURL)
		if e != nil {
			return NotificationPage{}, fmt.Errorf("%w: item %d: %v", ErrPageStructure, i+1, e)
		}
		result.Notifications = append(result.Notifications, n)
	}
	next := "/page/" + strconv.Itoa(pageNumber+1) + "/"
	for _, link := range findAll(doc, func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == "a" }) {
		href := attr(link, "href")
		if strings.Contains(href, next) && strings.Contains(href, "tab=notifications") {
			result.HasNext = true
			break
		}
	}
	return result, nil
}

func parseNotification(node *html.Node, baseURL *url.URL) (Notification, error) {
	kindNode := findFirst(node, func(n *html.Node) bool { return hasClass(n, "notification-kind") })
	actorNode := findFirst(node, func(n *html.Node) bool { return n.Data == "a" && hasClass(n, "post-title") })
	contentNode := findFirst(node, func(n *html.Node) bool { return hasClass(n, "notification-content") })
	timeNode := findFirst(node, func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == "time" })
	targetNode := findFirst(node, func(n *html.Node) bool { return n.Data == "a" && hasClass(n, "notification-reply-action") })
	if targetNode == nil {
		where := findFirst(node, func(n *html.Node) bool { return hasClass(n, "notification-where") })
		if where != nil {
			targetNode = findFirst(where, func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == "a" })
		}
	}
	if kindNode == nil || contentNode == nil || timeNode == nil {
		return Notification{}, errors.New("required kind, content, or time element is missing")
	}
	kind, content, actor := normalizedText(kindNode), normalizedText(contentNode), ""
	if actorNode != nil {
		actor = normalizedText(actorNode)
	}
	if kind == "" || content == "" {
		return Notification{}, errors.New("kind or content is empty")
	}
	created, err := time.Parse(time.RFC3339, attr(timeNode, "datetime"))
	if err != nil {
		return Notification{}, fmt.Errorf("invalid notification time: %w", err)
	}
	target := ""
	if targetNode != nil {
		target, err = resolveForumURL(baseURL, attr(targetNode, "href"))
		if err != nil {
			return Notification{}, fmt.Errorf("invalid notification target: %w", err)
		}
	}
	n := Notification{Kind: kind, Actor: actor, Content: content, TargetURL: target, CreatedAt: created}
	n.ID = notificationID(n)
	return n, nil
}

func notificationID(n Notification) string {
	if parsed, e := url.Parse(n.TargetURL); e == nil {
		if rid := parsed.Query().Get("reply_id"); rid != "" {
			return "reply:" + parsed.Path + ":" + rid + ":" + n.Kind
		}
	}
	canonical := strings.Join([]string{n.Kind, n.Actor, n.CreatedAt.UTC().Format(time.RFC3339Nano), n.TargetURL, n.Content}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return "hash:" + hex.EncodeToString(sum[:])
}
func resolveForumURL(base *url.URL, raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || raw == "" {
		return "", errors.New("empty or malformed URL")
	}
	u = base.ResolveReference(u)
	if u.Scheme != "https" || !strings.EqualFold(u.Host, base.Host) {
		return "", errors.New("target must remain on configured HTTPS forum host")
	}
	return u.String(), nil
}
func isLoginDocument(root *html.Node) bool {
	return findFirst(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "form" && strings.HasPrefix(attr(n, "action"), "/login/")
	}) != nil
}
func hasNotificationPageMarker(root *html.Node) bool {
	return findFirst(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "tab") && strings.Contains(attr(n, "href"), "tab=notifications")
	}) != nil
}
func findFirst(root *html.Node, p func(*html.Node) bool) *html.Node {
	if root == nil {
		return nil
	}
	if p(root) {
		return root
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if f := findFirst(c, p); f != nil {
			return f
		}
	}
	return nil
}
func findAll(root *html.Node, p func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if p(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	return out
}
func hasClass(n *html.Node, name string) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == name {
			return true
		}
	}
	return false
}
func attr(n *html.Node, name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}
func normalizedText(n *html.Node) string {
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(cur *html.Node) {
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
			b.WriteByte(' ')
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
