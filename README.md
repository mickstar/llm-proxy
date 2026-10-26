# llm-proxy

A local, OpenAI-compatible checkpoint for LLM traffic. `llm-proxy` sits between
your LLM client tools and OpenRouter, so every outbound request passes through one
place where it can be inspected, redacted, or blocked before it leaves the machine.

- **Content filters** — user-defined RE2 regexes that `block` a request or `replace`
  matches (API keys, personal names, local file paths) before forwarding.
- **Reversible redactions** — replace PII with an alias (`john.smith@example.com` →
  `redactedk7f2ab@gmail.com`) before it leaves, then put the original back in the
  response, including inside streamed tokens.
- **Model policy** — `passthrough` (default) forwards any model id your client
  names; `allowlist` forwards only models you have added to the catalog. See
  [Model policy](#model-policy).
- **Halt on breach** — a single `block` match stops the proxy outright. Every later
  request gets 403 until you resume, so an agent cannot retry around the filter
  with an encoding trick.
- **Traffic log** — every exchange is recorded (filtered bodies only) and streamed
  live to the terminal UI.
- **Usage metrics** — prompt/completion token counts and provider-reported cost,
  all-time and per-model totals, rolling throughput, and a macOS app with a
  menu-bar control center and a full native console.
- **Control API** — a loopback HTTP API for config, filters, models, traffic, and
  metrics; the TUI, menu bar panel, and native console are clients of the same
  surface.

## Install

From the repository root:

```sh
go build -o llm-proxy ./cmd/llm-proxy
```

or install it on your `PATH`:

```sh
go install github.com/mickstar/llm-proxy/cmd/llm-proxy@latest
```

Requirements:

- **Go 1.27+** to build the proxy. It runs on any platform Go supports, minus the
  macOS-only features: keychain credential references, desktop notifications, and
  the LaunchAgent autostart below.
- **macOS 13+** and the Xcode command line tools to build the Swift menu bar app.
- [Task](https://taskfile.dev) for the `task` entry points and
  [mise](https://mise.jdx.dev) for the pinned linter. Both are convenience only:
  `go build`, `go test`, and `golangci-lint` work without them.

## Autostart (macOS)

```sh
task service:install     # build, install, and start the LaunchAgent
task reload              # rebuild and restart the running proxy
task service:status
task service:uninstall
```

`task reload` builds before it touches the running process, so a compile error
leaves the old binary serving; on success it restarts the agent and waits for the
control API to answer.

`service:install` builds the binary to `$(go env GOPATH)/bin/llm-proxy`, renders
`deploy/io.github.mickstar.llm-proxy.plist`, and bootstraps it into
`~/Library/LaunchAgents/`. The agent runs `llm-proxy serve` at login and keeps it
alive; output goes to `~/Library/Logs/llm-proxy.log`.

Because it is `serve`, running `llm-proxy` afterwards attaches the TUI to the same
process instead of starting a second server. Every terminal, every editor, and
every agent those sessions spawn shares that one proxy on `127.0.0.1:8787`.

One caveat: the halt state lives in memory, and `KeepAlive` restarts a dead proxy,
so a crash or restart clears a halt. If you want the breaker to survive restarts it
needs to persist to disk.

## Upstream endpoint and credential

The upstream endpoint and key are configuration, not code: `openrouter.base_url`
(default `https://openrouter.ai/api/v1`) and `openrouter.api_key`. Every proxied
request is rewritten to `{base_url}/<rest-of-/v1/path>` and sent with
`Authorization: Bearer {api_key}`; the client's own `Authorization` header is
always dropped. Every other request header — including `User-Agent`, `X-Title`,
and `HTTP-Referer` — is forwarded unchanged, and a client that sends no
`User-Agent` has none invented for it. `Accept-Encoding` is the one exception:
it is renegotiated so the proxy can decompress and inspect upstream bodies.
Any OpenAI-compatible endpoint works, including Fireworks at
`https://api.fireworks.ai/inference/v1`.

`openrouter.api_key` holds either a literal secret or a macOS keychain reference:

```json
{ "openrouter": { "api_key": "{keychain:FireworksAI/fireworks-api-key}" } }
```

A reference is read with `security find-generic-password` at request time and
cached for a minute, so the secret never lands in the config file, `GET
/api/config`, or `stats.json`, and a rotated item is picked up without a restart.
References are macOS-only; an unreadable or malformed reference is rejected on
load (when edited by hand) or with a 400 (`PUT /api/key`), and a reference that
was valid at startup but later fails answers proxied requests with a local 502
instead of forwarding an unauthenticated request upstream.

Set the credential without editing the file — both fields are optional, so a
key-only update never resets the base URL:

```sh
curl -X PUT http://127.0.0.1:8788/api/key \
  -H 'Content-Type: application/json' \
  -d '{"api_key":"{keychain:FireworksAI/fireworks-api-key}","base_url":"https://api.fireworks.ai/inference/v1"}'
```

The TUI and the macOS app's Settings pane read and write the same field, so a
reference shows up there verbatim and survives unrelated settings changes.

Switching `base_url` away from OpenRouter limits the catalog features: `llm-proxy
fetch`, the Models tab, and `GET /v1/models/{id}` read OpenRouter's
`/model/{author}/{slug}` metadata, which Fireworks does not serve. Proxying in
`passthrough` mode is unaffected.

Fireworks also publishes no pricing API — its rate card is a static table at
<https://docs.fireworks.ai/serverless/pricing> — so cost tracking against it needs
hand-entered `models[].pricing`. The `cache` field is the cached-input rate; leave
it out to bill cached prompt tokens at the prompt rate.

## Usage metrics and macOS app

Every proxied exchange reports token usage from the provider's `usage` object:
the JSON body for non-streaming responses, or the final `data:` chunk for
streams (OpenRouter emits one before `[DONE]`). The proxy aggregates that into:

- all-time totals and per-model totals, including cost in USD, persisted next to
  the config in `stats.json`. Cost is either the provider's `usage.cost` or, when
  the provider reports none (Fireworks, for example), an estimate from the
  catalog's per-token rates: non-cached prompt tokens at `pricing.prompt`, cached
  prompt tokens at `pricing.cache`, completion tokens at `pricing.completion`.
- completion-token throughput and request rate over a trailing 60-second window
- in-flight requests and per-minute activity
- prompt/completion split, error count, blocked count, and average cost per request

`GET /api/stats` serves the snapshot plus the halt state; the TUI shows the token
counts when inspecting a traffic entry.

The menu bar app polls that endpoint once a second. Its status-bar title shows
completion tokens/sec; clicking it opens a compact control-center panel rather
than a menu. The panel renders on macOS 26's Liquid Glass with a large throughput
readout, a 30-minute activity sparkline, token, cost, and request tiles, per-model
usage and cost bars, the last request's generation rate, and a **Resume** action
while halted.
It reads JSON without storing credentials.

The panel's **Expand** button opens the full native console, a regular macOS
window with a sidebar for Overview, Traffic, Filters, Models, Redactions, and
Settings. It is a complete replacement for the terminal UI:

- **Overview** — large throughput chart, token/cost/request tiles, per-model
  usage, and recent traffic.
- **Traffic** — live table streamed over SSE, with a detail inspector for request
  and response bodies, token counts, cost, filters, and redactions.
- **Filters** — native list/editor for filters and redactions, with block,
  replace, and redact actions, pattern or pattern-file sources, replacement
  suggestions, inline validation, and a debounced live test pane. **Redact**
  saves a reversible redaction rule instead of a filter. **Choose…** (or
  dropping a file) points a block rule at a key or secret file, which is matched
  verbatim against outbound requests; the file's contents never enter the
  config.
- **Models** — catalog list, OpenRouter metadata fetch, full editor for context,
  prompt/cache/completion pricing, supported parameters, and enablement.
- **Redactions** — recorded aliases with originals, rules, counts, and first-seen
  times.
- **Settings** — listener addresses, model policy, traffic capacity, base URL,
  and API key.

The app is assembled as `LLM Proxy.app`; `menubar:install` copies it to
`~/Applications` and starts it at login. While the console window is open the
app takes a Dock tile and Cmd-Tab entry; closing it returns to a menu-bar-only
agent.

```sh
task menubar:run         # build the .app and run it attached to the terminal
task menubar:install     # install to ~/Applications and start at login
task menubar:status
task menubar:uninstall
```

If the panel reports that `/api/stats` is missing, the running proxy predates the
metrics endpoint; run `task reload` (or restart `serve`) to load a current build.

The app finds the control API through `LLM_PROXY_CONTROL_URL`, then the
`controlURL` user default, then `server.control_addr` in the standard config
file, and finally `127.0.0.1:8788`.

Totals recorded before cost support was added are seeded once from the catalog's
per-token rates, so historical token counts still show an estimated cost. History
holds no cached-token split, so seeding bills every prompt token at the prompt
rate. All-time
totals survive restarts; the rolling windows and in-flight gauge reset when the
proxy restarts. Delete `stats.json` (in the config directory, by default
`~/Library/Application Support/llm-proxy/stats.json`) to reset history.

## Quick start

Start the server and open the TUI:

```sh
llm-proxy
```

Or run it headless (proxy + control API, no UI):

```sh
llm-proxy serve
```

That is enough to proxy: the default `passthrough` policy forwards any model id.
Optionally pin a model — required only under `allowlist`, and it is what the TUI's
Models tab and `/v1/models/{id}` show:

```sh
llm-proxy fetch deepseek/deepseek-v4.1-flash
```

Then point any OpenAI-compatible client at `http://127.0.0.1:8787/v1`:

```sh
export OPENAI_BASE_URL=http://127.0.0.1:8787/v1
export OPENAI_API_KEY=anything   # the proxy replaces this with your OpenRouter credential
```

The proxy maps `/v1/X` to `{base_url}/X` (default `https://openrouter.ai/api/v1`), so
`/v1/chat/completions` becomes `https://openrouter.ai/api/v1/chat/completions`.

## Configuration

Configuration is a single JSON file. The default location is
`llm-proxy/config.json` under your user config directory
(macOS: `~/Library/Application Support/llm-proxy/config.json`,
Linux: `~/.config/llm-proxy/config.json`). Override it with `--config <path>`.
On first run the file is created with safe defaults (loopback addresses, empty
catalog and filter list). Changes made by anything other than the running server
are reloaded live; writes are atomic.

```json
{
  "server": {
    "proxy_addr": "127.0.0.1:8787",
    "control_addr": "127.0.0.1:8788",
    "model_policy": "passthrough",
    "traffic_log_capacity": 500
  },
  "openrouter": {
    "base_url": "https://openrouter.ai/api/v1",
    "api_key": "sk-or-..."
  },
  "models": [
    {
      "id": "deepseek/deepseek-v4.1-flash",
      "name": "DeepSeek V4.1 Flash",
      "context_length": 163840,
      "pricing": { "prompt": "0.0000001", "cache": "0.000000002", "completion": "0.0000002" },
      "supported_parameters": ["temperature", "tools"],
      "enabled": true
    }
  ],
  "redactions": [
    {
      "name": "email",
      "pattern": "([\\w.+-]+)@([\\w.-]+)",
      "template": "redacted$R@$2",
      "enabled": true
    }
  ],
  "filters": [
    {
      "name": "redact-keys",
      "pattern": "(sk_\\w{2}).*(\\w{2})",
      "action": "replace",
      "replacement": "${1}**********${2}",
      "enabled": true
    }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `server.proxy_addr` | Listen address for the OpenAI-compatible proxy. |
| `server.control_addr` | Listen address for the control API (loopback only by default). |
| `server.model_policy` | `passthrough` (default) or `allowlist`. See [Model policy](#model-policy). |
| `server.traffic_log_capacity` | Number of traffic entries retained in memory. |
| `openrouter.base_url` | Upstream base URL that `/v1/X` is mapped onto. |
| `openrouter.api_key` | Credential injected as `Authorization: Bearer …` on every forwarded request. |
| `models[]` | Catalog entries: `id`, `name`, `context_length`, `supported_parameters`, `enabled`. Only `enabled` entries are served and forwardable. |
| `models[].pricing` | Per-token USD rates as strings: `prompt`, optional `cache` (cached input), `completion`. Used to estimate cost when the provider reports none; `cache` defaults to the prompt rate. |
| `redactions[]` | Reversible substitution rules: `name`, `pattern` (RE2), `template` (literal text plus `$R` and `$0`–`$9`), `enabled`. See [Redactions](#redactions). |
| `filters[]` | Ordered filter rules: `name`, exactly one of `pattern` (RE2, case-insensitive by default; prefix `(?-i)` for case-sensitive) or `pattern_file` (path/glob whose files' verbatim contents are matched literally and case-sensitively), `action` (`block`\|`replace`), `replacement` (required for `replace`; supports `$1`, `${name}`), `enabled`. |

Configuration is validated on load and before every save: patterns must compile,
every `pattern_file` must resolve to at least one readable, non-empty file of at
most 1 MiB, actions must be `block` or `replace`, `replace` filters must define a
replacement, and model prices must be non-negative finite numbers when set. Invalid changes are rejected with an error naming the file
and field, never the pattern text itself — a pattern can hold a literal
credential, and the error reaches both the log file and API responses.

## Model policy

`server.model_policy` decides whether the catalog gates traffic. It is live: change
it from the TUI Settings tab, with `PUT /api/settings`, or by editing the file.

| Policy | Model id on a body-carrying `/v1/*` request | `GET /v1/models` |
| --- | --- | --- |
| `passthrough` (default) | Forwarded whatever it is; OpenRouter decides | Proxied upstream, so clients see the real catalog |
| `allowlist` | Forwarded only if present and enabled in the catalog, otherwise local HTTP 404 | Answered locally from the catalog |

Body-carrying endpoints are `/v1/chat/completions`, `/v1/completions`,
`/v1/embeddings`, and `/v1/responses`. `GET /v1/models/{id}` always answers from the
local catalog under either policy, because OpenRouter exposes no per-model route at
that path.

Under `passthrough` the catalog is a set of pins, not a gate: `llm-proxy fetch` and
the Models tab still populate the TUI and the detail endpoint, but a model does not
have to be pinned to be usable. Filters apply identically in both modes, and every
forwarded exchange is logged either way.

Choose `allowlist` to bound spend to models you have vetted; a request naming
anything else fails locally without an upstream call.

## Filters

Filters are applied, in configured order, to **every string value** of a JSON request
body (message content, system prompts, tool definitions — never keys or non-string
values). `replace` filters see each other's output; a `block` match halts processing
immediately and the request is never forwarded — and it halts the proxy itself, see
[Halt on breach](#halt-on-breach). Patterns are matched **case-insensitively by
default**; start a pattern with the inline `(?-i)` flag to force case-sensitive
matching.

**Mask an API key** (capture groups):

```json
{ "name": "mask-key", "pattern": "(sk_\\w{2}).*(\\w{2})", "action": "replace", "replacement": "${1}**********${2}", "enabled": true }
```

`sk_xy1234abcd7890ab` → `sk_xy**********ab`

**Reversible replacement** — put `$R` in the replacement and the alias is restored
in responses, exactly like a redaction rule:

```json
{ "name": "email", "pattern": "[\\w.+-]+@[\\w.-]+", "action": "replace", "replacement": "redacted$R@gmail.com", "enabled": true }
```

`john@gmail.com` goes out as `redacted7qybez@gmail.com` and comes back as
`john@gmail.com`. The TUI fills this in for you: in the filter editor, an empty
replacement is prefilled with `redacted$R@gmail.com` when the pattern contains `@`,
or `[REDACTED-$R]` otherwise. `ctrl+g` regenerates it after you change the pattern.

The native console's Action picker also offers **Redact**, which saves the rule
in `redactions` instead of `filters`. It is the same reversible substitution with
two differences: the console calls the replacement a **template**, and it may be
a fixed string with no `$R` (`/Users/jane.doe` → `/Users/john.smith`), which the
proxy restores exactly. A plain **Replace** filter is one-way unless its
replacement contains `$R`; its literal matches are not recorded in the alias
table. Switching an existing rule between Replace and Redact moves it between
the two config sections.

**Swap a personal name** (matches any case):

```json
{ "name": "pii", "pattern": "james brown", "action": "replace", "replacement": "John Smith", "enabled": true }
```

`Ask James Brown` → `Ask John Smith`

**Rewrite a local path:**

```json
{ "name": "paths", "pattern": "/Users/james/", "action": "replace", "replacement": "/users/john.smith/", "enabled": true }
```

`/Users/james/xyz` → `/users/john.smith/xyz`

**Block a live secret** (request never reaches OpenRouter; client gets HTTP 403):

```json
{ "name": "secret", "pattern": "sk-live-\\w+", "action": "block", "enabled": true }
```

**Block a private key file, without copying it** — `pattern_file` matches the file's
current contents verbatim, so the key itself never appears in the config:

```json
{ "name": "ssh-keys", "pattern_file": "~/.ssh/*.pem", "action": "block", "enabled": true }
```

`~` expands to your home directory, and `*`, `?`, `[` make the spec a glob whose
every matching regular file contributes its contents. A request containing any of
those files is never forwarded; the client gets HTTP 403.

In the console, **Choose…** (or dropping a file onto the field) fills
`pattern_file` with the selected path. The rule then blocks any request whose
body contains that file's contents; the key itself is never read into the
config, copied, or shown.

Notes:

- `pattern_file` contents are matched literally (regex metacharacters are escaped,
  no capture groups) and **case-sensitively** — `verbatim` means exact. Line endings
  are normalized, so LF and CRLF bodies both match, and the file's trailing newline
  need not be present in the request.
- Files are read when the config is loaded or changed; a key rotated in place is
  picked up on the next config save or restart.
- A `pattern_file` that matches nothing, is unreadable, empty, or larger than 1 MiB
  is a validation error rather than a silently inert filter. Only the path is ever
  written to the config or shown in the TUI.
- Requests whose body is not JSON (e.g. `multipart/form-data` transcription calls)
  are forwarded unmodified and marked **not content-filtered** in the traffic log.
- Values matched by `block` or `replace` filters are never stored — the log keeps
  the filtered body, and blocked requests store no body at all.
- Untouched bodies are forwarded byte-for-byte; a body is only re-encoded when a
  filter actually changed a string (number literals stay exact).

## Redactions

A redaction replaces matches in outbound request bodies with an alias and maps the
alias back to the original everywhere it appears in the response. It is the same
mechanism as a `replace` filter whose replacement contains `$R`; a redaction rule
just runs after the filter pass. Either kind populates the Redactions tab. The agent never
sees your email, home directory, or key; you still get the real value back.

```json
"redactions": [
  { "name": "email", "pattern": "([\\w.+-]+)@([\\w.-]+)", "template": "redacted$R@$2", "enabled": true },
  { "name": "home",   "pattern": "/Users/jane.doe/",           "template": "/Users/john.smith/", "enabled": true },
  { "name": "keys",   "pattern": "sk-live-\\w+",              "template": "[REDACTED-$R]", "enabled": true }
]
```

| Template piece | Meaning |
| --- | --- |
| `$R` | A six-character token derived from the matched value and a per-process salt. The same value always gets the same token. |
| `$0`–`$9` | Capture groups from `pattern`, as in the existing `replace` action. |

| Original | Alias sent upstream |
| --- | --- |
| `john.smith@example.com` | `redactedk7f2ab@gmail.com` |
| `/Users/jane.doe/notes/todo.md` | `/Users/john.smith/notes/todo.md` |
| `sk-live-abc123` | `[REDACTED-m4x2qp]` |

Behaviour details:

- Redactions run **after** the filter pass, so a `block` rule still sees the
  original text and a blocked request never allocates an alias.
- The proxy negotiates compression itself and decompresses upstream bodies, so aliases are restored even when the client asks for gzip (many agent clients do), and the traffic log stays readable.
- Aliases are restored in `application/json` and `text/event-stream` responses.
  Providers stream a reply one token at a time, so an alias usually arrives split
  across the JSON strings of successive deltas; those fragments are rejoined per
  JSON field before matching, so streaming stays incremental (one alias-length of
  delay at most) and every event the client receives is still valid JSON.
- The TUI's Traffic tab keeps the **aliased** body. Originals never reach the
  traffic log or the config file.
- Mappings live in memory only, capped at 10,000 aliases per process. Restarting
  the server clears them and the aliases change.
- A template without `$R` is a fixed substitution; if two different values ever
  map to the same alias, the first original is the one restored and the entry is
  flagged with `!` in the TUI.
- Values already present in the alias table are left alone, so a client echoing an
  alias back does not get it re-redacted.

The **Redactions** tab lists every alias, its original, the rule that produced it,
and how many times it was used. It refreshes when new traffic arrives.

## Halt on breach

A `block` filter match is treated as a breach, not just a rejected request. The
proxy enters a halted state and refuses **everything** on the proxy port with
`403` and code `proxy_halted`, including `GET /v1/models`. OpenRouter is not
contacted again, so re-encoding, chunking, or splitting the secret across requests
cannot get traffic through.

Resume one of three ways:

```sh
llm-proxy resume            # talks to the running control API
```

```sh
curl -X POST http://127.0.0.1:8788/api/resume
```

or press `R` in the TUI, which shows a red banner naming the filter and how many
requests have been refused.

```json
// GET /api/halt
{"halted": true, "halted_at": "2026-10-24T15:31:02Z", "halted_filter": "secret", "refused": 12}
```

Details:

- On macOS a Notification Center banner is posted the moment the proxy halts,
  naming the filter and never the matched value. It goes through `osascript`, so
  there is nothing to install; the first banner may ask to allow notifications
  for Script Editor.
- The control API stays up while the proxy is halted — it is the way back.
- The first breach wins: a second block while halted does not overwrite the filter
  or the timestamp.
- Requests refused while halted are **not** written to the traffic log; they are
  counted in the halt state instead. A retry loop therefore cannot flush the
  evidence of the original breach out of the ring buffer.
- The state is process-local. Restarting the server resumes traffic, so a halt is
  not a permanent poison pill.
- The triggering request is still recorded as a normal blocked entry, naming the
  filter but never the matched value.

## Fetching models

`fetch` pulls a model's metadata (context length, pricing, supported parameters)
from OpenRouter and adds or updates it in the config. It fills `pricing.prompt` and
`pricing.completion` only; set `pricing.cache` by hand when the provider discounts
cached input. Under the default
`passthrough` policy this is optional — it pins the model so it shows up in the TUI
and `/v1/models/{id}`, and it is required only when `model_policy` is `allowlist`.
When a server is running `fetch` routes the change through the control API so there
is a single writer; otherwise it loads, mutates, and saves the config file directly.

```sh
llm-proxy fetch deepseek/deepseek-v4.1-flash
llm-proxy --config /path/to/config.json fetch openai/gpt-4:free
```

The id must be `author/slug[:variant]` (split on the first `/`; a `:free`-style
suffix is preserved). Fetched models are usable immediately, without a restart.

## Terminal UI

`llm-proxy` with no subcommand probes the control address and attaches to a running
server, or starts one in-process and then opens the TUI. The TUI talks to the
control API over HTTP only.

| Scope | Keys |
| --- | --- |
| Global (view mode) | `q` / `ctrl+c` quit; `tab` / `shift+tab` next/previous tab; `1`–`5` jump to Traffic / Redactions / Filters / Models / Settings; `R` resume a halted proxy |
| Traffic | `↑`/`↓` move; `enter` inspect an entry; `r` refresh |
| Traffic detail | `↑`/`↓` / `pgup` / `pgdn` scroll; `esc` / `q` close |
| Redactions | `↑`/`↓` move; `r` refresh |
| Filters | `n` new; `e` / `enter` edit; `d` delete; `space` toggle enabled; `r` reload |
| Filters editor | `tab` / `shift+tab` cycle fields; `ctrl+a` toggle block/replace; `ctrl+g` suggest replacement; `ctrl+e` toggle enabled; `ctrl+s` save; `esc` cancel |
| Models | `a` add by id (fetches metadata); `e` / `enter` edit; `d` delete; `space` toggle enabled; `r` reload |
| Models editor | `tab` / `shift+tab` cycle fields; `ctrl+e` toggle enabled; `ctrl+s` save; `esc` cancel |
| Settings | `e` / `enter` edit; `r` reload; `tab` / `shift+tab` cycle fields; `space` toggle model policy; `ctrl+s` save; `esc` cancel |

While a form is focused, only `ctrl+c` is global, so typed characters reach the
fields; `esc` returns to the list and re-enables tab switching. The Filters editor
includes a live test pane that re-runs the pattern against sample text (via the
control API) as you type, without saving.

## Control API

The control API is a small REST + SSE surface bound to a loopback address by
default. It is deliberately frontend-agnostic: the TUI and the macOS app use the
same endpoints.

| Method & path | Purpose |
| --- | --- |
| `GET /api/health` | Liveness; includes `config_error` when the on-disk config is invalid. |
| `GET /api/stats` | All-time and per-model token and cost totals, 60-second throughput, activity buckets, in-flight requests, last request, and halt state. |
| `GET /api/config` | Full current configuration. |
| `PUT /api/settings` | Update `server` and/or `openrouter` settings (partial). |
| `GET` / `POST /api/filters` | List / create filters. |
| `PUT` / `DELETE /api/filters/{name}` | Update / delete a filter. |
| `POST /api/filters/test` | Apply a `pattern` or `pattern_file` plus optional `replacement` to sample text without saving. |
| `GET` / `POST /api/models` | List / create catalog entries. |
| `PUT` / `DELETE /api/models/{id}` | Update / delete a catalog entry (`{id}` may contain `/`). |
| `POST /api/models/fetch` | Fetch metadata for `{"id":"author/slug"}` and upsert it. |
| `GET /api/redactions` | Every recorded alias with its original, rule, use count, and first-seen time. |
| `POST /api/redactions/rules` | Create a redaction rule (`name`, `pattern`, `template`, `enabled`). |
| `PUT` / `DELETE /api/redactions/rules/{name}` | Update / delete a redaction rule. |
| `GET /api/halt` | Halted state, triggering filter, timestamp, and refused count. |
| `POST /api/resume` | Clear the halted state. |
| `GET /api/traffic?limit=N` | Recent entries, newest-first. |
| `GET /api/traffic/{id}` | One entry including request/response bodies. |
| `GET /api/traffic/stream` | Server-sent events, one `entry` event per recorded exchange. |

Errors are JSON: `{"error": "message"}` with a 4xx/5xx status.

## Security notes

- Both listeners bind to loopback (`127.0.0.1`) by default. The proxy port serves
  only the OpenAI surface and the local model listing; management lives on the
  control port.
- `GET /api/config` returns the OpenRouter API key in the clear, because the TUI
  settings form needs it and the listener is loopback-only. Do not expose the
  control port beyond the local machine.
- The control API refuses requests whose `Host` is not a loopback name or that
  carry an `Origin` header, so a web page that rebinds its own hostname to
  `127.0.0.1` cannot read the key or the redaction originals.
- The config file is written `0600` in a `0700` directory, because a literal
  `openrouter.api_key` is stored there. Prefer a keychain reference.
- `GET /api/redactions` returns redaction originals in the clear, exactly as
  `GET /api/config` returns the OpenRouter key. Both are loopback-only; do not
  expose the control port.
- Redaction mappings are never written to the config file or the traffic log, and
  a blocked request allocates no alias at all.
- A halt fails closed: the proxy answers 403 to everything until a human resumes
  it, and the control API that clears it is a separate loopback listener.
- Client-supplied `Authorization` headers are always replaced with the configured
  OpenRouter credential; client credentials are never forwarded upstream.

Report a vulnerability privately, as described in [SECURITY.md](SECURITY.md).

## Development

```sh
task build   # build the ./llm-proxy binary
task test    # go test ./...
task lint    # golangci-lint (pinned via mise)
task fmt     # gofmt -w .
task vet     # go vet ./...
task tidy    # go mod tidy
task clean   # remove build artifacts and the test cache
```

The macOS app is a Swift package under `menubar/`:

```sh
task menubar:test        # model, presentation, and control-client tests
task menubar:build       # build menubar/.build/LLM Proxy.app
task menubar:console     # build and open the native console
```

The linter is pinned in `mise.toml` (install tools with `mise install`); the Go
toolchain is expected on `PATH`.

Install the git hooks once per clone — this makes every commit run `task lint`
first and abort on a lint failure:

```sh
task hooks   # pre-commit install
```

## License

[MIT](LICENSE) © 2026 Michael Johnston
