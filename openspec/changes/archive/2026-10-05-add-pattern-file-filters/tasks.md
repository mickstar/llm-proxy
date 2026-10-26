# Tasks

## 1. Config: pattern_file resolution

- [x] 1.1 Add `PatternFile string \`json:"pattern_file,omitempty"\`` to `config.Filter` and require exactly one of `pattern`/`pattern_file` in `Validate`, with errors naming the filter and fields.
- [x] 1.2 Add `config.CompileFilter(Filter) (*regexp.Regexp, error)`: inline patterns compile through `CompilePattern`; file specs resolve (`~`, glob, sorted, directories skipped), read (≤1 MiB), and become a literal alternation prefixed `(?-i)`.
- [x] 1.3 Fail validation when a file spec matches nothing, a path is unreadable, or contents are empty/whitespace-only; verify with unit tests for missing file, empty file, directory-only glob, oversize file, and both/neither source.

## 2. Filter engine

- [x] 2.1 Compile enabled filters through `config.CompileFilter`.
- [x] 2.2 Change `filter.Test` to take a `config.Filter` (pattern or pattern_file) plus sample, and verify matching against file contents, trailing-newline tolerance, and CRLF bodies.

## 3. Control API

- [x] 3.1 Accept `pattern_file` in `POST /api/filters/test` and route the rule through `filter.Test`; verify with a handler test that a file-backed test reports matches and saves nothing.

## 4. TUI

- [x] 4.1 Add the pattern-file field to the filter editor (focus cycle, placeholder), show it in the table's pattern column, include it in the save payload and its required-field check, and pass it to the live test call.
- [x] 4.2 Verify with unit tests for the table cell and the editor form/save validation, plus a manual smoke of the test pane.

## 5. Docs

- [x] 5.1 README: document `pattern_file` in the config reference table, add a "block my SSH keys" example, and note the case-sensitivity, glob, and refresh semantics.

## 6. Verification

- [x] 6.1 `gofmt -l .`, `go vet ./...`, `go test ./...` clean.
- [x] 6.2 End-to-end smoke: run the built server with a temp config and a fake upstream, create a `pattern_file` block filter for a temp key file, send a request containing the key (expect 403 naming the filter, key absent from the traffic entry) and one without (expect 200).
