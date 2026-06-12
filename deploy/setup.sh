#!/usr/bin/env bash
# ABOUTME: Idempotent GCP bootstrap for cadenza: every resource, describe-before-create.
# ABOUTME: Doubles as the runbook; safe to re-run. Requires: gcloud auth + a billing-enabled project.

set -euo pipefail

# ---- Parameters (override via env) -----------------------------------------
PROJECT="${PROJECT:?set PROJECT to the dedicated GCP project id}"
REGION="${REGION:-europe-west1}"
SERVICE="${SERVICE:-cadenza}"
QUEUE="${QUEUE:-cadenza-exec}"
TZ_CRON="${TZ_CRON:-Europe/Rome}"
# No apostrophes inside ${...:?msg}: bash treats quotes in expansions as
# quoting even within double quotes, and the script dies at EOF.
ALERT_EMAIL="${ALERT_EMAIL:?set ALERT_EMAIL for the deadman switch alert channel}"
GITHUB_REPO="${GITHUB_REPO:-maroffo/cadenza}"

RUN_SA="cadenza-run@${PROJECT}.iam.gserviceaccount.com"
INVOKER_SA="cadenza-invoker@${PROJECT}.iam.gserviceaccount.com"
DEPLOY_SA="cadenza-deploy@${PROJECT}.iam.gserviceaccount.com"

gcloud config set project "$PROJECT" >/dev/null

say() { printf '\n=== %s\n' "$*"; }

# ---- APIs -------------------------------------------------------------------
say "Enabling APIs"
gcloud services enable \
  run.googleapis.com cloudtasks.googleapis.com cloudscheduler.googleapis.com \
  firestore.googleapis.com secretmanager.googleapis.com \
  artifactregistry.googleapis.com monitoring.googleapis.com \
  iamcredentials.googleapis.com cloudbuild.googleapis.com

# ---- Service accounts ---------------------------------------------------------
say "Service accounts"
for SA_DESC in "cadenza-run:Cadenza runtime" "cadenza-invoker:Scheduler/Tasks invoker" "cadenza-deploy:GitHub Actions deployer"; do
  SA_ID="${SA_DESC%%:*}"; DESC="${SA_DESC#*:}"
  if ! gcloud iam service-accounts describe "${SA_ID}@${PROJECT}.iam.gserviceaccount.com" >/dev/null 2>&1; then
    gcloud iam service-accounts create "$SA_ID" --display-name="$DESC"
  fi
done

say "IAM bindings"
gcloud projects add-iam-policy-binding "$PROJECT" --member="serviceAccount:${RUN_SA}" --role=roles/datastore.user --condition=None >/dev/null
gcloud projects add-iam-policy-binding "$PROJECT" --member="serviceAccount:${RUN_SA}" --role=roles/cloudtasks.enqueuer --condition=None >/dev/null
# Run SA must mint OIDC tokens as the invoker SA when enqueueing tasks (M3+).
gcloud iam service-accounts add-iam-policy-binding "$INVOKER_SA" \
  --member="serviceAccount:${RUN_SA}" --role=roles/iam.serviceAccountUser >/dev/null

# ---- Firestore ----------------------------------------------------------------
say "Firestore (Native, ${REGION})"
if ! gcloud firestore databases describe --database="(default)" >/dev/null 2>&1; then
  gcloud firestore databases create --location="$REGION" --type=firestore-native
fi
say "Firestore TTL policies (dedup 7d-style cleanup, session turns 18m retention)"
gcloud firestore fields ttls update expires_at \
  --collection-group=dedup --enable-ttl --async || true
gcloud firestore fields ttls update expires_at \
  --collection-group=turns --enable-ttl --async || true
gcloud firestore fields ttls update expires_at \
  --collection-group=profile_events --enable-ttl --async || true
gcloud firestore fields ttls update expires_at \
  --collection-group=events_written --enable-ttl --async || true
gcloud firestore fields ttls update expires_at \
  --collection-group=injuries --enable-ttl --async || true
gcloud firestore fields ttls update expires_at \
  --collection-group=log --enable-ttl --async || true
gcloud firestore fields ttls update expires_at \
  --collection-group=web_nonces --enable-ttl --async || true
gcloud firestore fields ttls update expires_at \
  --collection-group=web_sessions --enable-ttl --async || true

# ---- Artifact Registry ---------------------------------------------------------
say "Artifact Registry"
if ! gcloud artifacts repositories describe cadenza --location="$REGION" >/dev/null 2>&1; then
  gcloud artifacts repositories create cadenza --location="$REGION" --repository-format=docker
fi

