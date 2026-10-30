# RelayPine

**An open-source, self-hosted API gateway for Claude and OpenAI.**

[Website](https://relaypine.com) · [简体中文](README.zh-CN.md) · [Contributing](CONTRIBUTING.md) · [MIT license](LICENSE)

Route native API requests across multiple upstream keys, control their usage and budgets, and assign a separate SOCKS5 proxy to each key. RelayPine includes a Go gateway and a Next.js / HeroUI management console.

## What it does

- **Native protocols:** OpenAI Chat Completions and Responses, with image inputs and the Responses `image_generation` tool, and the Images API (GPT Image 2 and 2.5); Anthropic Messages and token counting; JSON and SSE streaming.
- **Multiple upstreams:** official hosts by default, plus configurable compatible hosts and authentication modes.
- **Key discovery and batch import:** verify credentials, discover models, and import a selection with matched official reference prices. Workspaces and rate limits are imported when the provider exposes them. Discovery does not run paid inference.
- **Routing:** lowest load, highest remaining local budget, or a balanced strategy, with RPM, TPM, concurrency, and shared quota groups.
- **Backup routes:** when every official route for a model is failing, rejecting its key, rate-limited, or out of budget, Chat Completions and Messages fail over to OpenRouter or ZenMux under the same model name and customer price.
- **Per-key egress:** direct connections or an assigned SOCKS5 proxy. A failed proxy does not silently fall back to a direct connection.
- **Usage accounting:** integer micro-USD balances, price snapshots, reservations, settlement, and separate upstream cost budgets.
- **Request inspection:** each request's details show the client's request body and the response it received, with conversations, tool calls, streamed output, and inline images rendered. Image generation calls and edits are stored with each image replaced by its size, so generated images never fill the database. Bodies are encrypted at rest and kept indefinitely by default; `REQUEST_BODY_RETENTION_DAYS` deletes them after a number of days, and `REQUEST_BODY_CAPTURE=false` stops recording. They are stored in MySQL, or in S3-compatible object storage (COS, S3, R2, MinIO) when `OBJECT_STORAGE_*` is set, where a bucket lifecycle rule moves old bodies to cheaper storage. Customers see their own workspace. The administrator sees every workspace and, for failed requests, the provider's reply that was withheld from the client.
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
3. **Models & rates:** choose **Import from upstream**, select a saved key and models, then review their reference prices. Exact official matches receive the standard prices, and any higher price for long prompts, for sales and upstream cost at 1:1. Unknown models and custom hosts need explicit pricing before activation. Existing model IDs are skipped without overwriting their configuration. Use **Update reference prices** to review and apply the latest prices to existing models.
4. **Routing:** select the routing policy and any shared quota groups.
5. **Backup routes (optional):** under Upstreams, add an OpenRouter or ZenMux key. **Check key** confirms the key without paid inference and lists which enabled models the provider offers under their official names. Backups are tried only after every official route, in the order you set. OpenRouter shows the account balance with the API key itself; ZenMux needs its optional Management API key to show the balance. An OpenRouter key with its own credit cap also shows what that cap still allows.
6. Record an internal credit adjustment with a reason for the customer workspace, then create an access key in that workspace and call a priced model. Calls reserve and deduct the RelayPine balance using the captured model rates. New workspaces have no credit by default.

Published model lists are filtered by protocol, enabled state, and access-key permissions. An empty `/v1/models` response usually means there is no matching published model. No example model prices or spendable balances are seeded.

## Client connections

Keep your native SDK and replace its base URL and API key:

| Protocol | Local base URL | Supported endpoints |
| --- | --- | --- |
| OpenAI | `http://127.0.0.1:4811/openai/v1` | Chat Completions; Responses create, retrieve, delete; Images generate and edit; Models |
| Anthropic | `http://127.0.0.1:4811/anthropic` | Messages; Count tokens; Models |

Authenticate with your RelayPine `rp_live_…` access key using `Authorization: Bearer …`, or Anthropic's `x-api-key`. Upstream API keys remain on the server. Behind the hosted domain, the base URLs are `https://relaypine.com/openai/v1` and `https://relaypine.com/anthropic`.

RelayPine preserves each provider's protocol and does not convert between OpenAI and Anthropic. It supports text and client-executed function tools. Anthropic Messages and Count tokens also accept inline base64 PNG/JPEG images in user content, including a user message's `tool_result.content`. Chat Completions and Responses accept OpenAI image inputs in user content, and Responses also in function and custom tool outputs, as https URLs or base64 PNG, JPEG, WebP or GIF data URLs; file IDs are refused. Each image first holds the most tokens it can cost on that model at its `detail` level (for example 3,001 on gpt-5.4 at `high`, and 36,001 at `auto` on GPT-6 Astra or on any model OpenAI has not documented), then settles at the input tokens OpenAI reports. Use `detail: "high"` or `"low"` to keep the hold small when sending many images. File inputs/references, audio, hosted tools other than Responses image generation, and background generation remain unsupported. A request moves to another route only after an upstream refused it before producing output: the request never left, or the upstream answered HTTP 401, 402, 403, 429, or 5xx. Each request tries at most three routes, official ones first. A request whose outcome is unknown, such as a connection that drops after the request was sent, is never replayed. Responses API calls and token counting never use a backup, because another provider cannot continue their stored state.

Image generation uses the OpenAI Images API with GPT Image 2 or 2.5 models (`gpt-image-2`, `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare`):

```ts
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "https://relaypine.com/openai/v1", apiKey: process.env.RELAYPINE_API_KEY });
const result = await client.images.generate({
  model: "gpt-image-2",
  prompt: "A lighthouse at dusk, watercolor",
  size: "1024x1024",
  quality: "medium",
});
const png = Buffer.from(result.data![0].b64_json!, "base64");
```

Responses can also offer the `image_generation` tool (`tools: [{"type": "image_generation", "quality": "medium"}]`). The tool's image model (default `gpt-image-2`) is billed at its own rates on top of the text model's tokens, from OpenAI's `tool_usage`. Because OpenAI may omit or undercount that report, each generated image is billed at least at OpenAI's calculator value for its size and quality, and at its worst case when no report arrives. `max_tool_calls` (default 1, at most 4) bounds the images per response.

Image requests are billed per token from the usage OpenAI reports: text input, input images and generated image tokens at their published rates. Each request first holds the worst case for its size, quality and image count, and settles at the actual amount. Edits accept uploaded PNG, JPEG or WebP images and base64 data URLs, not file IDs or remote URLs.

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

The entire JSON request, including base64 and text, must fit within **2 MiB**. Local image limits are 20 images per request, 8,000 pixels per axis, 16 million pixels per image, and 32 million pixels in total. All limits apply together. Only complete static PNG/JPEG files with a matching media type are accepted. Resize or crop before encoding; JPEG compression may help photographs fit, while PNG often suits text and diagrams. Base64 adds roughly one third to the binary size, so leave room for JSON and text. RelayPine forwards the image payload without resizing or recompressing it. Image data never appears in operational logs; like the rest of the request body, it is kept encrypted for request inspection.

Anthropic workspace headers come from the saved upstream credential. Only explicitly supported `anthropic-beta` capabilities are accepted; unknown betas and `fallbacks` are rejected before forwarding. See [Anthropic compatibility](docs/anthropic-compatibility.md) and the [HTTP contract](contracts/http.md).

## Limits and accounting

Budgets are **local accounting limits**, not live provider balances. Credential discovery imports limits only when the API exposes them with the supplied key's permissions. Otherwise, it uses conservative local defaults of 60 RPM, 100,000 TPM, and 10 concurrent requests; these are not provider guarantees. Unknown keys on one endpoint share a default quota group until configured otherwise.

**Rate limits.** A request is admitted to the current minute of its tenant, access key, credential, and quota-group buckets with a realistic token estimate: about one token per four bytes of text, each image's bound, and the output limit. A bucket admits while its minute is below its TPM, so one large request still runs on an idle key; later requests wait for the next minute or for settlement. When a request settles, its estimate becomes the tokens it used, and a route that refused it returns the whole estimate. When a provider answers 429 for one model's rate limit, only that model is paused on that key, for the Retry-After time. Other models keep using the key. A billing, quota or spend refusal pauses the whole key for at least five minutes. Tokens are counted the way each provider counts them. OpenAI counts every input token, cached or not, plus the output. Anthropic limits input (ITPM) and output (OTPM) separately: uncached input and cache writes count, while cache reads (except on Claude Haiku 3.5), output and `max_tokens` do not. An Anthropic key's TPM is therefore its ITPM, and its output limit is left to Anthropic. The local defaults only protect keys whose real limits are unknown. OpenAI and Anthropic state a key's per-minute limits on every response, and the Upstreams page shows the latest ones for each key and model next to its local limits. Set each credential's limits to its provider tier, especially for agents that send long contexts, or a 100,000 TPM key admits only about one such request at a time. Each workspace also has a requests-per-minute and a concurrency limit, shared by all its API keys and set on the Routing page. The defaults, 120 and 10, are low for agents that run many requests at once. A 429 says which limit refused the request: the workspace's, the API key's requests per minute, or upstream capacity (`All eligible upstream capacity is in use`). Its `Retry-After` says when that limit next has room.

**Output limits.** A request that names no output limit is sent with the model's configured output cap: `max_completion_tokens` for Chat Completions (`max_tokens` to a backup), `max_output_tokens` for Responses, and `max_tokens` for Messages. The cap also sizes the balance hold. OpenAI counts the output limit against its own rate limits, so high-volume clients should send a realistic limit.

**Long prompts.** Some models cost more when the prompt is long: OpenAI's gpt-6.x, gpt-5.6, gpt-5.5 and gpt-5.4 (not its mini and nano) above 272K input tokens, and Claude Haiku 5.5 above 100K tokens. Reference pricing imports these long context rates with the standard ones, and an operator can set them on a manually priced model. A request whose prompt, cache reads and writes included, is above the threshold is charged entirely at the long context rates, as the provider bills it, and the request log marks it. The balance hold uses those rates whenever the request's input bound is above the threshold. Every other model has one price for every prompt length it accepts. A model whose reference price was saved before long context rates were recorded refuses requests whose input bound exceeds its standard price range until **Update reference prices** is applied.

Enabled models must have metered pricing with positive input/output sales and cost rates. Reference pricing reads the providers’ current public price tables; the model-list APIs themselves do not expose these rates. The console shows the source and check time, previews changes, and applies selected updates with version checks. Official reference pricing is restricted to the matching official upstream among primary routes (a configured backup route can also carry the model); compatible hosts need explicit rates. A price is a reference, not an account-specific invoice or negotiated discount.

A request served by a backup is charged at the model's normal price. Its upstream cost is recorded at the model's upstream cost rates, an estimate of the provider's price; spending limits for a backup are set in the OpenRouter or ZenMux account. A refused attempt has no upstream cost, and the administrator's request log shows every route tried.

For image requests, the conservative input bound excludes only the logical base64 length of validated images from the existing text/body estimate, then adds `2 * ceil(width / 28) * ceil(height / 28) + 256` per image. The same image bound feeds monetary reservations, the long context price check of the hold, and TPM admission, where text counts about one token per four bytes instead of the conservative bound; final charges use verified upstream usage. This does not reduce the 2 MiB HTTP limit. Explicit `count_tokens` requests are free of RelayPine monetary charges but still subject to access and capacity checks; the gateway does not automatically call that endpoint before generation.

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
