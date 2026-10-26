# Proposal

## Why

Filtering a secret today means pasting the secret into `config.json` as a pattern,
which duplicates key material on disk, goes stale when the key is rotated, and is
easy to get subtly wrong (escaping, trailing newline, case). The motivating case
is refusing to let an SSH private key leave the machine: the user wants to point a
`block` filter at `~/.ssh/id_rsa` or `~/.ssh/*.pem` and have the file's current
contents matched verbatim, without copying them anywhere.

## What Changes

- Add an optional `pattern_file` field to a filter rule: a path, `~`-prefixed path,
  or glob (e.g. `~/.ssh/*.pem`) naming one or more files whose **verbatim contents**
  become the filter's match pattern.
- A filter must define exactly one of `pattern` (inline RE2) or `pattern_file`;
  setting both, or neither, is a validation error.
- File contents are read at compile/validation time, never persisted to config or
  written to any log; only the path is stored.
- File-derived patterns match literally (RE2 metacharacters escaped), case-sensitively,
  and with line endings normalized so CRLF/LF bodies both match and a trailing newline
  in the file is not required in the request.
- A glob that matches nothing, an unreadable file, an empty/whitespace-only file, or a
  file larger than 1 MiB is a validation error naming the filter and path — a filter
  that would silently stop protecting a key must instead fail closed.
- `POST /api/filters/test` accepts `pattern_file`; the TUI filter editor, table, and
  live test pane gain the field.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `configuration`: filter rules accept `pattern_file` as an alternative match source;
  validation requires exactly one of `pattern`/`pattern_file` and fails closed when the
  named files cannot be read.
- `content-filtering`: filters may derive a literal, case-sensitive pattern from the
  current contents of one or more files.
- `control-api`: the filter test endpoint accepts `pattern_file`.
- `tui`: the Filters editor and table expose `pattern_file`.

## Impact

- `internal/config`: `Filter` struct, `Validate`, new `CompileFilter`/file-literal
  helpers (glob and `~` expansion).
- `internal/filter`: `Compile` and `Test` compile through `CompileFilter`.
- `internal/control`: `POST /api/filters/test` request shape.
- `internal/tui`: filter model, client, table and editor rendering.
- `README.md`: filter configuration reference and examples.
- Config file format: additive optional field, backward compatible.
