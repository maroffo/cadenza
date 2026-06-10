#!/usr/bin/env bash
# ABOUTME: Registers the Telegram webhook with secret token after a URL-affecting deploy.
# ABOUTME: Usage: TELEGRAM_BOT_TOKEN=... TELEGRAM_WEBHOOK_SECRET=... SERVICE_URL=... ./set-webhook.sh

set -euo pipefail

: "${TELEGRAM_BOT_TOKEN:?}"
: "${TELEGRAM_WEBHOOK_SECRET:?}"
: "${SERVICE_URL:?e.g. https://cadenza-xxxx.run.app}"

curl -fsS "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/setWebhook" \
  -d "url=${SERVICE_URL}/telegram/webhook" \
  -d "secret_token=${TELEGRAM_WEBHOOK_SECRET}" \
  -d 'allowed_updates=["message","callback_query"]' \
  -d "drop_pending_updates=false"
echo
curl -fsS "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getWebhookInfo"
echo
