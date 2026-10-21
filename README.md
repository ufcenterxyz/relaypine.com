# RelayPine

**An open-source, self-hosted API gateway for Claude and OpenAI.**

[Website](https://relaypine.com) · [简体中文](README.zh-CN.md) · [Contributing](CONTRIBUTING.md) · [MIT license](LICENSE)

Route native API requests across multiple upstream keys, control their usage and budgets, and assign a separate SOCKS5 proxy to each key. RelayPine includes a Go gateway and a Next.js / HeroUI management console.

## What it does

- **Native protocols:** OpenAI Chat Completions and Responses; Anthropic Messages and token counting; JSON and SSE streaming.
- **Multiple upstreams:** official hosts by default, plus configurable compatible hosts and authentication modes.
- **Key discovery and batch import:** verify credentials, discover models, and publish a selection in one operation without entering rates. Workspaces and rate limits are imported when the provider exposes them. Discovery does not run paid inference.
- **Routing:** lowest load, highest remaining local budget, or a balanced strategy, with RPM, TPM, concurrency, and shared quota groups.
- **Per-key egress:** direct connections or an assigned SOCKS5 proxy. A failed proxy does not silently fall back to a direct connection.
- **Usage accounting:** integer micro-USD balances, price snapshots, reservations, settlement, and separate upstream cost budgets.
- **Management UI:** English and Chinese, light and dark themes, email verification codes, and a waitlist.

The current release supports **one administrator per deployment**. Other verified email addresses join the waitlist without gaining console or API access. The hosted service at [relaypine.com](https://relaypine.com) uses the same admission model. Self-service customer onboarding, team roles, and payment processing are not implemented.

## Quick start

Prerequisites: Go 1.26, Node.js 20.9 or newer (CI uses Node.js 22), Yarn Classic, Python 3, and Docker Compose.

```sh
git clone https://github.com/ufcenterxyz/relaypine.com.git
cd relaypine.com
SUPERADMIN_EMAIL=you@example.com make setup
```

Setup creates local database credentials and an encryption key in `.env`. Configure these Mailgun values in that file using a verified sending domain:

```dotenv
MAILGUN_API_KEY=your-mailgun-api-key
MAILGUN_DOMAIN=mg.example.com
MAILGUN_FROM='RelayPine <login@mg.example.com>'
```

For a Mailgun EU domain, also set `MAILGUN_API_BASE=https://api.eu.mailgun.net`.

```sh
make infra
make gateway  # terminal 1
make console  # terminal 2
```

Open [localhost:4810](http://localhost:4810) and sign in with the email configured in `SUPERADMIN_EMAIL`. Login requires the six-digit code delivered to that mailbox; there is no password or development-code bypass. Without Mailgun configuration, the services can start but cannot send login codes.

| Service | Local address |
| --- | --- |
| Console | `http://localhost:4810` |
| Gateway | `http://127.0.0.1:4811` |
| MySQL | `127.0.0.1:4812` |
| Redis | `127.0.0.1:4813` |

`SUPERADMIN_EMAIL` is required and validated at startup. It is not a list of administrators. Keep `.env` private and back up `MASTER_KEY` with your database: stored upstream secrets cannot be decrypted without it. Setup preserves an existing `.env`; supplying the email again only fills a missing administrator setting. See [.env.example](.env.example) for the remaining options.

## Configure the first route

1. **Upstreams:** choose the official OpenAI or Anthropic endpoint, or add a compatible host.
2. **Credentials:** paste the upstream API key and verify it. Save the detected workspace when needed. Proxy, capacity overrides, and budgets are under advanced settings.
3. **Models & rates:** choose **Import from upstream**, select a saved key, then select the models to import. New models are enabled in **Usage only** mode without entering rates. Existing models are skipped without changing their configuration. Model discovery alone does not publish a model.
4. **Routing:** select the routing policy and any shared quota groups.
5. Create an access key and call an imported model. Usage-only requests do not require a RelayPine balance. For models using **Metered** pricing, enter the rates and record an internal credit adjustment with a reason before making a billable request. New workspaces have no credit by default.

Published model lists are filtered by protocol, enabled state, and access-key permissions. An empty `/v1/models` response usually means there is no matching published model. No example model prices or spendable balances are seeded.

## Client connections

Keep your native SDK and replace its base URL and API key:

| Protocol | Local base URL | Supported endpoints |
| --- | --- | --- |
| OpenAI | `http://127.0.0.1:4811/openai/v1` | Chat Completions; Responses create, retrieve, delete; Models |
| Anthropic | `http://127.0.0.1:4811/anthropic` | Messages; Count tokens; Models |

Authenticate with your RelayPine `rp_live_…` access key using `Authorization: Bearer …`, or Anthropic's `x-api-key`. Upstream API keys remain on the server. Behind the hosted domain, the base URLs are `https://relaypine.com/openai/v1` and `https://relaypine.com/anthropic`.

RelayPine preserves each provider's protocol and does not convert between OpenAI and Anthropic. The supported inference surface is text and client-executed function tools. Images, audio, files, hosted tools, and background generation are rejected where costs cannot be reliably accounted for. A request is not automatically replayed after it has been sent upstream.

Anthropic workspace headers come from the saved upstream credential. Only explicitly supported `anthropic-beta` capabilities are accepted; unknown betas and `fallbacks` are rejected before forwarding. See [Anthropic compatibility](docs/anthropic-compatibility.md) and the [HTTP contract](contracts/http.md).

## Limits and accounting

Budgets are **local accounting limits**, not live provider balances. Credential discovery imports limits only when the API exposes them with the supplied key's permissions. Otherwise, it uses conservative local defaults of 60 RPM, 100,000 TPM, and 10 concurrent requests; these are not provider guarantees. Unknown keys on one endpoint share a default quota group until configured otherwise.

Each model has a billing mode:

| Mode | Behavior |
| --- | --- |
| Usage only | Records requests and token usage without RelayPine charges, money reservations, or ledger entries. Upstream cost is **not priced**, rather than reported as free. The provider still bills your account normally. |
| Metered | Uses configured sales and upstream cost rates for balance reservations and settlement. Positive input/output rates are required to enable the model; disabled drafts may have zero rates. |

Batch import defaults to **Usage only**. You can switch an individual model to **Metered** and configure its rates later. Existing models retain their pricing, and in-flight requests retain the mode and rates captured when they started.

Usage-only models require an upstream credential without a local monetary budget (`budget_micros=0`), because an unknown cost cannot be checked against a dollar limit. Import does not remove existing budgets or change access-key permissions. Authentication, RPM, TPM, concurrency, output limits, and proxy rules still apply. Internal balance adjustments do not charge a payment method or add credit to an upstream provider. See [usage and budgets](docs/commercial-billing.md) for settlement behavior.

## Development

```sh
make check             # Go vet, TypeScript, ESLint
make test              # Go tests with the race detector
make test-integration  # dedicated local MySQL test schema and Redis
make build             # gateway binary and console production build
```

Integration tests run packages serially and use the separate `relaypine_test` schema. Do not point test variables at an application database. CI provisions disposable MySQL and Redis services. Tests use injected mail senders and mock upstreams.

```text
apps/console/       Next.js and HeroUI console and landing page
services/gateway/   Go relay, access control, scheduling, accounting
contracts/         HTTP API contract
docs/              Architecture, compatibility, and operations
k8s/               Maintainer deployment manifests
```

The manifests and tag-triggered deployment workflow target the maintainer's hosted service. Forks must supply their own infrastructure, domain, administrator email, registry, and secrets; the hosted deployment job is disabled outside this repository. See [deployment notes](k8s/README.md) and [operations](docs/operations.md). Architecture documents also include future design proposals; this README describes the implemented scope.

## Contributing and security

Bug reports and focused pull requests are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md). Report vulnerabilities privately through [GitHub security advisories](https://github.com/ufcenterxyz/relaypine.com/security/advisories/new), following [SECURITY.md](SECURITY.md).

RelayPine is distributed under the [MIT license](LICENSE).
