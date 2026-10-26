# Spec Delta

## MODIFIED Requirements

### Requirement: Test filter patterns
The control API SHALL provide a test operation that applies a supplied pattern or `pattern_file` and optional replacement against supplied sample text and returns the matches and resulting text, without saving anything.

#### Scenario: Test a pattern
- **WHEN** a client tests pattern `sk-\w+` against sample text containing `sk-live-abc123`
- **THEN** the response reports the match and, when a replacement is supplied, the resulting text

#### Scenario: Test a file-backed pattern
- **WHEN** a client tests `pattern_file` naming an existing file against sample text containing that file's contents
- **THEN** the response reports the match, and an error naming the path when the file cannot be read

#### Scenario: File-backed test saves nothing
- **WHEN** a client tests a `pattern_file` filter
- **THEN** the configuration is unchanged and the file's contents are not stored
