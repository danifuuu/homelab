// Bot is the homelab Telegram notification bot.
// It exposes a single HTTP endpoint (POST /notify) that receives structured
// notifications from other services and forwards them to a configured chat.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/dani/homelab/telegram/internal/handler"
	"github.com/dani/homelab/telegram/internal/telegram"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	token := requireEnv("TELEGRAM_BOT_TOKEN")
	chatIDStr := requireEnv("TELEGRAM_CHAT_ID")
	secret := os.Getenv("WEBHOOK_SECRET") // optional
	port := envOr("PORT", "8080")

	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid TELEGRAM_CHAT_ID %q: %w", chatIDStr, err)
	}

	tg, err := telegram.New(token, chatID)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/notify", handler.New(tg, secret))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := ":" + port
	slog.Info("bot listening", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required env variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
