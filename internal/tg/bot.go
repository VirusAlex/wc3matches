// Package tg is a minimal Telegram Bot API client for sendMessage / editMessageText.
// Handles HTTP 429 with retry-after.
package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const apiBase = "https://api.telegram.org/bot"

type Bot struct {
	token  string
	chatID string
	httpc  *http.Client
}

func New(token, chatID string) *Bot {
	return &Bot{
		token:  token,
		chatID: chatID,
		httpc:  &http.Client{Timeout: 30 * time.Second},
	}
}

type sendReq struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview,omitempty"`
}

type editReq struct {
	ChatID                string `json:"chat_id"`
	MessageID             int64  `json:"message_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview,omitempty"`
}

// richMessage is InputRichMessage; exactly one of html/markdown. We use html.
type richMessage struct {
	HTML string `json:"html"`
}

type sendRichReq struct {
	ChatID              string      `json:"chat_id"`
	RichMessage         richMessage `json:"rich_message"`
	DisableNotification bool        `json:"disable_notification,omitempty"`
}

type editRichReq struct {
	ChatID      string      `json:"chat_id"`
	MessageID   int64       `json:"message_id"`
	RichMessage richMessage `json:"rich_message"`
}

type tgResp struct {
	OK          bool   `json:"ok"`
	Result      any    `json:"result"`
	Description string `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type sendResult struct {
	MessageID int64 `json:"message_id"`
}

// SendHTML sends an HTML-formatted message and returns the new message ID.
func (b *Bot) SendHTML(ctx context.Context, text string) (int64, error) {
	body := sendReq{
		ChatID:                b.chatID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	}
	var res sendResult
	if err := b.call(ctx, "sendMessage", body, &res); err != nil {
		return 0, err
	}
	return res.MessageID, nil
}

// EditHTML edits an existing message. Returns ErrNotModified if Telegram says
// the content is identical (we treat that as not-an-error).
func (b *Bot) EditHTML(ctx context.Context, messageID int64, text string) error {
	body := editReq{
		ChatID:                b.chatID,
		MessageID:             messageID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	}
	return b.call(ctx, "editMessageText", body, nil)
}

// SendRich sends a rich message (Bot API 10.1) described as HTML and returns
// the new message ID. Rich HTML supports tables, headings, expandable quotes.
func (b *Bot) SendRich(ctx context.Context, html string) (int64, error) {
	body := sendRichReq{
		ChatID:      b.chatID,
		RichMessage: richMessage{HTML: html},
	}
	var res sendResult
	if err := b.call(ctx, "sendRichMessage", body, &res); err != nil {
		return 0, err
	}
	return res.MessageID, nil
}

// EditRich edits an existing message into rich content. Rich messages have no
// dedicated edit method; editMessageText carries the rich_message field instead.
// Returns ErrNotModified / ErrMessageNotEditable like EditHTML.
func (b *Bot) EditRich(ctx context.Context, messageID int64, html string) error {
	body := editRichReq{
		ChatID:      b.chatID,
		MessageID:   messageID,
		RichMessage: richMessage{HTML: html},
	}
	return b.call(ctx, "editMessageText", body, nil)
}

// ErrNotModified is returned by EditHTML when Telegram refuses the edit because
// the new content equals the old.
var ErrNotModified = errors.New("telegram: message not modified")

// ErrMessageNotEditable signals the message can't be edited (deleted, too old).
var ErrMessageNotEditable = errors.New("telegram: message not editable")

func (b *Bot) call(ctx context.Context, method string, body any, out any) error {
	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		err, retryAfter := b.callOnce(ctx, method, body, out)
		if err == nil {
			return nil
		}
		if retryAfter > 0 {
			delay := time.Duration(retryAfter+attempt*5) * time.Second
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}
	return fmt.Errorf("telegram %s: max retries exceeded", method)
}

func (b *Bot) callOnce(ctx context.Context, method string, body any, out any) (error, int) {
	buf, err := json.Marshal(body)
	if err != nil {
		return err, 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+b.token+"/"+method, bytes.NewReader(buf))
	if err != nil {
		return err, 0
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpc.Do(req)
	if err != nil {
		return err, 0
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var tg tgResp
	if err := json.Unmarshal(respBody, &tg); err != nil {
		return fmt.Errorf("decode: %w (raw: %s)", err, string(respBody)), 0
	}
	if tg.OK {
		if out != nil && tg.Result != nil {
			rr, _ := json.Marshal(tg.Result)
			if err := json.Unmarshal(rr, out); err != nil {
				return fmt.Errorf("result decode: %w", err), 0
			}
		}
		return nil, 0
	}
	// 429: retry after the server-specified delay
	if resp.StatusCode == http.StatusTooManyRequests && tg.Parameters != nil {
		return fmt.Errorf("rate limited (%ds)", tg.Parameters.RetryAfter), tg.Parameters.RetryAfter
	}
	if strings.Contains(tg.Description, "message is not modified") {
		return ErrNotModified, 0
	}
	if strings.Contains(tg.Description, "message to edit not found") ||
		strings.Contains(tg.Description, "message can't be edited") {
		return ErrMessageNotEditable, 0
	}
	return fmt.Errorf("telegram: %s", tg.Description), 0
}
