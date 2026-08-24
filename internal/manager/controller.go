package manager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

type BotUI interface {
	Messenger
	DeleteMessage(context.Context, int64, int) error
	SendUnbindConfirmation(context.Context, int64) error
	AnswerCallback(context.Context, string, string) error
}

type IncomingMessage struct {
	TelegramID, ChatID int64
	MessageID          int
	Private            bool
	Text               string
}
type IncomingCallback struct {
	TelegramID, ChatID int64
	Private            bool
	ID, Data           string
}

type limitEntry struct {
	window time.Time
	count  int
}
type Controller struct {
	store                 *Store
	cipher                *CookieCipher
	forum                 Forum
	ui                    BotUI
	admins                map[int64]struct{}
	maxUsers              int
	log                   *slog.Logger
	mu                    sync.Mutex
	userLocks             [64]sync.Mutex
	commands, credentials map[int64]limitEntry
}

func NewController(s *Store, c *CookieCipher, f Forum, ui BotUI, admins map[int64]struct{}, maxUsers int, log *slog.Logger) *Controller {
	return &Controller{store: s, cipher: c, forum: f, ui: ui, admins: admins, maxUsers: maxUsers, log: log, commands: map[int64]limitEntry{}, credentials: map[int64]limitEntry{}}
}
func (c *Controller) isAdmin(id int64) bool { _, ok := c.admins[id]; return ok }
func (c *Controller) allow(entries map[int64]limitEntry, id int64, window time.Duration, max int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	e := entries[id]
	if now.Sub(e.window) >= window {
		e = limitEntry{window: now}
	}
	if e.count >= max {
		return false
	}
	e.count++
	entries[id] = e
	return true
}

