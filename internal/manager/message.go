package manager

import (
	"html"
	"strings"
)

const maxMessageBytes = 3900

func FormatNotification(n Notification) string {
	header := "<b>🔔 " + escapeWithinBytes(n.Kind, 256) + "</b>"
	if n.Actor != "" {
		header += " · " + escapeWithinBytes(n.Actor, 256)
	}
	footer := ""
	if n.TargetURL != "" {
		footer = "\n\n<a href=\"" + escapeWithinBytes(n.TargetURL, 1200) + "\">查看原帖</a>"
	}
	budget := maxMessageBytes - len(header) - len(footer) - 2
	return header + "\n\n" + escapeWithinBytes(n.Content, budget) + footer
}
func escapeWithinBytes(v string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	cut := false
	for _, r := range v {
		e := html.EscapeString(string(r))
		if used+len(e) > maxBytes-len("…") {
			cut = true
			break
		}
		b.WriteString(e)
		used += len(e)
	}
	if cut && used+len("…") <= maxBytes {
		b.WriteRune('…')
	}
	return b.String()
}
