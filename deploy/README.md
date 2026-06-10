# ABOUTME: Deploy runbook: bootstrap order, secret handling, WIF, verification steps.
# ABOUTME: setup.sh is idempotent; this file covers the parts that are inherently manual.

# Deploy runbook

## One-time bootstrap (M2)

1. **Project**: create the dedicated project, link billing.
2. **Secrets values** (shells are created by setup.sh):
   ```bash
   printf '%s' "$TOKEN" | gcloud secrets versions add cadenza-telegram-bot-token --data-file=-
   printf '%s' "$KEY"   | gcloud secrets versions add cadenza-icu-api-key --data-file=-
   # webhook secret (M3) and anthropic key (M4) when those milestones land
   ```
   Deployments reference **pinned versions** (`:1`, `:2`, ...), never `latest`:
   a bad version must not brick every cold start.
3. **WIF for GitHub Actions** (keyless deploy):
   ```bash
   gcloud iam workload-identity-pools create github --location=global
   gcloud iam workload-identity-pools providers create-oidc github \
     --location=global --workload-identity-pool=github \
     --issuer-uri="https://token.actions.githubusercontent.com" \
     --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" \
     --attribute-condition="assertion.repository=='maroffo/cadenza'"
   gcloud iam service-accounts add-iam-policy-binding \
     cadenza-deploy@$PROJECT.iam.gserviceaccount.com \
     --role=roles/iam.workloadIdentityUser \
     --member="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/github/attribute.repository/maroffo/cadenza"
   gcloud projects add-iam-policy-binding $PROJECT \
     --member="serviceAccount:cadenza-deploy@$PROJECT.iam.gserviceaccount.com" --role=roles/run.admin
   gcloud projects add-iam-policy-binding $PROJECT \
     --member="serviceAccount:cadenza-deploy@$PROJECT.iam.gserviceaccount.com" --role=roles/artifactregistry.writer
   gcloud iam service-accounts add-iam-policy-binding \
     cadenza-run@$PROJECT.iam.gserviceaccount.com \
     --member="serviceAccount:cadenza-deploy@$PROJECT.iam.gserviceaccount.com" --role=roles/iam.serviceAccountUser
   ```
   Then set repo secrets `GCP_PROJECT`, `GCP_PROJECT_NUMBER`, `TELEGRAM_CHAT_ID`.
4. **First deploy**: push to `main` (deploy.yml) or run the gcloud command setup.sh prints.
5. **Re-run `deploy/setup.sh`**: with the service URL now known it creates Scheduler jobs, invoker binding, alert policies.
6. **Anthropic spend cap** (before M4): set the monthly limit in the Anthropic console.

## Verification after bootstrap (M2 checkpoint)

- `gcloud scheduler jobs run cadenza-morning --location=europe-west1` → message on the phone with real numbers + verdict.
- Run it twice → second run logs "already completed, no-op", no duplicate message.
- `gcloud scheduler jobs pause cadenza-morning`, wait for 07:15 next day → watchdog message + alert email. Unpause.

## Notes

- The service is `--allow-unauthenticated` because Telegram webhooks cannot do OIDC; `/internal/execute` does its own in-app OIDC (audience + invoker email).
- Scheduler `--attempt-deadline=540s` is deliberate: the default ~180s would mark a slow morning run failed and retry it mid-flight.
- The Tasks queue exists from M2 but is consumed starting M3 (webhook re-enqueue).