# ---- Secrets (values added manually; pinned versions referenced by deploy) -----
say "Secrets (create empty shells; add versions with: gcloud secrets versions add <name> --data-file=-)"
for S in cadenza-telegram-bot-token cadenza-telegram-webhook-secret cadenza-icu-api-key cadenza-anthropic-api-key cadenza-web-session-secret; do
  if ! gcloud secrets describe "$S" >/dev/null 2>&1; then
    gcloud secrets create "$S" --replication-policy=user-managed --locations="$REGION"
  fi
  gcloud secrets add-iam-policy-binding "$S" \
    --member="serviceAccount:${RUN_SA}" --role=roles/secretmanager.secretAccessor >/dev/null
done

# ---- Cloud Tasks queue (created now, consumed from M3) -------------------------
say "Cloud Tasks queue"
if ! gcloud tasks queues describe "$QUEUE" --location="$REGION" >/dev/null 2>&1; then
  gcloud tasks queues create "$QUEUE" --location="$REGION"
fi
gcloud tasks queues update "$QUEUE" --location="$REGION" \
  --max-concurrent-dispatches=1 --max-dispatches-per-second=1 \
  --max-attempts=5 --min-backoff=10s --max-backoff=300s >/dev/null

# ---- First deploy must exist before Scheduler (needs the URL) ------------------
say "Cloud Run service check"
if ! gcloud run services describe "$SERVICE" --region="$REGION" >/dev/null 2>&1; then
  cat <<'EOT'
Cloud Run service not deployed yet. Deploy first (CI or manually):
  gcloud run deploy cadenza --region=$REGION \
    --image=$REGION-docker.pkg.dev/$PROJECT/cadenza/cadenza:latest \
    --service-account=cadenza-run@$PROJECT.iam.gserviceaccount.com \
    --allow-unauthenticated --min-instances=0 --max-instances=1 \
    --timeout=600 --memory=512Mi --cpu-boost \
    --set-env-vars=ENV=prod,GCP_PROJECT=$PROJECT,... \
    --set-secrets=TELEGRAM_BOT_TOKEN=cadenza-telegram-bot-token:1,ICU_API_KEY=cadenza-icu-api-key:1
Then re-run this script to create Scheduler jobs + invoker binding.
EOT
  exit 0
fi

# SERVICE_URL can be overridden: services can expose two URLs (legacy
# x-suffix and deterministic project-number form) and status.url is not
# stable about which one it reports. The OIDC audience must match the
# EXECUTOR_AUDIENCE env exactly, so pass the canonical one explicitly.
SERVICE_URL="${SERVICE_URL_OVERRIDE:-$(gcloud run services describe "$SERVICE" --region="$REGION" --format='value(status.url)')}"
say "Service URL (OIDC audience): $SERVICE_URL"

# The deploy SA holds run.developer (least privilege, no setIamPolicy), so
# the deploy workflow CANNOT apply --allow-unauthenticated itself: the
# public binding for the Telegram webhook lives here instead. Idempotent.
gcloud run services add-iam-policy-binding "$SERVICE" --region="$REGION" \
  --member="allUsers" --role=roles/run.invoker >/dev/null
gcloud run services add-iam-policy-binding "$SERVICE" --region="$REGION" \
  --member="serviceAccount:${INVOKER_SA}" --role=roles/run.invoker >/dev/null

# ---- Scheduler jobs -------------------------------------------------------------
say "Scheduler jobs (${TZ_CRON})"
create_job() {
  local NAME="$1" CRON="$2" BODY="$3"
  if gcloud scheduler jobs describe "$NAME" --location="$REGION" >/dev/null 2>&1; then
    gcloud scheduler jobs update http "$NAME" --location="$REGION" \
      --schedule="$CRON" --time-zone="$TZ_CRON" --uri="${SERVICE_URL}/internal/execute" \
      --http-method=POST --message-body="$BODY" \
      --oidc-service-account-email="$INVOKER_SA" --oidc-token-audience="$SERVICE_URL" \
      --attempt-deadline=540s >/dev/null
  else
    gcloud scheduler jobs create http "$NAME" --location="$REGION" \
      --schedule="$CRON" --time-zone="$TZ_CRON" --uri="${SERVICE_URL}/internal/execute" \
      --http-method=POST --message-body="$BODY" \
      --oidc-service-account-email="$INVOKER_SA" --oidc-token-audience="$SERVICE_URL" \
      --attempt-deadline=540s >/dev/null
  fi
}
# IDs are derived server-side from the date; static bodies are fine.
create_job cadenza-morning   "0 7 * * *"  '{"v":1,"type":"morning_check","id":"morning-scheduler"}'
create_job cadenza-watchdog  "15 7 * * *" '{"v":1,"type":"watchdog","id":"watchdog-scheduler"}'
create_job cadenza-reconcile "0 12 * * *" '{"v":1,"type":"daily_reconcile","id":"reconcile-scheduler"}'