func (c *Controller) HandleMessage(ctx context.Context, m IncomingMessage) {
	if !m.Private || m.TelegramID <= 0 {
		return
	}
	userLock := &c.userLocks[uint64(m.TelegramID)%uint64(len(c.userLocks))]
	userLock.Lock()
	defer userLock.Unlock()
	text := strings.TrimSpace(m.Text)
	if strings.HasPrefix(text, "SBM1.") {
		c.handleCredential(ctx, m, text)
		return
	}
	if !c.allow(c.commands, m.TelegramID, time.Minute, 10) {
		_ = c.ui.SendText(ctx, m.ChatID, "请求过于频繁，请稍后再试。")
		return
	}
	banned, e := c.store.IsBanned(ctx, m.TelegramID)
	if e != nil {
		c.log.Error("check ban", "telegram_id", m.TelegramID, "error", e)
		return
	}
	if banned {
		_ = c.ui.SendText(ctx, m.ChatID, "此账户已被管理员禁用。")
		return
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	cmd := strings.Split(fields[0], "@")[0]
	switch cmd {
	case "/start", "/help":
		_ = c.ui.SendText(ctx, m.ChatID, "欢迎使用 sb-manager-bot。它会保存经 AES-256-GCM 加密的 sb.sb Cookie，并把通知发回此私聊。\n\n使用 /bind 查看绑定步骤，/status 查看状态，/pause 暂停，/resume 恢复，/unbind 删除全部绑定数据。请勿把凭据发给其他人。")
	case "/bind":
		_ = c.ui.SendText(ctx, m.ChatID, "1. 安装 Tampermonkey 脚本：\nhttps://raw.githubusercontent.com/krabdo/sb-manager-bot/main/userscript/sb-manager-credentials.user.js\n2. 打开你自己的 sb.sb 通知页。\n3. 点击“复制 Bot 凭据”，立即粘贴到此私聊。\n\n凭据含登录 Cookie；Bot 会先删除消息，删除失败时不会保存。")
	case "/status":
		c.status(ctx, m.ChatID, m.TelegramID)
	case "/pause":
		c.setStatus(ctx, m.ChatID, m.TelegramID, StatusPaused, "已暂停论坛轮询。")
	case "/resume":
		c.setStatus(ctx, m.ChatID, m.TelegramID, StatusActive, "已恢复论坛轮询。")
	case "/unbind":
		if e := c.ui.SendUnbindConfirmation(ctx, m.ChatID); e != nil {
			c.log.Warn("send unbind confirmation", "telegram_id", m.TelegramID, "error", e)
		}
	case "/admin_stats", "/admin_pause", "/admin_resume", "/admin_ban", "/admin_unban":
		c.admin(ctx, m, cmd, fields)
	default:
		_ = c.ui.SendText(ctx, m.ChatID, "未知命令。使用 /help 查看帮助。")
	}
}

func (c *Controller) handleCredential(ctx context.Context, m IncomingMessage, text string) {
	if e := c.ui.DeleteMessage(ctx, m.ChatID, m.MessageID); e != nil {
		c.log.Warn("credential message deletion failed", "telegram_id", m.TelegramID, "error", e)
		_ = c.ui.SendText(ctx, m.ChatID, "无法删除凭据消息，因此已拒绝保存。请确认 Bot 有删除消息权限后重试。")
		return
	}
	if !c.allow(c.credentials, m.TelegramID, 10*time.Minute, 3) {
		_ = c.ui.SendText(ctx, m.ChatID, "绑定尝试过于频繁，请 10 分钟后再试。")
		return
	}
	banned, e := c.store.IsBanned(ctx, m.TelegramID)
	if e != nil || banned {
		_ = c.ui.SendText(ctx, m.ChatID, "此账户无法绑定。")
		return
	}
	cred, e := ParseCredential(text)
	if e != nil {
		_ = c.ui.SendText(ctx, m.ChatID, "凭据格式无效，请重新从通知页生成。")
		return
	}
	page, e := c.forum.FetchPage(ctx, cred.ForumUserID, cred.Cookie, 1)
	if e != nil {
		if errors.Is(e, ErrAuthentication) {
			_ = c.ui.SendText(ctx, m.ChatID, "论坛登录验证失败，请重新获取完整 Cookie。")
		} else {
			_ = c.ui.SendText(ctx, m.ChatID, "暂时无法验证论坛凭据，请稍后重试。")
		}
		return
	}
	blob, e := c.cipher.Encrypt(cred.Cookie, m.TelegramID, cred.ForumUserID)
	if e != nil {
		c.log.Error("encrypt credential", "telegram_id", m.TelegramID, "error", e)
		_ = c.ui.SendText(ctx, m.ChatID, "服务器无法安全保存凭据，请联系管理员。")
		return
	}
	var baseline []string
	old, e := c.store.Account(ctx, m.TelegramID)
	isNewOrChanged := errors.Is(e, sql.ErrNoRows) || (e == nil && old.ForumUserID != cred.ForumUserID)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		c.log.Error("load account before bind", "telegram_id", m.TelegramID, "error", e)
		return
	}
	if isNewOrChanged && len(page.Notifications) > 1 {
		for _, n := range page.Notifications[1:] {
			baseline = append(baseline, n.ID)
		}
	}
	_, e = c.store.Bind(ctx, Account{TelegramID: m.TelegramID, ChatID: m.ChatID, ForumUserID: cred.ForumUserID, CookieCipher: blob, NextPollAt: time.Now()}, c.maxUsers, baseline)
	if errors.Is(e, ErrCapacity) {
		_ = c.ui.SendText(ctx, m.ChatID, "服务已达到用户上限，暂时无法新增绑定。")
		return
	}
	if e != nil {
		c.log.Error("save binding", "telegram_id", m.TelegramID, "error", e)
		_ = c.ui.SendText(ctx, m.ChatID, "保存绑定失败，请稍后重试。")
		return
	}
	_ = c.ui.SendText(ctx, m.ChatID, "绑定成功。首次绑定只会转发当前最新一条通知，之后自动去重。建议现在清除剪贴板历史。")
}

