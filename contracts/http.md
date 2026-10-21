# RelayPine HTTP contract

Console uses same-origin `/api/*`, proxied to Go. Auth uses HttpOnly `rp_session` cookie; JSON mutation requires same Origin as configured public URL. Errors: `{ "error": { "code": "...", "message": "..." } }`.

- POST `/api/auth/register` `{name,email,password}` -> `{user}` + cookie. Creates tenant/project/wallet with zero funds.
- POST `/api/auth/login` `{email,password}` -> `{user}` + cookie.
- POST `/api/auth/logout` -> `{ok:true}`.
- GET `/api/state` -> Snapshot from `services/gateway/internal/domain/types.go`; unauthenticated 401. All lists arrays. Operator-only fields omitted for customers. Money micro-USD; rates micro-USD per million tokens.
- POST `/api/keys` `{name,models:[],rpm:60}` -> `{key,secret}` secret displayed once.
- DELETE `/api/keys/{id}` revokes tenant-owned key.
- POST `/api/admin/endpoints` full Endpoint fields; no ID creates, with ID updates.
- POST `/api/admin/credentials` full Credential fields plus `secret` on create/rotation. Empty secret preserves existing.
- POST `/api/admin/proxies` `{id?,name,url,enabled}`; empty URL preserves existing.
- POST `/api/admin/models` full Model fields; save increments version.
- POST `/api/admin/routing` `{strategy:"least_load"|"highest_balance"|"balanced",tenant_rpm:120,tenant_concurrency:10}`.
- POST `/api/admin/credits` `{tenant_id,amount_micros,reason,idempotency_key}` audited adjustment, positive only.
- POST `/api/billing/checkout` `{amount_cents:1000}` -> `{url}`; 503 if Stripe not configured.
- POST `/api/admin/refunds` `{order_id}` -> `{status}` full unspent-order refund only in initial release.
- POST `/api/payments/stripe/webhook` signed raw payload; no browser session required.
- Native SDK endpoints `/openai/v1/chat/completions`, `/openai/v1/responses`, `/anthropic/v1/messages`, `/anthropic/v1/messages/count_tokens`, and each provider's `/v1/models`. Access Key Bearer (Anthropic also x-api-key). Native traffic must not use cookie auth.

UI routes: `/` product entry, `/app` overview, `/app/keys`, `/app/models`, `/app/usage`, `/app/billing`; operators additionally `/app/upstreams`, `/app/proxies`, `/app/routing`, `/app/customers`. No invented traffic or finances. Empty database has actionable setup, no demo statistics.
