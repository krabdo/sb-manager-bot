package manager

import (
	"context"
	"errors"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Telegram struct {
	bot        *bot.Bot
	controller *Controller
	store      *Store
	log        *slog.Logger
}

var telegramTrue = true

func NewTelegram(token, serverURL string, offset int64, store *Store, log *slog.Logger) (*Telegram, error) {
	initialOffset := offset
	if initialOffset > 0 {
		initialOffset-- // The library stores the last processed ID and adds one to the API offset.
	}
	opts := []bot.Option{bot.WithNotAsyncHandlers(), bot.WithWorkers(8), bot.WithInitialOffset(initialOffset), bot.WithAllowedUpdates(bot.AllowedUpdates{models.AllowedUpdateMessage, models.AllowedUpdateCallbackQuery}), bot.WithErrorsHandler(func(e error) { log.Warn("telegram polling error", "error", redactError(e)) })}
	if serverURL != "" {
		opts = append(opts, bot.WithServerURL(serverURL))
	}
	b, e := bot.New(token, opts...)
	if e != nil {
		return nil, errors.New("Telegram Bot API initialization failed: " + redactError(e))
	}
	t := &Telegram{bot: b, store: store, log: log}
	return t, nil
}
func (t *Telegram) SetController(c *Controller) { t.controller = c }
func (t *Telegram) Start(ctx context.Context) {
	t.bot.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, t.handle)
	t.bot.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, t.handle)
	t.bot.Start(ctx)
}
func (t *Telegram) handle(ctx context.Context, _ *bot.Bot, u *models.Update) {
	if t.controller == nil {
		return
	}
	if u.Message != nil && u.Message.From != nil {
		t.controller.HandleMessage(ctx, IncomingMessage{TelegramID: u.Message.From.ID, ChatID: u.Message.Chat.ID, MessageID: u.Message.ID, Private: u.Message.Chat.Type == models.ChatTypePrivate, Text: u.Message.Text})
	} else if u.CallbackQuery != nil {
		chat := int64(0)
		private := false
		if m := u.CallbackQuery.Message.Message; m != nil {
			chat = m.Chat.ID
			private = m.Chat.Type == models.ChatTypePrivate
		}
		t.controller.HandleCallback(ctx, IncomingCallback{TelegramID: u.CallbackQuery.From.ID, ChatID: chat, Private: private, ID: u.CallbackQuery.ID, Data: u.CallbackQuery.Data})
	}
	if e := t.store.UpdateOffset(ctx, u.ID+1); e != nil {
		t.log.Error("persist telegram update offset", "update_id", u.ID, "error", e)
	}
}
func (t *Telegram) SendText(ctx context.Context, chatID int64, text string) error {
	_, e := t.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &telegramTrue}})
	return e
}
func (t *Telegram) SendHTML(ctx context.Context, chatID int64, text string) error {
	_, e := t.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &telegramTrue}})
	return e
}
func (t *Telegram) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	ok, e := t.bot.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: messageID})
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("Telegram did not delete message")
	}
	return nil
}
func (t *Telegram) SendUnbindConfirmation(ctx context.Context, chatID int64) error {
	_, e := t.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "确认删除绑定、Cookie 和全部去重历史吗？", ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "确认删除", CallbackData: "unbind:confirm"}, {Text: "取消", CallbackData: "unbind:cancel"}}}}})
	return e
}
func (t *Telegram) AnswerCallback(ctx context.Context, id, text string) error {
	_, e := t.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text})
	return e
}
