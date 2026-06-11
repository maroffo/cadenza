#!/usr/bin/env bash
# ABOUTME: Registers the Telegram webhook with secret token after a URL-affecting deploy.
# ABOUTME: Usage: TELEGRAM_BOT_TOKEN=... TELEGRAM_WEBHOOK_SECRET=... SERVICE_URL=... ./set-webhook.sh

set -euo pipefail

: "${TELEGRAM_BOT_TOKEN:?}"
: "${TELEGRAM_WEBHOOK_SECRET:?}"
: "${SERVICE_URL:?e.g. https://cadenza-xxxx.run.app}"

# Secret via stdin so it never appears in the process list. Pick the secret
# 64+ random chars: the constant-time compare still leaks its LENGTH pre-auth.
# (The token-in-URL is inherent to the Bot API.)
printf 'secret_token=%s' "${TELEGRAM_WEBHOOK_SECRET}" | curl -fsS \
  "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/setWebhook" \
  -d "url=${SERVICE_URL}/telegram/webhook" \
  -d 'allowed_updates=["message","callback_query"]' \
  -d "drop_pending_updates=false" \
  -d @-
echo
curl -fsS "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getWebhookInfo"
echo
