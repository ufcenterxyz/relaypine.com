# RelayPine HTTP contract

Console uses same-origin `/api/*`, proxied to Go. Auth uses HttpOnly `rp_session` cookie; JSON mutation requires same Origin as configured public URL. Errors: `{ "error": { "code": "...", "message": "..." } }`.

- POST `/api/waitlist` `{email,name?}` -> HTTP 202 `{status:"waitlisted"}`. Email is normalized and deduplicated; no account/session/key is created. Origin checked and rate limited.
- POST `/api/auth/register` legacy `{name?,email,password?}` -> same waitlist response. Password is ignored; public registration is closed.
- POST `/api/auth/login` `{email,password}` -> `{user}` + cookie only for preprovisioned `ufcenterxyz@gmail.com` with its valid password. Other addresses receive 403 `waitlist_required`; invalid administrator credentials receive 401.
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

Bootstrap requires `BOOTSTRAP_PASSWORD` and always provisions the fixed administrator
email. If `BOOTSTRAP_EMAIL` is set, it must match that address. Existing other
accounts are preserved and backfilled into the waitlist; their sessions and API
keys cannot access the console or relay. Promotion of an existing public account
at the administrator address replaces its old password and revokes its old
sessions and keys. No waitlist approval/invitation flow is exposed in this release.
