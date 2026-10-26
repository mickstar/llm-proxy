# Spec Delta

## ADDED Requirements

### Requirement: Match verbatim file contents
A filter MAY define `pattern_file` instead of `pattern`: a path, `~`-prefixed path, or glob naming one or more files whose contents at compile time become the filter's pattern. Every matched file's contents SHALL be matched literally — RE2 metacharacters escaped, no capture groups, case-sensitively — and line endings SHALL be normalized so a file's contents match whether the request body uses LF or CRLF and whether or not the file's trailing newline is present in the body.

#### Scenario: Private key blocked
- **WHEN** an enabled `block` filter names `~/.ssh/id_rsa`, the file exists, and a request body contains that file's contents verbatim
- **THEN** the request is not forwarded and the client receives a 403 naming the filter

#### Scenario: Glob covers several keys
- **WHEN** a filter names `~/.ssh/*.pem` and a request body contains the contents of any matching file
- **THEN** the request is blocked, and the body is not blocked when it contains none of them

#### Scenario: Regex metacharacters in a key are literal
- **WHEN** file contents contain characters such as `.`, `+`, or `[`
- **THEN** they are matched literally rather than as a regular expression

#### Scenario: Trailing newline and CRLF tolerated
- **WHEN** the file ends with a newline and the request body carries the same contents without it, or with CRLF line endings
- **THEN** the filter still matches

### Requirement: Pattern files fail closed
A filter whose `pattern_file` matches no file, names an unreadable or empty file, or names a file larger than 1 MiB SHALL be rejected at validation time rather than compiled with no pattern, and file contents SHALL never be written to the configuration file or the traffic log.

#### Scenario: Missing file rejected
- **WHEN** a filter names a `pattern_file` that does not exist
- **THEN** validation fails naming the filter and the path, and the configuration is unchanged

#### Scenario: Contents never persisted
- **WHEN** a filter derived from a file is saved through the control API
- **THEN** the configuration file stores the file's path and not its contents
