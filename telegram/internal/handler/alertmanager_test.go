package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// stubSender records messages instead of calling Telegram.
type stubSender struct {
	messages []string
	err      error
}

func (s *stubSender) Send(text string) error {
	if s.err != nil {
		return s.err
	}
	s.messages = append(s.messages, text)
	return nil
}

const amPayload = `{
  "status": "firing",
  "alerts": [
    {"status":"firing","labels":{"alertname":"HighCPU","severity":"critical","namespace":"media"},"annotations":{"summary":"CPU above 90%"},"startsAt":"2026-10-17T10:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://prometheus/graph"},
    {"status":"resolved","labels":{"alertname":"DiskFull","severity":"warning"},"annotations":{"summary":"Disk is fine"},"startsAt":"2026-10-17T09:00:00Z","endsAt":"2026-10-17T10:00:00Z"}
  ],
  "commonLabels":{"severity":"critical"},
  "groupLabels":{"alertname":"HighCPU"},
  "externalURL":"http://alertmanager:9093",
  "version":"4",
  "truncatedAlerts":0
}`

func TestAlertmanagerHandlerStatus(t *testing.T) {
	const secret = "s3cret"

	tests := []struct {
		name       string
		secret     string
		method     string
		auth       string
		body       string
		sendErr    error
		wantStatus int
		wantSent   bool
	}{
		{"auth disabled", "", http.MethodPost, "", amPayload, nil, http.StatusNoContent, true},
		{"auth accepted", secret, http.MethodPost, "Bearer " + secret, amPayload, nil, http.StatusNoContent, true},
		{"auth missing", secret, http.MethodPost, "", amPayload, nil, http.StatusUnauthorized, false},
		{"auth wrong token", secret, http.MethodPost, "Bearer nope", amPayload, nil, http.StatusUnauthorized, false},
		{"bad json", "", http.MethodPost, "", `{"status":`, nil, http.StatusBadRequest, false},
		{"method not allowed", "", http.MethodGet, "", "", nil, http.StatusMethodNotAllowed, false},
		{"telegram failure", "", http.MethodPost, "", amPayload, errors.New("boom"), http.StatusInternalServerError, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubSender{err: tt.sendErr}
			h := NewAlertmanager(stub, tt.secret)

			req := httptest.NewRequest(tt.method, "/alertmanager", strings.NewReader(tt.body))
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if sent := len(stub.messages) > 0; sent != tt.wantSent {
				t.Fatalf("sent = %v, want %v", sent, tt.wantSent)
			}
			if tt.wantSent && !strings.Contains(stub.messages[0], "🔥 1 firing · 1 resolved") {
				t.Fatalf("unexpected message: %q", stub.messages[0])
			}
		})
	}
}

func TestBuildAlertmanagerMessage(t *testing.T) {
	longSummary := strings.Repeat("x", 300)
	manyAlerts := make([]Alert, 0, 13)
	for i := 0; i < 13; i++ {
		manyAlerts = append(manyAlerts, Alert{
			Status:      "firing",
			Labels:      map[string]string{"alertname": fmt.Sprintf("alert%d", i)},
			Annotations: map[string]string{"summary": "s"},
		})
	}
	wideAlerts := make([]Alert, 0, maxAlerts)
	for i := 0; i < maxAlerts; i++ {
		wideAlerts = append(wideAlerts, Alert{
			Status:      "firing",
			Labels:      map[string]string{"alertname": "wide", "namespace": strings.Repeat("n", 500)},
			Annotations: map[string]string{"summary": "s"},
		})
	}

	tests := []struct {
		name    string
		payload AlertmanagerWebhook
		want    []string
		notWant []string
	}{
		{
			name: "firing and resolved",
			payload: AlertmanagerWebhook{
				Status: "firing",
				Alerts: []Alert{
					{Status: "firing", Labels: map[string]string{"alertname": "HighCPU", "severity": "critical", "namespace": "media"}, Annotations: map[string]string{"summary": "CPU above 90%"}},
					{Status: "resolved", Labels: map[string]string{"alertname": "DiskFull", "severity": "warning"}, Annotations: map[string]string{"summary": "Disk is fine"}},
				},
			},
			want: []string{"🔥 1 firing · 1 resolved", "🔥 *HighCPU*", "severity: critical", "ns: media", "CPU above 90%", "✅ *DiskFull*", "Disk is fine"},
		},
		{
			name: "all resolved",
			payload: AlertmanagerWebhook{
				Status: "resolved",
				Alerts: []Alert{{Status: "resolved", Labels: map[string]string{"alertname": "DiskFull"}}},
			},
			want:    []string{"✅ 1 resolved", "✅ *DiskFull*"},
			notWant: []string{"🔥"},
		},
		{
			name:    "escapes labels",
			payload: AlertmanagerWebhook{Alerts: []Alert{{Status: "firing", Labels: map[string]string{"alertname": "CPU (prod)"}}}},
			want:    []string{`CPU \(prod\)`},
		},
		{
			name:    "more than max alerts",
			payload: AlertmanagerWebhook{Alerts: manyAlerts},
			want:    []string{"+3 more alerts"},
			notWant: []string{"alert12"},
		},
		{
			name: "long summary truncated",
			payload: AlertmanagerWebhook{Alerts: []Alert{
				{Status: "firing", Labels: map[string]string{"alertname": "Verbose"}, Annotations: map[string]string{"summary": longSummary}},
			}},
			want:    []string{strings.Repeat("x", maxSummaryRunes) + "…"},
			notWant: []string{strings.Repeat("x", maxSummaryRunes+1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := buildAlertmanagerMessage(tt.payload)
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("message missing %q:\n%s", want, msg)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(msg, notWant) {
					t.Fatalf("message unexpectedly contains %q:\n%s", notWant, msg)
				}
			}
		})
	}

	t.Run("message length cap", func(t *testing.T) {
		msg := buildAlertmanagerMessage(AlertmanagerWebhook{Alerts: wideAlerts})
		if n := utf8.RuneCountInString(msg); n > maxMessageRunes {
			t.Fatalf("message is %d runes, want <= %d", n, maxMessageRunes)
		}
		if !strings.HasSuffix(msg, truncatedSuffix) {
			t.Fatalf("message missing truncation suffix: %q", msg)
		}
		body := strings.TrimSuffix(msg, truncatedSuffix)
		if strings.HasSuffix(body, "\\") {
			t.Fatalf("message ends with a dangling escape: %q", body)
		}
	})
}

func TestTruncateMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{
			name: "under limit unchanged",
			in:   "🔥 *a*\nline",
			max:  100,
			want: "🔥 *a*\nline",
		},
		{
			name: "cuts on newline boundary",
			in:   "aaaa\nbbbb\ncccc\ndddd\neeee\nffff",
			max:  25,
			want: "aaaa\nbbbb" + truncatedSuffix,
		},
		{
			name: "drops dangling escape",
			in:   "abcdefghij\\" + strings.Repeat("k", 20),
			max:  26,
			want: "abcdefghij" + truncatedSuffix,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMessage(tt.in, tt.max)
			if got != tt.want {
				t.Fatalf("truncateMessage() = %q, want %q", got, tt.want)
			}
			if n := utf8.RuneCountInString(got); tt.max > 0 && len([]rune(tt.in)) > tt.max && n > tt.max {
				t.Fatalf("result is %d runes, want <= %d", n, tt.max)
			}
		})
	}
}