func (c *Controller) status(ctx context.Context, chat, id int64) {
	a, e := c.store.Account(ctx, id)
	if errors.Is(e, sql.ErrNoRows) {
		_ = c.ui.SendText(ctx, chat, "尚未绑定。使用 /bind 开始。")
		return
	}
	if e != nil {
		return
	}
	last := "从未"
	if a.LastSuccessAt != nil {
		last = a.LastSuccessAt.Local().Format("2006-01-02 15:04:05 MST")
	}
	_ = c.ui.SendText(ctx, chat, fmt.Sprintf("论坛 UID：%s\n状态：%s\n上次成功：%s", a.ForumUserID, a.Status, last))
}
func (c *Controller) setStatus(ctx context.Context, chat, id int64, status, ok string) {
	a, e := c.store.Account(ctx, id)
	if errors.Is(e, sql.ErrNoRows) {
		_ = c.ui.SendText(ctx, chat, "尚未绑定。使用 /bind 开始。")
		return
	}
	if e != nil {
		c.log.Error("load account status", "telegram_id", id, "error", e)
		return
	}
	if a.Status == StatusRebindRequired {
		_ = c.ui.SendText(ctx, chat, "论坛凭据已失效，请使用 /bind 重新绑定；不能暂停或恢复旧凭据。")
		return
	}
	if e := c.store.SetStatus(ctx, id, status); errors.Is(e, sql.ErrNoRows) {
		_ = c.ui.SendText(ctx, chat, "尚未绑定。使用 /bind 开始。")
	} else if e != nil {
		c.log.Error("change account status", "telegram_id", id, "error", e)
	} else {
		if status == StatusActive {
			_ = c.store.Schedule(ctx, id, time.Now(), 0, nil, "")
		}
		_ = c.ui.SendText(ctx, chat, ok)
	}
}

func (c *Controller) admin(ctx context.Context, m IncomingMessage, cmd string, fields []string) {
	if !c.isAdmin(m.TelegramID) {
		_ = c.ui.SendText(ctx, m.ChatID, "无权执行管理员命令。")
		return
	}
	switch cmd {
	case "/admin_stats":
		st, e := c.store.Stats(ctx, time.Now())
		if e == nil {
			_ = c.ui.SendText(ctx, m.ChatID, fmt.Sprintf("活跃：%d\n暂停：%d\n待重新绑定：%d\n封禁：%d\n轮询积压：%d", st.Active, st.Paused, st.RebindRequired, st.Banned, st.Backlog))
		}
	case "/admin_pause":
		if c.store.SetGlobalPaused(ctx, true) == nil {
			_ = c.ui.SendText(ctx, m.ChatID, "全局轮询已暂停。")
		}
	case "/admin_resume":
		if c.store.SetGlobalPaused(ctx, false) == nil {
			_ = c.ui.SendText(ctx, m.ChatID, "全局轮询已恢复。")
		}
	case "/admin_ban", "/admin_unban":
		if len(fields) != 2 {
			_ = c.ui.SendText(ctx, m.ChatID, "用法："+cmd+" <telegram_id>")
			return
		}
		id, e := strconv.ParseInt(fields[1], 10, 64)
		if e != nil || id <= 0 {
			_ = c.ui.SendText(ctx, m.ChatID, "Telegram ID 无效。")
			return
		}
		if cmd == "/admin_ban" {
			e = c.store.Ban(ctx, id, m.TelegramID)
		} else {
			e = c.store.Unban(ctx, id)
		}
		if e == nil {
			_ = c.ui.SendText(ctx, m.ChatID, "操作完成。")
		}
	}
}

func (c *Controller) HandleCallback(ctx context.Context, cb IncomingCallback) {
	if !cb.Private || cb.TelegramID <= 0 {
		return
	}
	userLock := &c.userLocks[uint64(cb.TelegramID)%uint64(len(c.userLocks))]
	userLock.Lock()
	defer userLock.Unlock()
	switch cb.Data {
	case "unbind:confirm":
		if e := c.store.Unbind(ctx, cb.TelegramID); e != nil {
			c.log.Error("unbind account", "telegram_id", cb.TelegramID, "error", e)
			_ = c.ui.AnswerCallback(ctx, cb.ID, "删除失败")
		} else {
			_ = c.ui.AnswerCallback(ctx, cb.ID, "已删除")
			_ = c.ui.SendText(ctx, cb.ChatID, "绑定、加密 Cookie 和全部通知去重记录已删除。")
		}
	case "unbind:cancel":
		_ = c.ui.AnswerCallback(ctx, cb.ID, "已取消")
	default:
		_ = c.ui.AnswerCallback(ctx, cb.ID, "操作已过期")
	}
}
