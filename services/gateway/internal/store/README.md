# Durable state contract

`Open` requires a MySQL DSN and a base64-encoded 32-byte AES key. It creates the
embedded `schema.sql` under a MySQL advisory lock, selects UTC, and enables
`parseTime`. Tables use InnoDB. The only seeded records are official upstream
endpoint definitions and the routing defaults; there are no seeded users,
passwords, customer credits, API keys, or sale prices.

## Money

All amounts are signed integer micro-USD (`1 USD = 1,000,000`); `wallets.currency`
is `usd`. `balance_micros` includes active reservations; available funds equal
`balance_micros - held_micros`. A request reserves both the customer wallet and
credential cost budget atomically. Credentials with `budget_micros = 0` have no
local monetary cap; they still retain spend/hold accounting.

Money transactions lock a request row when applicable, then its wallet, then its
credential. Reserve/settle/recovery use `READ COMMITTED`; row locks and ledger
unique keys serialize accounting. Price configuration is snapshotted into each
request before upstream I/O, along with a durable attempt `send_intent`.

`ledger_entries` is an append-only journal for this implementation. Fields are
`id`, `tenant_id`, `kind`, `amount_micros`, `reference`, `reason`, `created_at`.
`UNIQUE(tenant_id, reference)` is the permanent business idempotency key. Credits
are positive and usage charges negative. Zero charge entries record final free
requests. This is not a full general ledger or separate cash/bonus lot system.

`CreditTx(ctx, tx, tenantID, amountMicros, reference, reason)` supports audited
internal credits in an existing transaction. It locks
the wallet and owns the credit journal insertion. Callers must not update the
same balance separately. Repeating the same reference and amount is a no-op;
changing the amount or kind returns `ErrConflict`.

## Settlement and recovery

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
Only the fixed administrator can obtain a snapshot. Secrets and token hashes
are always omitted.
Responses mappings are immutable and scoped to tenant and project.

Run `go test ./internal/store` for pure invariant/crypto tests. Set
`MYSQL_TEST_DSN` to run real MySQL integration tests; optionally set `MASTER_KEY`
to the development key. Use an isolated test database: tests provision the fixed
administrator and remove their fixtures. Run packages sequentially (`-p 1`)
because the administrator address is globally unique. Never point tests at the
application database. `go test -race ./internal/store`
also exercises concurrent wallet reservations under the race detector.

## Private access and waitlist

`store.SuperAdminEmail` is fixed to `ufcenterxyz@gmail.com`; an email match alone
never authenticates a user. `EnsureSuperAdmin` provisions this identity without a
password, demotes other operators, retires other accounts' sessions and keys,
and backfills existing users into the waitlist. Migrating an administrator from
password authentication or promoting an unverified public account replaces its
password hash with an unusable `!email-otp` marker and revokes its sessions and
API keys. Repeated bootstrap preserves sessions and keys created after migration.

`SessionUser` requires `auth_method=email_otp`; old sessions default to `legacy`
through an additive, serialized schema migration. The only public authentication
path consumes a valid email challenge before `EmailAdministrator` and
`CreateSession` are called. Login, snapshots, key creation and relay key
authentication additionally require the exact administrator identity and its
operator flag in durable state. Only a verified ordinary email challenge can
call `JoinWaitlist` through the API. It writes a normalized, unique email,
optional name and original submission time, never a tenant, user, wallet,
session or key. Historical accounts, balances and usage remain intact.
