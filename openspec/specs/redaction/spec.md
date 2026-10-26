# redaction Specification

## Purpose

Replaces sensitive values in outbound request bodies with stable aliases and restores the originals in responses, so an agent can work with real paths, addresses, and credentials without ever receiving them.

## Requirements

### Requirement: Substitute sensitive values on the way out
Enabled redaction rules SHALL be applied, in configured order, to every string value of a JSON request body. Each match SHALL be replaced by the rule's template, with `$R` expanded to a per-match token and `$0`–`$9` to capture groups. Object keys and non-string values MUST NOT be rewritten. Redactions SHALL run after the content filters, so a blocked request allocates no alias. Text already present in the alias table MUST NOT be substituted again.

#### Scenario: Email replaced with a stable alias
- **WHEN** a rule matches `john.smith@example.com` and a request body contains that value
- **THEN** the forwarded body carries the template expansion instead, and the same value in a later request produces the same alias

#### Scenario: Blocked request allocates no alias
- **WHEN** a block filter matches and a redaction rule would also match the same text
- **THEN** the request is blocked and no alias is recorded

#### Scenario: Object keys untouched
- **WHEN** an object key and a string value both match a rule
- **THEN** only the value is replaced

### Requirement: Restore aliases in responses
The proxy SHALL replace recorded aliases with their originals in `application/json` and `text/event-stream` response bodies, escaping the replacement for the surrounding JSON string when necessary. An alias split across two stream chunks, or across the JSON string values of successive streamed deltas, SHALL be held back and rejoined until it resolves rather than emitted in pieces; rewritten event streams SHALL remain valid JSON. The proxy SHALL drop the client's `Accept-Encoding` so upstream bodies arrive decompressed and are rewritten regardless of what the client asked for; a body that is still content-encoded, or that has a non-text content type, SHALL pass through unmodified.

#### Scenario: Non-streaming response restored
- **WHEN** a response body contains a recorded alias
- **THEN** the client receives the original value in its place

#### Scenario: Alias restored for a gzip-capable client
- **WHEN** the client sends `Accept-Encoding: gzip` and the response contains a recorded alias
- **THEN** the client receives the original value, uncompressed, and the traffic log stores readable text

#### Scenario: Alias split across streamed chunks
- **WHEN** an SSE delta ends with `redacted4739` and the next begins `19@gmail.com`
- **THEN** the client receives the original value once, unsplit

#### Scenario: Alias split across streamed deltas
- **WHEN** a provider streams `redacted4739` in one delta's `content` string and `19@gmail.com` in the next
- **THEN** the fragments are rejoined before matching, and the client receives the original value in valid JSON events

### Requirement: Keep the alias table in memory
Alias mappings SHALL live in process memory only, bounded by a capacity, and MUST NOT be written to the config file or the traffic log. Restarting the server SHALL discard them. The traffic log SHALL record the aliased request and response bodies.

#### Scenario: Restart forgets aliases
- **WHEN** the server restarts
- **THEN** no alias from the previous run is listed and responses are not rewritten for it

#### Scenario: Log holds no originals
- **WHEN** a redacted exchange is recorded
- **THEN** the stored request and response bodies contain aliases, not originals

### Requirement: Report recorded aliases
The system SHALL expose every recorded alias with its original value, producing rule, use count, and first-seen time, and the TUI SHALL show them in a tab next to Traffic.

#### Scenario: Alias listed after a request
- **WHEN** a request is forwarded with one redaction applied
- **THEN** the alias is listed with its original value and a count of one
