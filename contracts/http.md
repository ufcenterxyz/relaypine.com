# RelayPine HTTP contract

Console uses same-origin `/api/*`, proxied to Go. Auth uses HttpOnly `rp_session` cookie; JSON mutation requires same Origin as configured public URL. Errors: `{ "error": { "code": "...", "message": "..." } }`.

- POST `/api/auth/code` `{email,name?}` -> HTTP 202 `{status:"code_sent",expires_in:600,resend_after:60}`. A cryptographically random six-digit code is emailed to the normalized address. Names are optional (up to 100 UTF-8 bytes), bound to that challenge, and used only for a verified waitlist entry. Sending creates no account, session, or waitlist entry.
- POST `/api/auth/verify` `{email,code}` -> HTTP 200 `{status:"authenticated",user}` + cookie only for the fixed administrator `ufcenterxyz@gmail.com`; all other verified mailboxes receive HTTP 202 `{status:"waitlisted"}` without a session/account/key. Waitlist emails are deduplicated. Codes expire after ten minutes, allow at most five wrong attempts, are single-use, and are invalidated by a resend. Verification consumes the challenge atomically.
- Code delivery has a 60-second resend cooldown and ten-send limit per email per hour. Both endpoints additionally enforce normalized-email, IP and email/IP-pair rate limits. HTTP 429 includes `Retry-After` and `error.retry_after` seconds; code errors are `invalid_code`/`expired_code`. Delivery not configured or rejected returns 503 `email_unavailable`; storage failures return 503 `auth_unavailable` or fail-closed 429 from the limiter. Codes are never exposed in API responses or logs.
- Former POST `/api/auth/login`, `/api/auth/register`, and `/api/waitlist` return HTTP 410 `verification_required`. They cannot authenticate a password or submit an unverified waitlist entry.
- POST `/api/auth/logout` -> `{ok:true}`.
- GET `/api/state` -> Snapshot from `services/gateway/internal/domain/types.go`; unauthenticated 401. All lists arrays. Private access only: the fixed administrator and operator flag are required for every console route and relay key. Snapshot includes `waitlist:[{id,name,email,created_at}]`; removed `payments_enabled` and `orders` are absent. Money micro-USD; rates micro-USD per million tokens.
- POST `/api/keys` `{name,models:[],rpm:60}` -> `{key,secret}` secret displayed once.
- DELETE `/api/keys/{id}` revokes tenant-owned key.
- POST `/api/admin/endpoints` full Endpoint fields; no ID creates, with ID updates.
- POST `/api/admin/credentials` full Credential fields plus `secret` on create/rotation. Empty secret preserves existing.
- POST `/api/admin/proxies` `{id?,name,url,enabled}`; empty URL preserves existing.
- POST `/api/admin/models` full Model fields; save increments version.
- POST `/api/admin/routing` `{strategy:"least_load"|"highest_balance"|"balanced",tenant_rpm:120,tenant_concurrency:10}`.
- POST `/api/admin/credits` `{tenant_id,amount_micros,reason,idempotency_key}` audited adjustment, positive only.
- Former `/api/billing/checkout`, `/api/admin/refunds`, and `/api/payments/*` routes return 404. No Stripe calls, webhook processing, or online payment capability remains. Historical payment data is retained in the database.
- Native SDK endpoints `/openai/v1/chat/completions`, `/openai/v1/responses`, `/anthropic/v1/messages`, `/anthropic/v1/messages/count_tokens`, and each provider's `/v1/models`. Access Key Bearer (Anthropic also x-api-key). Native traffic must not use cookie auth.

UI routes: `/` product entry, `/app` overview, `/app/keys`, `/app/models`, `/app/usage`, `/app/billing`; operators additionally `/app/upstreams`, `/app/proxies`, `/app/routing`, `/app/waitlist`. No invented traffic or finances. Empty database has actionable setup, no demo statistics.

Bootstrap provisions the fixed administrator without a password. If
`BOOTSTRAP_EMAIL` is set, it must match that address. An email match alone does
not authenticate anyone: the email OTP must be delivered and verified. Existing
other accounts are preserved and backfilled into the waitlist; their sessions
and API keys cannot access the console or relay. Migrating an administrator from
password authentication replaces its password hash with an unusable marker and
revokes its old sessions and keys once. Only sessions with `auth_method=email_otp`
are accepted, including before a rolling migration finishes. Repeated startup
preserves sessions and keys issued after this migration. No waitlist approval or
invitation flow is exposed in this release.

Email delivery uses `MAILGUN_API_KEY`, `MAILGUN_DOMAIN`, `MAILGUN_FROM`, and optional
`MAILGUN_API_BASE` (`https://api.mailgun.net` or `https://api.eu.mailgun.net`). The
existing 32-byte `MASTER_KEY` derives a separate HMAC key for code verification;
Redis stores only a challenge ID and keyed digest, never the plaintext code.
Unconfigured delivery keeps the gateway running but returns an explicit 503.
