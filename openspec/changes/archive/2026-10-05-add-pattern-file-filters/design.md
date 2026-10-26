# Design

## Context

Filters compile once into an immutable snapshot (`filter.Set`) and are matched
per request against every string value of a JSON body. The pattern is stored in
`config.json` as RE2 text. Secrets are the common thing users filter, so storing
them inline is the weak point.

## Goals / Non-Goals

- Goal: a filter whose match text comes from files on disk and is never stored in config.
- Goal: fail closed — a filter that cannot resolve its file must not silently stop matching.
- Non-goal: reloading files independently of config refreshes (files are read at compile time).
- Non-goal: partial/fuzzy matching; the semantics stay "the file's text appears in the body".

## Decisions

### D1: `pattern_file` field, exactly one match source

`config.Filter` gains `PatternFile string` (`pattern_file`). Validation requires
exactly one of `pattern`/`pattern_file`. Additive and backward compatible.

### D2: Spec resolution — `~` expansion plus glob

A spec is `~`-expanded against `os.UserHomeDir()` (leading `~/` or bare `~`).
If it contains any of `*?[` it is expanded with `filepath.Glob`; otherwise it is
`os.Stat`ed directly. Matches are sorted for determinism. Directories are skipped
(`~/.ssh/*` must not break on a subdirectory); zero remaining regular files, or a
glob syntax error, is an error naming the filter and spec.

### D3: Literal generation

For each file: reject > 1 MiB (a glob should not turn a huge file into a pattern);
normalize `\r\n` and lone `\r` to `\n`; trim trailing newlines; reject empty or
whitespace-only content; `regexp.QuoteMeta` each line and join lines with `\r?\n`.
Multiple files become one non-capturing alternation. Result: metacharacters are
literal, a file's trailing newline is optional in the body, and LF/CRLF bodies
both match. No capture groups exist, so `$1` in a replacement expands to empty.

### D4: File patterns are case-sensitive

`CompilePattern` prepends `(?i)`. File-derived sources are prefixed with `(?-i)`,
which the existing documented opt-out makes case-sensitive from that point on.
Rationale: "verbatim" is the contract, and base64 key bodies are case-significant;
case-insensitive matching of a multi-kilobyte literal only manufactures false
positives. Inline `pattern` keeps its case-insensitive default.

### D5: Fail closed

Resolution happens in `config.Validate` (load, save, control-API change) and in
`config.CompileFilter` (snapshot build). A filter whose file has vanished since
startup therefore cannot be saved, and the last-good snapshot keeps blocking.

### D6: Secret hygiene

Only the path is persisted; contents live in the compiled regex in memory. A
`block` match is recorded without a request body, so matched text never reaches
the traffic log (existing behavior), and the test endpoint only echoes matches
from sample text the caller supplied on loopback.

### D7: Refresh semantics

Files are read whenever filters compile: config load, any config update, and
`proxy.Refresh`. Rotating a key means re-saving the config (or restarting); a
key rotated in place is picked up on the next compile. Documented, not watched.

## Risks

- A glob like `~/.ssh/*` picks up unrelated files; a match on `known_hosts` or
  `config` would block unrelated requests. The user's choice of spec; the README
  recommends `*.pem` / explicit paths.
- Compile is now I/O-bound; it runs on config changes only.
