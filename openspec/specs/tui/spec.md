# tui Specification

## Purpose

Terminal interface for watching proxied traffic live and managing filters, models, and settings through the control API, as the first frontend in the server/frontend separation.

## Requirements

### Requirement: Live traffic view
The TUI SHALL display a live-updating list of proxied requests showing at least time, model, endpoint, status, duration, and filter outcome, and SHALL allow opening a selected entry to inspect its request and response bodies.

#### Scenario: Live updates
- **WHEN** a new exchange is recorded while the traffic view is open
- **THEN** the list updates to include the new entry without user action

#### Scenario: Inspect an entry
- **WHEN** the user opens a recorded entry
- **THEN** the filtered request body and the response body are displayed

#### Scenario: Inspect token usage
- **WHEN** the user opens an entry whose response reported token usage
- **THEN** the detail view shows prompt, completion, and total token counts, the cached prompt count when present, and the cost

### Requirement: Show and clear a halt
When the proxy is halted the TUI SHALL show it prominently, naming the triggering filter and the number of refused requests, and SHALL offer a key that resumes the proxy through the control API.

#### Scenario: Halt banner
- **WHEN** the proxy is halted and the TUI polls health
- **THEN** the banner names the filter and the refused count

#### Scenario: Resume from the TUI
- **WHEN** the user presses the resume key while halted
- **THEN** the halt is cleared and the banner returns to normal

### Requirement: Show redactions
The TUI SHALL provide a tab, adjacent to the traffic view, listing every recorded redaction alias with its original value, producing rule, and use count, refreshed when new traffic arrives and on demand.

#### Scenario: Redaction visible after a request
- **WHEN** a request is proxied with a redaction applied and the redactions tab is opened
- **THEN** the alias and its original value are listed

### Requirement: Manage filters
The TUI SHALL allow creating, editing, enabling, disabling, and deleting filters, and SHALL include a test pane that applies the edited pattern or `pattern_file` and replacement against sample text and shows the matches and resulting text. The filter list SHALL show which match source a filter uses.

#### Scenario: Draft and test a filter
- **WHEN** the user drafts a pattern with sample text in the test pane
- **THEN** matches and the resulting text are shown before the filter is saved

#### Scenario: Draft a file-backed filter
- **WHEN** the user enters a file path or glob in the pattern-file field and sample text in the test pane
- **THEN** matches against that file's contents are shown before the filter is saved

#### Scenario: Prefill a replacement
- **WHEN** the user enters a pattern for a replace filter and leaves the replacement empty
- **THEN** the editor fills it with `redacted$R@gmail.com` when the pattern contains `@`, or `[REDACTED-$R]` otherwise, without overwriting a replacement the user typed

#### Scenario: Regenerate a suggestion
- **WHEN** the user changes the pattern and presses the suggest key
- **THEN** the replacement is regenerated from the new pattern

#### Scenario: Disable a filter
- **WHEN** the user disables an enabled filter
- **THEN** subsequent requests are no longer affected by that filter

### Requirement: Manage models
The TUI SHALL allow listing configured models, adding a model by fetching its metadata from OpenRouter, editing entries, and removing entries.

#### Scenario: Add a model
- **WHEN** the user adds model id `deepseek/deepseek-v4.1-flash` in the TUI and the fetch succeeds
- **THEN** the model appears in the list with its fetched metadata

### Requirement: Edit settings
The TUI SHALL allow editing server settings and the OpenRouter credential, saved through the control API.

#### Scenario: Change a setting
- **WHEN** the user edits the OpenRouter API key and saves
- **THEN** the new credential is persisted and used for subsequent upstream requests

#### Scenario: Toggle the model policy
- **WHEN** the user toggles the model policy to `allowlist` in settings and saves
- **THEN** the policy is persisted and subsequent requests gate model ids against the catalog

### Requirement: Server lifecycle
The default `llm-proxy` invocation SHALL start the server and open the TUI attached to it. If a server is already reachable on the configured control address, the TUI SHALL attach to it instead of starting a second one.

#### Scenario: Default start
- **WHEN** `llm-proxy` is run with no subcommand
- **THEN** the server starts and the TUI opens connected to it

#### Scenario: Attach to a running server
- **WHEN** `llm-proxy` is run while a server is already running on the configured control address
- **THEN** no second server starts and the TUI attaches to the existing one
