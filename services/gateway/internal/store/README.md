# Durable state contract

`Open` requires a MySQL DSN and a base64-encoded 32-byte AES key. It creates the
embedded `schema.sql` under a MySQL advisory lock, selects UTC, and enables
`parseTime`. Tables use InnoDB. The only seeded records are official upstream
endpoint definitions and the routing defaults; there are no seeded users,
passwords, customer credits, API keys, or sale prices.

## Money

All amounts are signed integer micro-USD (`1 USD = 1,000,000`); `wallets.currency`
is `usd`. `balance_micros` includes active reservations; available funds equal
`balance_micros - held_micros`. A metered request reserves both the customer wallet and
credential cost budget atomically. Credentials with `budget_micros = 0` have no
local monetary cap; they still retain spend/hold accounting.

Models default to `billing_mode=metered`, including historical snapshots that
omit this field. New relay calls require metered prices and spendable customer
balance. Official reference imports and updates capture their source, verification
time, standard-price context boundary, and official upstream routing policy.
`UpdateModelPrices` atomically checks every expected model version and only updates
price fields, reference metadata and the disabling of unpriced entries. It never
changes historical requests, balances or ledger entries. Manual edits clear the
reference policy when rates change; `SaveManualModel` explicitly clears it even
when rates remain identical. Status-only edits preserve that policy.

Historical `billing_mode=usage_only` snapshots remain supported during settlement
and recovery. They require all eight rates to be zero, create no holds or charges,
and keep `cost_status=untracked`; later catalog repricing never rebills that history.
`Reserve` retains its positive credential-budget guard for durable historical
compatibility, while current API publication and relay admission reject enabled
usage-only models. Unknown reference models are saved disabled pending manual rates.

Money transactions lock a request row when applicable, then its wallet, then its
credential. Reserve/settle/recovery use `READ COMMITTED`; row locks and ledger
unique keys serialize accounting. Price configuration is snapshotted into each
request before upstream I/O, along with a durable attempt `send_intent`.

`ledger_entries` is an append-only journal for this implementation. Fields are
`id`, `tenant_id`, `kind`, `amount_micros`, `reference`, `reason`, `created_at`.
`UNIQUE(tenant_id, reference)` is the permanent business idempotency key. Credits
are positive and usage charges negative. Zero charge entries record metered
requests with a final zero charge; usage-only requests create no ledger entry.
This is not a full general ledger or separate cash/bonus lot system.

`CreditTx(ctx, tx, tenantID, amountMicros, reference, reason)` supports audited
internal credits in an existing transaction. It locks
the wallet and owns the credit journal insertion. Callers must not update the
same balance separately. Repeating the same reference and amount is a no-op;
changing the amount or kind returns `ErrConflict`.

## Settlement and recovery

The following monetary rules apply to metered requests, using the immutable
model snapshot taken at admission even if the model's billing mode changes:

- `not_sent` proves the upstream was never called and releases both holds.
- Known usage settles upstream cost once. Customer charges are capped at the
  original reservation; `error`/`failed` customer charges are zero.
- Unknown usage for a completed/canceled/unknown request retains both holds and
  shows `pending_usage`. Unknown usage for an error releases the customer hold,
  but keeps upstream cost held for reconciliation.
- `Recover` releases customer holds older than 24 hours at zero customer charge.
  It preserves unknown upstream cost holds. Trustworthy late usage may settle
  upstream costs but never back-charge an already finalized customer request.
- Final settlements are idempotent. A reservation with an existing request ID
  returns `ErrConflict` so the caller cannot accidentally send a second call.

Usage-only requests retain the same durable send intention and execution status.
Known token totals move them directly to `settled`; unknown totals remain in
`usage_only_pending` without any money holds. Recovery moves these records to
`usage_only_expired` after 24 hours and marks interrupted requests
`usage_unavailable`. Late trustworthy usage can fill missing totals and clear
the synthetic expiration error, but preserves recorded failures/cancellations
and never introduces a monetary charge. `not_sent` is known zero usage. Snapshot
responses expose `billing_mode` and `cost_status` (`untracked`, `pending`, or
`recorded`) so consumers do not mistake unpriced usage for free upstream calls.

## Reroutes and backup routes

`Reroute` moves an in-flight metered request to another credential after its
current attempt was refused before producing output. One transaction locks the
request, then both credentials in ID order. It requires the request to still be
`reserved`/`in_progress` on the stated credential with both holds active, closes
the current attempt as `failed_over` with its error code and zero cost, releases
that credential's upstream hold, reserves the next credential's budget, points
the request at it and records a new `send_intent` attempt (`<request>_<n>`). The
customer wallet hold and price snapshot are untouched, so settlement still
charges once. Settlement and recovery update only attempts that are not
`failed_over`. `attempts.error_code` is added to existing installations at
startup.

`SaveBackup` writes a vendor backup as one endpoint (`role=backup`, `vendor`, no
provider, preset address) and one credential (`backup-<vendor>`, quota group
`backup:<vendor>`) in a single transaction. `SaveEndpoint` and `SaveCredential`
refuse backup roles, `backup-` IDs and backup endpoints with `ErrBackupManaged`.
`Reserve` and `Reroute` accept a backup endpoint for either protocol.

`backup_management_keys` holds at most one optional vendor management key per
vendor, sealed with AES-GCM under `backup-management:<vendor>`. `SaveBackup`
replaces, keeps or deletes it in the same transaction and records
`management_key=true` on the backup endpoint while one exists. The key is used
only to read the vendor account balance and is never returned.

## Image models and usage

