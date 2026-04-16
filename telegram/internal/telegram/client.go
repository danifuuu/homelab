// Package telegram calls the Telegram Bot API directly over HTTPS.
// No third-party library is used.
package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const apiBase = "https://api.telegram.org/bot"

// Client sends messages to a Telegram chat.
type Client struct {
	token  string
	chatID int64
	http   *http.Client
}

// New creates a Client. token is the bot token; chatID is the target chat/user.
// The constructor validates the token by calling getMe.
func New(token string, chatID int64) (*Client, error) {
	c := &Client{
		token:  token,
		chatID: chatID,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
	if err := c.ping(); err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	return c, nil
}

// Send sends a MarkdownV2-formatted message to the configured chat.
func (c *Client) Send(text string) error {
	body, err := json.Marshal(map[string]any{
		"chat_id":    strconv.FormatInt(c.chatID, 10),
		"text":       text,
		"parse_mode": "MarkdownV2",
	})
	if err != nil {
		return fmt.Errorf("telegram: marshal sendMessage: %w", err)
	}

	resp, err := c.post("sendMessage", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return checkResponse(resp)
}

// ping calls getMe to verify the token is valid.
func (c *Client) ping() error {
	resp, err := c.http.Get(apiBase + c.token + "/getMe")
	if err != nil {
		return fmt.Errorf("getMe: %w", err)
	}
	defer resp.Body.Close()
	return checkResponse(resp)
}

func (c *Client) post(method string, body []byte) (*http.Response, error) {
	url := apiBase + c.token + "/" + method
	resp, err := c.http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("telegram: POST %s: %w", method, err)
	}
	return resp, nil
}

// apiResponse mirrors the top-level Telegram API response envelope.
type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

func checkResponse(resp *http.Response) error {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram: read response body: %w", err)
	}

	var ar apiResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return fmt.Errorf("telegram: decode response: %w", err)
	}
	if !ar.OK {
		return fmt.Errorf("telegram: API error: %s", ar.Description)
	}
	return nil
}
