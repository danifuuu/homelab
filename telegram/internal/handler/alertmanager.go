package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// maxAlerts is the number of alerts rendered individually; the rest are
	// summarised in a trailing "+N more alerts" line.
	maxAlerts = 10
	// maxSummaryRunes caps each alert summary annotation.
	maxSummaryRunes = 200
	// maxMessageRunes keeps the message below Telegram's 4096-rune limit.
	maxMessageRunes = 4000
	// truncatedSuffix is appended when a message is cut to maxMessageRunes.
	truncatedSuffix = "\n\n_… truncated_"
)

// AlertmanagerWebhook is the payload posted by an Alertmanager v4 webhook.
type AlertmanagerWebhook struct {
	Status       string            `json:"status"`
	Alerts       []Alert           `json:"alerts"`
	CommonLabels map[string]string `json:"commonLabels"`
	GroupLabels  map[string]string `json:"groupLabels"`
	ExternalURL  string            `json:"externalURL"`
}

// Alert is a single alert inside an AlertmanagerWebhook.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
}

// AlertmanagerHandler handles POST /alertmanager requests.
type AlertmanagerHandler struct {
	tg     sender
	secret string // optional shared secret checked in Authorization header
}

// NewAlertmanager creates an AlertmanagerHandler. secret may be empty to disable auth.
func NewAlertmanager(tg sender, secret string) *AlertmanagerHandler {
	return &AlertmanagerHandler{tg: tg, secret: secret}
}

func (h *AlertmanagerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !authorized(r, h.secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload AlertmanagerWebhook
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.tg.Send(buildAlertmanagerMessage(payload)); err != nil {
		slog.Error("failed to send telegram message", "error", err)
		http.Error(w, "telegram error", http.StatusInternalServerError)
		return
	}

	slog.Info("alertmanager notification sent",
		"status", payload.Status,
		"alerts", len(payload.Alerts),
	)
	w.WriteHeader(http.StatusNoContent)
}

// buildAlertmanagerMessage formats the payload into a compact MarkdownV2 message.
func buildAlertmanagerMessage(w AlertmanagerWebhook) string {
	firing, resolved := 0, 0
	for _, a := range w.Alerts {
		switch a.Status {
		case "firing":
			firing++
		case "resolved":
			resolved++
		}
	}

	var sb strings.Builder
	sb.WriteString(formatHeader(firing, resolved, w.Status))

	for i, a := range w.Alerts {
		if i >= maxAlerts {
			sb.WriteString("\n\n")
			sb.WriteString(escape(fmt.Sprintf("+%d more alerts", len(w.Alerts)-maxAlerts)))
			break
		}
		sb.WriteString("\n\n")
		sb.WriteString(formatAlert(a))
	}

	return truncateMessage(sb.String(), maxMessageRunes)
}

// formatHeader renders the "🔥 2 firing · 1 resolved" summary line.
func formatHeader(firing, resolved int, status string) string {
	switch {
	case firing > 0 && resolved > 0:
		return fmt.Sprintf("🔥 %d firing · %d resolved", firing, resolved)
	case firing > 0:
		return fmt.Sprintf("🔥 %d firing", firing)
	case resolved > 0:
		return fmt.Sprintf("✅ %d resolved", resolved)
	}
	if status == "" {
		status = "unknown"
	}
	return escape(strings.ToUpper(status))
}

// formatAlert renders one alert block.
func formatAlert(a Alert) string {
	icon := "🔥"
	if a.Status == "resolved" {
		icon = "✅"
	}

	name := a.Labels["alertname"]
	if name == "" {
		name = "alert"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s *%s*", icon, escape(name)))

	if sev := a.Labels["severity"]; sev != "" {
		sb.WriteString(" — severity: " + escape(sev))
	}
	if ns := a.Labels["namespace"]; ns != "" {
		sb.WriteString(" · ns: " + escape(ns))
	}
	if summary := a.Annotations["summary"]; summary != "" {
		sb.WriteString("\n" + escape(truncateRunes(summary, maxSummaryRunes)))
	}

	return sb.String()
}

// truncateRunes shortens s to at most max runes, marking the cut with an ellipsis.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// truncateMessage caps s at max runes, cutting on a newline boundary and
// appending truncatedSuffix so no MarkdownV2 escape is left dangling.
func truncateMessage(s string, max int) string {
	if max <= 0 {
		return s
	}

	r := []rune(s)
	if len(r) <= max {
		return s
	}

	cut := max - len([]rune(truncatedSuffix))
	if cut < 0 {
		cut = 0
	}
	if cut > len(r) {
		cut = len(r)
	}
	head := r[:cut]

	for i := len(head) - 1; i > 0; i-- {
		if head[i] == '\n' {
			head = head[:i]
			break
		}
	}
	// A trailing backslash would escape whatever comes next; drop it.
	for len(head) > 0 && head[len(head)-1] == '\\' {
		head = head[:len(head)-1]
	}

	return strings.TrimRight(string(head), "\n") + truncatedSuffix
}