`models.config` may carry `kind:"image"` and `image_input_rate`,
`image_output_rate`, `cost_image_input_rate` and `cost_image_output_rate`. The
kind is derived from an OpenAI `upstream_model` in a supported GPT Image family
(`gpt-image-2`, `gpt-image-2.5-*`, and dated snapshots) on every save and every
read, never taken from a client, so rows saved before image support read
correctly. Image rates on any other model are rejected. An enabled image model
needs positive sales and cost text input, image input and image output rates;
its text output rate may be zero.

`requests.usage_json` may carry `image_input_tokens`, `image_output_tokens` and
`image_text_tokens`. For Images API calls the first two are the image model's
image tokens, and `input_tokens`/`output_tokens` its text tokens. For a
Responses request with the image generation tool, `image_text_tokens`,
`image_input_tokens` and `image_output_tokens` are the image model's tokens
next to the text model's usage. The relay computes the charge from both models'
rates; `Settle` rejects negative counts. The customer snapshot hides
`cost_image_*` rates like every other cost rate.

Request inspection stores image calls without their images. Images API request
bodies with uploads keep every field, with each image replaced by its type and
size. Replies and stream events keep every field, with each base64 image
replaced by a short note, and record the real size of the reply.

## Historical payment tables

`topup_orders`: `id VARCHAR(64) PRIMARY KEY`, `tenant_id VARCHAR(64)`,
`amount_micros BIGINT`, `currency VARCHAR(8)`, `status VARCHAR(32)`, nullable unique
`session_id VARCHAR(255)` and `payment_id VARCHAR(255)`, plus `created_at` and
`updated_at` (`DATETIME(6)`).

`payment_events`: `id VARCHAR(255) PRIMARY KEY`, `event_type VARCHAR(128)`,
`payload JSON`, `status VARCHAR(32)` default `pending`, `last_error TEXT NULL`,
`attempts INT` default zero, plus `created_at` and `updated_at`. These and any
existing supplemental refund tables are retained for historical data only.
There is no checkout, refund, webhook, payment worker, or Stripe integration.

## Security and verification

Historical password fields no longer authenticate. Email codes use a separate
HMAC-SHA-256 key derived from MASTER_KEY; Redis stores only the keyed digest and
challenge metadata. Session and customer API tokens use 256 bits from the OS
random source and are stored only as SHA-256 hashes. Credential secrets and
proxy URLs use AES-GCM with their configuration identity as associated data.
Only the configured administrator can obtain a snapshot. Secrets and token hashes
are always omitted.
Responses mappings are immutable and scoped to tenant and project.

Run `go test ./internal/store` for pure invariant/crypto tests. Set
`MYSQL_TEST_DSN` to run real MySQL integration tests; optionally set `MASTER_KEY`
to the development key. Use an isolated test database: tests provision a synthetic
administrator and remove their fixtures. Run packages sequentially (`-p 1`)
because bootstrap enforces one administrator per database. Never point tests at the
application database. `go test -race ./internal/store`
also exercises concurrent wallet reservations under the race detector.

## Verified users and administrative access

`SUPERADMIN_EMAIL` is required at startup and normalized to lowercase with
surrounding whitespace removed. Invalid or missing values fail before connecting
to MySQL. `store.Open` copies this identity into immutable instance state. Every
gateway replica sharing a database must configure the same administrator.
An email match is an authorization policy, never proof of mailbox ownership.

`EnsureSuperAdmin` provisions this identity without a usable password and demotes
other operators. It revokes legacy password credentials and former operators'
sessions/API keys, preserving their identities, tenants, money and history.
Verified ordinary customers and their sessions/API keys survive repeated startup.
The unusable `!email-otp` marker separates verified identities from legacy
password identities; it is not an authentication secret.

After consuming a successful OTP on the customer origin, `EmailUser` provisions
a new tenant, user, default project and zero-balance wallet in one transaction.
It never grants credit or creates an API key automatically. A previously
waitlisted or legacy email must verify ownership first; existing identities keep
their own historical tenant and balance. Restoring a legacy identity revokes its
old sessions and API keys before enabling verified access. Duplicate concurrent
sign-ins converge on the unique email identity. Lookup uses an exact normalized
email comparison so database collation cannot authenticate a different mailbox.

`CreateSurfaceSession` and `SurfaceSessionUser` use explicit `customer` or `admin`
audiences stored as `email_otp_customer` / `email_otp_admin` in `auth_method`.
Older `legacy` and `email_otp` sessions fail closed and require a fresh OTP.
Admin sessions also require the exact configured email and durable operator flag.
The gateway accepts only configured `APP_URL` / `ADMIN_APP_URL` hosts, checks the
matching Origin for writes, and sets host-only cookies. On the public surface,
even the administrator receives a customer audience and `operator=false`.

Snapshot and key methods recheck durable user/tenant ownership, active status and
mailbox-verification marker. Customer snapshots scope request, key, wallet and
ledger queries to the tenant and remove procurement prices, pricing-reference
metadata and operational resources. Relay keys authenticate for verified active
customers and remain bound to their tenant/project and model permissions.

Changing `SUPERADMIN_EMAIL` is an administrator change, not an account rename or
data transfer. A new address gets a new tenant, project, and zero-balance wallet;
an existing address retains its own historical tenant. Bootstrap revokes the
former administrator's sessions/keys and demotes its role, preserving its tenant,
balance, ledger and usage. Changing back never reactivates revoked keys. Stop old
replicas before changing the configured administrator to avoid policy conflicts.
The new owner must still verify a code delivered to its mailbox.
