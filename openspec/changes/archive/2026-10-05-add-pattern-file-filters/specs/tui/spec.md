# Spec Delta

## MODIFIED Requirements

### Requirement: Manage filters
The TUI SHALL allow creating, editing, enabling, disabling, and deleting filters, and SHALL include a test pane that applies the edited pattern or `pattern_file` and replacement against sample text and shows the matches and resulting text. The filter list SHALL show which match source a filter uses.

#### Scenario: Draft and test a filter
- **WHEN** the user drafts a pattern with sample text in the test pane
- **THEN** matches and the resulting text are shown before the filter is saved

#### Scenario: Draft a file-backed filter
- **WHEN** the user enters a file path or glob in the pattern-file field and sample text in the test pane
- **THEN** matches against that file's contents are shown before the filter is saved

#### Scenario: Disable a filter
- **WHEN** the user disables an enabled filter
- **THEN** subsequent requests are no longer affected by that filter
