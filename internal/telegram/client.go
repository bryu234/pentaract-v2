package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL  string
	http     *http.Client
	mu       sync.Mutex
	lastSend time.Time
}

type Bot struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Chat struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
}

type SentDocument struct {
	MessageID int64    `json:"message_id"`
	Document  Document `json:"document"`
}

type File struct {
	FilePath string `json:"file_path"`
}

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 30 * time.Minute}}
}

func (c *Client) GetMe(ctx context.Context, token string) (Bot, error) {
	return request[Bot](ctx, c, token, "getMe", nil, false)
}

func (c *Client) GetChat(ctx context.Context, token string, chatID int64) (Chat, error) {
	return request[Chat](ctx, c, token, "getChat", url.Values{"chat_id": {strconv.FormatInt(chatID, 10)}}, false)
}

func (c *Client) SendDocument(ctx context.Context, token string, chatID int64, absolutePath, caption string) (SentDocument, error) {
	if !filepath.IsAbs(absolutePath) {
		return SentDocument{}, errors.New("Telegram local file path must be absolute")
	}
	if _, err := os.Stat(absolutePath); err != nil {
		return SentDocument{}, err
	}
	c.waitForSend(ctx)
	values := url.Values{
		"chat_id":              {strconv.FormatInt(chatID, 10)},
		"document":             {"file://" + absolutePath},
		"caption":              {caption},
		"disable_notification": {"true"},
	}
	return request[SentDocument](ctx, c, token, "sendDocument", values, true)
}

func (c *Client) ResolveFile(ctx context.Context, token, fileID string) (string, error) {
	file, err := request[File](ctx, c, token, "getFile", url.Values{"file_id": {fileID}}, false)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(file.FilePath) {
		return "", fmt.Errorf("local Bot API returned non-absolute path %q", file.FilePath)
	}
	cleaned := filepath.Clean(file.FilePath)
	if !strings.HasPrefix(cleaned, "/var/lib/telegram-bot-api/") {
		return "", errors.New("Telegram file path escapes the shared data directory")
	}
	return cleaned, nil
}

func (c *Client) DeleteMessage(ctx context.Context, token string, chatID, messageID int64) error {
	_, err := request[bool](ctx, c, token, "deleteMessage", url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)}, "message_id": {strconv.FormatInt(messageID, 10)},
	}, true)
	return err
}

func CloudLogout(ctx context.Context, token string) error {
	cloud := New("https://api.telegram.org")
	_, err := request[bool](ctx, cloud, token, "logOut", nil, true)
	return err
}

func (c *Client) waitForSend(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	wait := time.Until(c.lastSend.Add(time.Second))
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
	c.lastSend = time.Now()
}

func request[T any](ctx context.Context, c *Client, token, method string, values url.Values, retry bool) (T, error) {
	var zero T
	for attempt := 0; attempt < 5; attempt++ {
		var body io.Reader
		if values != nil {
			body = strings.NewReader(values.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+token+"/"+method, body)
		if err != nil {
			return zero, err
		}
		if values != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if retry && attempt < 4 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return zero, fmt.Errorf("Telegram %s request: %w", method, err)
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if readErr != nil {
			return zero, readErr
		}
		var decoded apiResponse[T]
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return zero, fmt.Errorf("Telegram %s returned invalid JSON (HTTP %d)", method, resp.StatusCode)
		}
		if decoded.OK {
			return decoded.Result, nil
		}
		if decoded.ErrorCode == http.StatusTooManyRequests && decoded.Parameters.RetryAfter > 0 && attempt < 4 {
			timer := time.NewTimer(time.Duration(decoded.Parameters.RetryAfter) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if retry && resp.StatusCode >= 500 && attempt < 4 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		return zero, fmt.Errorf("Telegram %s failed (%d): %s", method, decoded.ErrorCode, decoded.Description)
	}
	return zero, fmt.Errorf("Telegram %s failed after retries", method)
}
