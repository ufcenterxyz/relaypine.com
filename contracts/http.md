# RelayPine HTTP contract

Console uses same-origin `/api/*`, proxied to Go. Auth uses HttpOnly `rp_session` cookie; JSON mutation requires same Origin as configured public URL. Errors: `{ "error": { "code": "...", "message": "..." } }`.

- POST `/api/auth/code` `{email,name?}` -> HTTP 202 `{status:"code_sent",expires_in:600,resend_after:60}`. A cryptographically random six-digit code is emailed to the normalized address. Names are optional (up to 100 UTF-8 bytes), bound to that challenge, and used only for a verified waitlist entry. Sending creates no account, session, or waitlist entry.
- POST `/api/auth/verify` `{email,code}` -> HTTP 200 `{status:"authenticated",user}` + cookie only for the administrator configured by `SUPERADMIN_EMAIL`; all other verified mailboxes receive HTTP 202 `{status:"waitlisted"}` without a session/account/key. Waitlist emails are deduplicated. Codes expire after ten minutes, allow at most five wrong attempts, are single-use, and are invalidated by a resend. Verification consumes the challenge atomically.
- Code delivery has a 60-second resend cooldown and ten-send limit per email per hour. Both endpoints additionally enforce normalized-email, IP and email/IP-pair rate limits. HTTP 429 includes `Retry-After` and `error.retry_after` seconds; code errors are `invalid_code`/`expired_code`. Delivery not configured or rejected returns 503 `email_unavailable`; storage failures return 503 `auth_unavailable` or fail-closed 429 from the limiter. Codes are never exposed in API responses or logs.
- Former POST `/api/auth/login`, `/api/auth/register`, and `/api/waitlist` return HTTP 410 `verification_required`. They cannot authenticate a password or submit an unverified waitlist entry.
- POST `/api/auth/logout` -> `{ok:true}`.
- GET `/api/state` -> Snapshot from `services/gateway/internal/domain/types.go`; unauthenticated 401. All lists arrays. Private access only: the configured administrator and operator flag are required for every console route and relay key. Snapshot includes `waitlist:[{id,name,email,created_at}]`; removed `payments_enabled` and `orders` are absent. Money micro-USD; rates micro-USD per million tokens.
- POST `/api/keys` `{name,models:[],rpm:60}` -> `{key,secret}` secret displayed once.
- DELETE `/api/keys/{id}` revokes tenant-owned key.
- POST `/api/admin/endpoints` full Endpoint fields; no ID creates, with ID updates.
- POST `/api/admin/credentials` full Credential fields plus `secret` on create/rotation. Empty secret preserves existing. Optional `workspace_id` is trimmed and must match `wrkspc_[A-Za-z0-9]+` within 128 ASCII characters, or be empty. A nonempty workspace is allowed only for an Anthropic endpoint. The value is returned in the administrator snapshot; raw upstream secrets are never returned.
- Creating a credential may submit only `{endpoint_id,secret}`. Omitted advanced settings receive local defaults: generated name, endpoint-shared quota group, RPM 60, TPM 100000, concurrency 10, enabled true, budget and priority 0. Explicit invalid zero capacities are rejected; explicit overrides, including enabled false, are preserved. Updates retain full-object semantics.
- POST `/api/admin/credentials/discover` `{id?,endpoint_id,secret?,workspace_id?,proxy_id?}` performs bounded read-only upstream discovery. Blank secret may reuse exactly the supplied credential ID on the same endpoint; no stored secret is returned. Saved proxy selection is honored with no direct fallback. Response: `{status:"verified"|"workspace_required",models:[{id,name,max_output_tokens?}],workspaces:[{id,name}],workspace_id,organization_id,defaults,limits_source:"provider"|"default",warnings,checked_at}`. Discovery does not save credentials/models or generate billable inference. Requires the configured superadmin session, same-origin POST, and rate-limit allowance.
- POST `/api/admin/proxies` `{id?,name,url,enabled}`; empty URL preserves existing.
- POST `/api/admin/models` full Model fields; save increments version. Optional `create_only:true` performs an atomic insert and returns 409 if the ID exists, preserving its configuration and pricing version. `billing_mode` is `metered` (the default when omitted) or `usage_only`. Enabled metered models require positive input/output sales and cost rates; incomplete prices may be saved only while disabled. Usage-only models require all eight rates to be zero and may be enabled without pricing. Both modes require a valid maximum output token count.
- POST `/api/admin/models/import` `{models:[Model,...]}` accepts 1–400 models and is always create-only. All rows are validated before any write; duplicate IDs within the request are rejected. Existing catalog IDs are skipped without changing their configuration or version. New rows are created with version 1 in one transaction; any other database error rolls the batch back. HTTP 200 returns `{created_count,skipped_count,results:[{id,status:"created"|"skipped",reason?:"already_exists"}]}` in input order. Retrying a committed batch only skips existing entries. The route requires the administrator session, exact Origin, and POST; it never changes endpoint/access-key allowlists, budgets, or balances.
- POST `/api/admin/routing` `{strategy:"least_load"|"highest_balance"|"balanced",tenant_rpm:120,tenant_concurrency:10}`.
- POST `/api/admin/credits` `{tenant_id,amount_micros,reason,idempotency_key}` audited adjustment, positive only.
- Former `/api/billing/checkout`, `/api/admin/refunds`, and `/api/payments/*` routes return 404. No Stripe calls, webhook processing, or online payment capability remains. Historical payment data is retained in the database.
- Native SDK endpoints `/openai/v1/chat/completions`, `/openai/v1/responses`, `/anthropic/v1/messages`, `/anthropic/v1/messages/count_tokens`, and each provider's `/v1/models`. Access Key Bearer (Anthropic also x-api-key). Native traffic must not use cookie auth.
- Provider `/v1/models` lists the local published catalog, filtered by protocol, enabled status, client-key model permissions, and a matching enabled endpoint. Upstream key discovery does not publish models. An empty local catalog yields an empty list; model lists do not check wallet balance, credential capacity, or instantaneous upstream availability.
- Batch-imported models default to `billing_mode=usage_only` in the console. Such requests record usage without local money holds or charges, and require an upstream credential with `budget_micros=0`. Configured monetary budgets are not silently bypassed. All authentication, model permissions, rate/concurrency limits, output bounds, protocol checks, and proxy behavior still apply.
- Request snapshots expose `billing_mode` and `cost_status`. A usage-only request has `cost_status=untracked`, even when token usage is known; metered costs are `recorded` or `pending` according to settlement state. These values come from the request's immutable price snapshot, not the model's current configuration. Usage-only requests do not add zero-value entries to the money ledger.
- Anthropic forwards `anthropic-version` (default `2023-06-01`) and the selected credential's nonempty `workspace_id` as `anthropic-workspace-id`. Client workspace headers never override routing configuration. Neither workspace nor beta headers are forwarded to OpenAI upstreams.
- `anthropic-beta` accepts repeated header fields and comma-separated values, trims optional spaces/tabs, deduplicates tokens, and enforces 1024 bytes/16 tokens before deduplication. Supported flags: `task-budgets-2026-03-13`, `thinking-display-updates-2026-08-18`, `thinking-binding-controls-2026-08-01`, `interleaved-thinking-2025-05-14`, `fine-grained-tool-streaming-2025-05-14`. Unknown, malformed, or unsupported beta flags return HTTP 400 before capacity or money reservation and before an upstream call.
- Any `fallbacks` property is rejected before reservation. Server-side fallback beta flags are also rejected; each fallback attempt can use a different model and price, which the current single-model accounting does not support. Existing standard-tier, content, tool, caching, and output-limit checks apply to beta requests too.

