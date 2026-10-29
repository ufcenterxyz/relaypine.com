# RelayPine

**An open-source, self-hosted API gateway for Claude and OpenAI.**

[Website](https://relaypine.com) · [简体中文](README.zh-CN.md) · [Contributing](CONTRIBUTING.md) · [MIT license](LICENSE)

Route native API requests across multiple upstream keys, control their usage and budgets, and assign a separate SOCKS5 proxy to each key. RelayPine includes a Go gateway and a Next.js / HeroUI management console.

## What it does

- **Native protocols:** OpenAI Chat Completions and Responses; Anthropic Messages and token counting; JSON and SSE streaming.
- **Multiple upstreams:** official hosts by default, plus configurable compatible hosts and authentication modes.
- **Key discovery and batch import:** verify credentials, discover models, and import a selection with matched official reference prices. Workspaces and rate limits are imported when the provider exposes them. Discovery does not run paid inference.
- **Routing:** lowest load, highest remaining local budget, or a balanced strategy, with RPM, TPM, concurrency, and shared quota groups.
- **Per-key egress:** direct connections or an assigned SOCKS5 proxy. A failed proxy does not silently fall back to a direct connection.
- **Usage accounting:** integer micro-USD balances, price snapshots, reservations, settlement, and separate upstream cost budgets.
- **Request inspection:** each request's details show the client's request body and the response it received, with conversations, tool calls, streamed output, and inline images rendered. Bodies are encrypted at rest and kept for 7 days by default (`REQUEST_BODY_RETENTION_DAYS`; `0` stops recording and deletes stored bodies). Customers see their own workspace. The administrator sees every workspace and, for failed requests, the provider's reply that was withheld from the client.
- **Management UI:** English and Chinese, light and dark themes, email verification codes, isolated customer workspaces, and a separate administration domain.

Customers sign in with an email code at [relaypine.com](https://relaypine.com). The administrator uses [admin.relaypine.com](https://admin.relaypine.com) with the same verification flow. Each verified customer receives an isolated workspace with zero initial balance. Only the configured administrator can manage upstreams, prices, and audited balance credits. Team roles and online payment processing are not implemented.

## Quick start

Prerequisites: Go 1.26, Node.js 22.23.4 (`nvm install && nvm use` from the repository root), Yarn Classic, Python 3, and Docker Compose.

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

Open [localhost:4810](http://localhost:4810) for the customer console or [admin.localhost:4810](http://admin.localhost:4810) for administration. The latter requires the email configured in `SUPERADMIN_EMAIL`. Login requires the six-digit code delivered to that mailbox; there is no password or development-code bypass. Without Mailgun configuration, the services can start but cannot send login codes.

| Service | Local address |
| --- | --- |
| Customer console | `http://localhost:4810` |
| Administration | `http://admin.localhost:4810` |
| Gateway | `http://127.0.0.1:4811` |
| MySQL | `127.0.0.1:4812` |
| Redis | `127.0.0.1:4813` |

`SUPERADMIN_EMAIL` is required and validated at startup. It is not a list of administrators. Keep `.env` private and back up `MASTER_KEY` with your database: stored upstream secrets cannot be decrypted without it. Setup preserves an existing `.env`; supplying the email again only fills a missing administrator setting. See [.env.example](.env.example) for the remaining options.

## Configure the first route

1. **Upstreams:** choose the official OpenAI or Anthropic endpoint, or add a compatible host.
2. **Credentials:** paste the upstream API key and verify it. Save the detected workspace when needed. Proxy, capacity overrides, and budgets are under advanced settings.
3. **Models & rates:** choose **Import from upstream**, select a saved key and models, then review their reference prices. Exact official matches receive standard text prices for sales and upstream cost at 1:1. Unknown models and custom hosts need explicit pricing before activation. Existing model IDs are skipped without overwriting their configuration. Use **Update reference prices** to review and apply the latest prices to existing models.
4. **Routing:** select the routing policy and any shared quota groups.
5. Record an internal credit adjustment with a reason for the customer workspace, then create an access key in that workspace and call a priced model. Calls reserve and deduct the RelayPine balance using the captured model rates. New workspaces have no credit by default.

Published model lists are filtered by protocol, enabled state, and access-key permissions. An empty `/v1/models` response usually means there is no matching published model. No example model prices or spendable balances are seeded.

## Client connections

Keep your native SDK and replace its base URL and API key:

| Protocol | Local base URL | Supported endpoints |
| --- | --- | --- |
| OpenAI | `http://127.0.0.1:4811/openai/v1` | Chat Completions; Responses create, retrieve, delete; Models |
| Anthropic | `http://127.0.0.1:4811/anthropic` | Messages; Count tokens; Models |

Authenticate with your RelayPine `rp_live_…` access key using `Authorization: Bearer …`, or Anthropic's `x-api-key`. Upstream API keys remain on the server. Behind the hosted domain, the base URLs are `https://relaypine.com/openai/v1` and `https://relaypine.com/anthropic`.

RelayPine preserves each provider's protocol and does not convert between OpenAI and Anthropic. It supports text and client-executed function tools. Anthropic Messages and Count tokens also accept inline base64 PNG/JPEG images in user content, including a user message's `tool_result.content`. OpenAI image inputs, image URLs, file inputs/references, other image formats, audio, hosted tools, and background generation remain unsupported. A request is not automatically replayed after it has been sent upstream.

For a configured model that supports vision:

```ts
import Anthropic from "@anthropic-ai/sdk";
import { readFile } from "node:fs/promises";

const client = new Anthropic({
  apiKey: process.env.RELAYPINE_API_KEY,
  baseURL: "https://relaypine.com/anthropic",
});
await client.messages.create({
  model: "your-enabled-vision-model",
  max_tokens: 256,
  messages: [{ role: "user", content: [
    { type: "image", source: {
      type: "base64", media_type: "image/png",
      data: (await readFile("screenshot.png")).toString("base64"),
    } },
    { type: "text", text: "Describe this image." },
  ] }],
});
```

The entire JSON request, including base64 and text, must fit within **2 MiB**. Local image limits are 20 images per request, 8,000 pixels per axis, 16 million pixels per image, and 32 million pixels in total. All limits apply together. Only complete static PNG/JPEG files with a matching media type are accepted. Resize or crop before encoding; JPEG compression may help photographs fit, while PNG often suits text and diagrams. Base64 adds roughly one third to the binary size, so leave room for JSON and text. RelayPine forwards the image payload without resizing or recompressing it and does not write image data to request logs.

Anthropic workspace headers come from the saved upstream credential. Only explicitly supported `anthropic-beta` capabilities are accepted; unknown betas and `fallbacks` are rejected before forwarding. See [Anthropic compatibility](docs/anthropic-compatibility.md) and the [HTTP contract](contracts/http.md).

## Limits and accounting

Budgets are **local accounting limits**, not live provider balances. Credential discovery imports limits only when the API exposes them with the supplied key's permissions. Otherwise, it uses conservative local defaults of 60 RPM, 100,000 TPM, and 10 concurrent requests; these are not provider guarantees. Unknown keys on one endpoint share a default quota group until configured otherwise.

Enabled models must have metered pricing with positive input/output sales and cost rates. Reference pricing reads the providers’ current public price tables; the model-list APIs themselves do not expose these rates. The console shows the source and check time, previews changes, and applies selected updates with version checks. Official reference pricing is restricted to the matching official upstream; compatible hosts need explicit rates. A price is a reference, not an account-specific invoice or negotiated discount.

For image requests, the conservative input bound excludes only the logical base64 length of validated images from the existing text/body estimate, then adds `2 * ceil(width / 28) * ceil(height / 28) + 256` per image. The same bound feeds context checks, TPM admission, and monetary reservations; final charges use verified upstream usage. This does not reduce the 2 MiB HTTP limit. Explicit `count_tokens` requests are free of RelayPine monetary charges but still subject to access and capacity checks; the gateway does not automatically call that endpoint before generation.

Historical `usage_only` requests retain their original accounting snapshots. Migrating a model to metered pricing affects future requests only: it never rewrites old charges or gives a workspace credit. Import and price updates preserve access-key permissions and upstream budgets. Authentication, RPM, TPM, concurrency, output limits, and proxy rules still apply. Internal balance adjustments do not charge a payment method or add credit to an upstream provider. See [usage and budgets](docs/commercial-billing.md) for settlement behavior.

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
