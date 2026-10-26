# Security Policy

`llm-proxy` sits between local clients and an upstream LLM provider, holds the
upstream credential, and keeps reversible mappings of the PII it redacts. Reports
that break one of the guarantees below are treated as vulnerabilities.

## Supported versions

The latest commit on `main`. There are no tagged releases yet; fixes land on
`main` rather than in backports.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting:

<https://github.com/mickstar/llm-proxy/security/advisories/new>

(Security tab → "Report a vulnerability".) Do not open a public issue for a
suspected vulnerability.

Include the commit you tested, the config that reproduces it, the steps, and the
observed versus expected result. This is a single-maintainer project, so reports
are triaged on a best-effort basis rather than against a fixed SLA.

## In scope

- **Filter or redaction bypass** — original text reaching upstream despite a
  matching rule (encoding or escaping tricks, streamed bodies, a JSON body the
  filter decoder accepts but the provider reads differently), a `block` match
  forwarded instead of refused, or an alias restored into content the original
  never appeared in.
- **Halt bypass** — after a breach, a proxied request answered with anything
  other than 403 before a human resumes the proxy.
- **Credential or PII disclosure to a remote party** — reading
  `GET /api/config` or `GET /api/redactions` without local access, by defeating
  the control API's loopback `Host`/`Origin` checks (DNS rebinding, a page on a
  loopback origin, cross-origin reads), or a credential or redaction original
  appearing in the config file, `stats.json`, the traffic log, or argv.
- **File disclosure** — `pattern_file` reading outside the paths its glob is
  meant to cover.
- **Local-privilege regression** — the config written group/world-readable, a
  keychain reference resolving an item other than the one configured, or the
  keychain value reaching disk.
- **Injection through proxied data** — filter names, redaction originals, or
  upstream response content escaping their context into the macOS notification
  script, the TUI, or the menu bar app.

## Out of scope

- Anything that already requires code execution as the same local user: that
  user can read the config, `stats.json`, and the proxy's memory by design.
- Exposing the control port beyond loopback, or binding the listeners to a
  non-loopback address. Both are documented as unsupported.
- Provider-side logging, retention, or compromise, and anything the provider
  does with traffic that was legitimately redacted before forwarding.
- Denial of service from a local process against the loopback ports.
- `model_policy: "passthrough"` forwarding whatever model id a client names, and
  responses being passed through for alias restoration without filter scanning.
  Both are the documented behavior, not a bypass.
- A request whose body is not valid JSON and whose `Content-Type` is not a JSON
  type is forwarded unchanged: filters and redactions only parse JSON bodies.
  The traffic log marks such entries "not content-filtered".
- Missing macOS notarization, hardened runtime, or code-signing identity in the
  menu bar app.

## Design boundaries

Loopback is the trust boundary: both listeners bind `127.0.0.1`, management lives
on the control port, and the control API rejects non-loopback `Host` values and
any request carrying an `Origin` header. The config file is written `0600` in a
`0700` directory; a keychain reference (`{keychain:service/account}`) keeps a
literal credential out of it. Filters apply to every string value of a JSON
request body — never to keys or non-string values — and redaction mappings are
held in memory only, never persisted with the traffic log. Validation errors name
the filter and the failure class but never repeat the pattern text, so a literal
credential in an inline pattern cannot reach the log file or an API response
through a compile error.

Inline patterns are still stored, and served by `GET /api/config`, in the clear:
treat `config.json` as a secret store, and prefer `pattern_file` for anything
that must not appear in it.