UI routes: `/` product entry, `/app` overview, `/app/keys`, `/app/models`, `/app/usage`, `/app/billing`; operators additionally `/app/upstreams`, `/app/proxies`, `/app/routing`, `/app/waitlist`. No invented traffic or finances. Empty database has actionable setup, no demo statistics.

Bootstrap provisions the administrator configured by required `SUPERADMIN_EMAIL`
without a password. Missing or invalid email configuration prevents startup.
`BOOTSTRAP_EMAIL` is no longer used. An email match alone does
not authenticate anyone: the email OTP must be delivered and verified. Existing
other accounts are preserved and backfilled into the waitlist; their sessions
and API keys cannot access the console or relay. Migrating an administrator from
password authentication replaces its password hash with an unusable marker and
revokes its old sessions and keys once. Only sessions with `auth_method=email_otp`
are accepted, including before a rolling migration finishes. Repeated startup
preserves sessions and keys issued after this migration. Changing the configured
email revokes other users' sessions and access keys; it does not transfer their
tenants or balances to the new identity. No waitlist approval or
invitation flow is exposed in this release.

Email delivery uses `MAILGUN_API_KEY`, `MAILGUN_DOMAIN`, `MAILGUN_FROM`, and optional
`MAILGUN_API_BASE` (`https://api.mailgun.net` or `https://api.eu.mailgun.net`). The
existing 32-byte `MASTER_KEY` derives a separate HMAC key for code verification;
Redis stores only a challenge ID and keyed digest, never the plaintext code.
Unconfigured delivery keeps the gateway running but returns an explicit 503.
