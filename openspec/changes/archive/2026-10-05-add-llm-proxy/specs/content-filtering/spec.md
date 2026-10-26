# Spec Delta

## Purpose

Inspects outbound request payloads against user-defined regular expression rules and either blocks the request or transforms matched values before anything leaves the machine.

## ADDED Requirements

### Requirement: User-defined filter rules
Filters SHALL be defined in the JSON config, each with a unique name, a regular expression pattern following RE2 syntax, an action of `block` or `replace`, a replacement string (required for `replace`), and an enabled flag. Patterns SHALL match case-insensitively by default; a pattern MAY force case-sensitive matching by starting with the inline RE2 flag `(?-i)`.

#### Scenario: Filter round-trips through config
- **WHEN** the config contains an enabled filter with pattern `(?i)james brown`, action `replace`, and replacement `John Smith`
- **THEN** the filter is loaded and applied to subsequent requests

#### Scenario: Case-insensitive by default
- **WHEN** the config contains an enabled filter with pattern `james brown` and action `replace`
- **THEN** it matches message text `Ask James Brown` and rewrites it

#### Scenario: Case-sensitive opt-out
- **WHEN** a filter pattern starts with `(?-i)`
- **THEN** it matches case-sensitively

### Requirement: Scan JSON request bodies
Enabled filters SHALL be applied, in configured order, to every string value in the parsed JSON request body, including strings nested in objects and arrays such as message content, system prompts, and tool definitions.

#### Scenario: Message content is scanned
- **WHEN** a chat completion request contains `"content": "ping sk-live-abc123"` and a filter matching that key pattern is enabled
- **THEN** the filter matches the content string before the request is forwarded

#### Scenario: Nested fields are scanned
- **WHEN** a matched value appears inside a tool definition or another nested string field of the JSON body
- **THEN** the filter applies to it the same as to message content

### Requirement: Block matching requests
When an enabled `block` filter matches, the request SHALL NOT be forwarded. The client SHALL receive an HTTP 403 error response in the OpenAI error format that names the filter, and neither the response nor the traffic log entry SHALL contain the matched value.

#### Scenario: Secret blocked
- **WHEN** a request body contains a value matching an enabled block filter
- **THEN** OpenRouter receives no request and the client receives a 403 naming the filter

### Requirement: Replace matched values
When an enabled `replace` filter matches, each match SHALL be substituted with the filter's replacement string, which SHALL support regular expression capture-group references (e.g. `$1`, `${name}`).

#### Scenario: Literal replacement
- **WHEN** filter pattern `James Brown` with replacement `John Smith` matches message text `Ask James Brown`
- **THEN** the forwarded request contains `Ask John Smith`

#### Scenario: Masked replacement using capture groups
- **WHEN** filter pattern `(sk_\w{2}).*(\w{2})` with replacement `${1}**********${2}` matches `sk_xy1234abcd7890ab`
- **THEN** the forwarded request contains `sk_xy**********ab`

#### Scenario: Case-insensitive path rewrite
- **WHEN** filter pattern `(?i)/Users/james/` with replacement `/users/john.smith/` matches `/Users/james/xyz`
- **THEN** the forwarded request contains `/users/john.smith/xyz`

### Requirement: Apply filters sequentially with block precedence
`replace` filters SHALL be applied in configured order, each filter seeing the output of the previous one. A `block` match SHALL halt filter processing immediately and the request SHALL NOT be forwarded regardless of any other filter.

#### Scenario: Chained replacements
- **WHEN** filter A replaces `James` with `John` and filter B replaces `Brown` with `Smith`, in that order
- **THEN** the text `James Brown` becomes `John Smith` in the forwarded request

#### Scenario: Block short-circuits
- **WHEN** a request matches both a replace filter and a block filter
- **THEN** the request is not forwarded and no replacement is applied

### Requirement: Preserve JSON structure
Filtering SHALL preserve the JSON structure of the request body: the forwarded body SHALL parse as JSON with the same fields and non-string values as the original, with only matched substrings of string values changed.

#### Scenario: Structure preserved
- **WHEN** a replace filter rewrites part of a message content string in a request that also carries tools, temperature, and fields the proxy does not recognize (e.g. OpenRouter extensions such as `provider`)
- **THEN** the forwarded body retains all fields and their types with only the matched substring changed

#### Scenario: Number literals preserved
- **WHEN** a filter rewrites a string in a request whose body also contains large-integer or high-precision numeric values
- **THEN** those numbers are forwarded with their original literal text unchanged

### Requirement: Pass through non-JSON bodies unfiltered
Requests whose body is not JSON SHALL be forwarded without filtering, and their traffic log entry SHALL record that no content filtering was applied.

#### Scenario: Multipart audio transcription
- **WHEN** a client sends `POST /v1/audio/transcriptions` with a multipart body
- **THEN** the request is forwarded unmodified and the traffic entry is marked as not content-filtered
