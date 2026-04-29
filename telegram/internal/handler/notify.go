// Package handler provides the HTTP webhook handler that receives m3uparser
// notifications and forwards them to Telegram.
package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dani/homelab/telegram/internal/telegram"
)

// MediaItem represents a single piece of added or removed media.
type MediaItem struct {
	Title string `json:"title"` // human-readable title, e.g. "Game of Thrones S08E01"
	Type  string `json:"type"`  // "movie" | "tv" | "unsorted"
}

// DownloadError describes a single failed M3U download.
type DownloadError struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// Notification is the payload POSTed by the m3uparser cronjob.
type Notification struct {
	// Added and Removed hold the explicit list of media items.
	Added   []MediaItem `json:"added"`
	Removed []MediaItem `json:"removed"`

	// Stats
	Errors int `json:"errors"`

	// DownloadErrors holds per-URL download failures.
	DownloadErrors []DownloadError `json:"download_errors,omitempty"`
}

// NotifyHandler handles POST /notify requests.
type NotifyHandler struct {
	tg     *telegram.Client
	secret string // optional shared secret checked in Authorization header
}

// New creates a NotifyHandler.  secret may be empty to disable auth.
func New(tg *telegram.Client, secret string) *NotifyHandler {
	return &NotifyHandler{tg: tg, secret: secret}
}

func (h *NotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.secret != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+h.secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var n Notification
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	msg := buildMessage(n)
	if err := h.tg.Send(msg); err != nil {
		slog.Error("failed to send telegram message", "error", err)
		http.Error(w, "telegram error", http.StatusInternalServerError)
		return
	}

	slog.Info("notification sent",
		"added", len(n.Added),
		"removed", len(n.Removed),
		"errors", n.Errors,
	)
	w.WriteHeader(http.StatusNoContent)
}

// buildMessage formats the Notification into a MarkdownV2 Telegram message.
func buildMessage(n Notification) string {
	now := time.Now().UTC().Format("02 Jan 2006 15:04 UTC")
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("*m3uparser run* — %s\n\n", escape(now)))

	if len(n.Added) > 0 {
		sb.WriteString("*Media added:*\n")
		for _, item := range n.Added {
			sb.WriteString(fmt.Sprintf("  \\+ %s _%s_\n", escape(item.Title), escape(item.Type)))
		}
		sb.WriteString("\n")
	}

	if len(n.Removed) > 0 {
		sb.WriteString("*Media removed:*\n")
		for _, item := range n.Removed {
			sb.WriteString(fmt.Sprintf("  \\- %s _%s_\n", escape(item.Title), escape(item.Type)))
		}
		sb.WriteString("\n")
	}

	if len(n.Added) == 0 && len(n.Removed) == 0 {
		sb.WriteString("_No media changes this run\\._\n\n")
	}

	if len(n.DownloadErrors) > 0 {
		sb.WriteString("*Download errors:*\n")
		for _, de := range n.DownloadErrors {
			sb.WriteString(fmt.Sprintf("  ⚠️ `%s`\n  _%s_\n", escape(de.URL), escape(de.Error)))
		}
		sb.WriteString("\n")
	}

	// Stats footer
	sb.WriteString(fmt.Sprintf(
		"📊 Added: *%d* \\| Removed: *%d* \\| Errors: *%d*",
		len(n.Added), len(n.Removed), n.Errors,
	))

	return sb.String()
}

// escape escapes special characters for Telegram MarkdownV2.
func escape(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(s)
}