# --- Firestore backup (M9.2): daily export to GCS, 90-day lifecycle -------
BACKUP_BUCKET="gs://cadenza-backups-${PROJECT}"
if ! gsutil ls -b "$BACKUP_BUCKET" >/dev/null 2>&1; then
  gsutil mb -l "$REGION" -b on "$BACKUP_BUCKET"
fi
cat > /tmp/cadenza-backup-lifecycle.json <<'LIFECYCLE'
{"rule":[{"action":{"type":"Delete"},"condition":{"age":90}}]}
LIFECYCLE
gsutil lifecycle set /tmp/cadenza-backup-lifecycle.json "$BACKUP_BUCKET"
# Firestore service agent writes the export; invoker SA calls the API.
PROJECT_NUMBER=$(gcloud projects describe "$PROJECT" --format="value(projectNumber)")
FS_SA="service-${PROJECT_NUMBER}@gcp-sa-firestore.iam.gserviceaccount.com"
gsutil iam ch "serviceAccount:${FS_SA}:roles/storage.objectAdmin" "$BACKUP_BUCKET" || true
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member="serviceAccount:cadenza-invoker@${PROJECT}.iam.gserviceaccount.com" \
  --role=roles/datastore.importExportAdmin --condition=None >/dev/null
if ! gcloud scheduler jobs describe cadenza-backup --location="$REGION" >/dev/null 2>&1; then
  gcloud scheduler jobs create http cadenza-backup \
    --location="$REGION" --schedule="0 3 * * *" --time-zone="Europe/Rome" \
    --uri="https://firestore.googleapis.com/v1/projects/${PROJECT}/databases/(default):exportDocuments" \
    --http-method=POST \
    --message-body="{\"outputUriPrefix\":\"${BACKUP_BUCKET}\"}" \
    --oauth-service-account-email="cadenza-invoker@${PROJECT}.iam.gserviceaccount.com" \
    --attempt-deadline=540s
fi

# ---- Dead-man's switch: email on Scheduler failures + watchdog ERROR ------------
say "Monitoring: notification channel + alert policies"
CHANNEL=$(gcloud beta monitoring channels list --filter="displayName='cadenza-email'" --format='value(name)' | head -1)
if [ -z "$CHANNEL" ]; then
  CHANNEL=$(gcloud beta monitoring channels create --display-name="cadenza-email" \
    --type=email --channel-labels="email_address=${ALERT_EMAIL}" --format='value(name)')
fi
ensure_policy() {
  # REST API: gcloud's "alpha monitoring" prompts to install a component,
  # which kills non-interactive runs. Idempotent by displayName check.
  local NAME="$1" FILTER="$2"
  local TOKEN; TOKEN=$(gcloud auth print-access-token)
  local EXISTING
  EXISTING=$(curl -fsS "https://monitoring.googleapis.com/v3/projects/${PROJECT}/alertPolicies?filter=display_name%3D%22${NAME// /%20}%22" \
    -H "Authorization: Bearer $TOKEN" | grep -c '"name"' || true)
  if [ "$EXISTING" -gt 0 ]; then return 0; fi
  curl -fsS -X POST "https://monitoring.googleapis.com/v3/projects/${PROJECT}/alertPolicies" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"displayName\":\"$NAME\",\"combiner\":\"OR\",\"notificationChannels\":[\"$CHANNEL\"],
         \"conditions\":[{\"displayName\":\"$NAME\",\"conditionThreshold\":{
           \"filter\":\"$FILTER\",\"comparison\":\"COMPARISON_GT\",\"thresholdValue\":0,
           \"duration\":\"0s\",\"aggregations\":[{\"alignmentPeriod\":\"300s\",\"perSeriesAligner\":\"ALIGN_COUNT\"}]}}]}" >/dev/null
}
ensure_policy "cadenza scheduler failures" \
  'metric.type=\"logging.googleapis.com/log_entry_count\" AND resource.type=\"cloud_scheduler_job\" AND metric.label.severity=\"ERROR\"'
ensure_policy "cadenza watchdog errors" \
  'metric.type=\"logging.googleapis.com/log_entry_count\" AND resource.type=\"cloud_run_revision\" AND resource.label.service_name=\"'"$SERVICE"'\" AND metric.label.severity=\"ERROR\"'

say "Done. Remaining manual steps: see deploy/README.md"
