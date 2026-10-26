# Spec Delta

## MODIFIED Requirements

### Requirement: Validate configuration
Configuration SHALL be validated when loaded and when modified: the JSON must parse, filters must define exactly one match source — an inline `pattern` or a `pattern_file` naming files whose verbatim contents are matched — inline patterns must compile, every file named by `pattern_file` must be readable, non-empty, at most 1 MiB, and matched by its glob, filter actions must be `block` or `replace`, and `replace` filters must define a replacement. On invalid configuration the server SHALL fail to start with an error identifying the config file and the offending field.

#### Scenario: Invalid pattern fails startup
- **WHEN** the config contains a filter with pattern `[unclosed`
- **THEN** startup fails with an error naming the config file and the filter

#### Scenario: Unreadable pattern file fails validation
- **WHEN** a filter sets `pattern_file` to a path that does not exist or a glob that matches no file
- **THEN** configuration validation fails with an error naming the filter and the path, and the file is not persisted

#### Scenario: Ambiguous match source rejected
- **WHEN** a filter defines both `pattern` and `pattern_file`, or neither
- **THEN** configuration validation fails with an error naming the filter and the conflicting fields
