# Delta Spec for Capability: console

## ADDED Requirements

### Requirement: Console Page Title

The console webapp SHALL display a configurable page title as its top-bar heading and browser tab
title, sourced from the gateway configuration.

#### Scenario: Configured page title

- **GIVEN** gateway configuration with `ui.page_title` set
- **WHEN** an authenticated or anonymous-browsing user opens the console
- **THEN** the top-bar heading and the browser tab title render the configured value

#### Scenario: Default page title

- **GIVEN** gateway configuration without `ui.page_title`
- **WHEN** the configuration is loaded
- **THEN** the effective page title MUST be `VeriProc Operator Console`

#### Scenario: Page title served with instance list

- **GIVEN** the console gateway
- **WHEN** the frontend requests `GET /api/console/instances`
- **THEN** the response's `ui` object MUST include the effective `page_title` as a string

#### Scenario: Fallback before load

- **GIVEN** the webapp before the instance list has loaded or when the request is unavailable
  (e.g. the login screen of a gateway without anonymous browsing)
- **WHEN** the top bar and tab title are rendered
- **THEN** they display the built-in default `VeriProc Operator Console` until a `page_title`
  value is available

## MODIFIED Requirements

### Requirement: Gateway Configuration

The console gateway SHALL load a dedicated configuration file (`veriproc.console/v1` schema) defining
HTTP bind address and TLS settings, console-local authentication sources, the list of managed instances
with per-instance upstream base URL and credentials reference, per-instance shared-filesystem working-root
base, dashboard defaults including the console page title, console SQLite database path and migration
policy, and optional frontend bundle directory, session settings, and audit-retention settings.

#### Scenario: Secret handling

- **GIVEN** gateway configuration
- **WHEN** upstream bearer tokens are configured
- **THEN** the gateway SHOULD NOT store them in plaintext in SQLite
- **AND** environment variables, file-backed secrets, or an external secret manager are preferred

#### Scenario: Read-only filesystem access

- **GIVEN** configured working-root bases
- **WHEN** the gateway accesses them
- **THEN** it SHOULD have read-only access and SHOULD NOT require write access to those roots

**Additional normative rules for this requirement:**

- dashboard defaults MUST include visible slot count, completed-run visibility timeout, refresh interval,
  station activity page size, and the console page title (browser tab title and top-bar heading,
  default `VeriProc Operator Console`, configurable via the `ui.page_title` key)
