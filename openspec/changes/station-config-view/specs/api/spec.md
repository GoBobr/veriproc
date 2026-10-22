# Delta Spec for Capability: api

## ADDED Requirements

### Requirement: Station Config Endpoint

`GET /api/v1/stations/{station_id}/config` SHALL return the station's effective configuration as
loaded at daemon startup: environment variables expanded, instance `definitions` references resolved,
and per-run runtime references left literal. The response MUST include `station_id`, `station_name`,
`content_hash`, `schema_version`, `effective_at`, and the resolved configuration document, and MUST be
readable with reader or operator credentials.

#### Scenario: Resolved Configuration Contents

- **GIVEN** a station whose configuration references environment variables and instance `definitions`
  keys
- **WHEN** the config endpoint is called
- **THEN** every `<env:VAR>` reference is returned with its startup-time value substituted
- **AND** references to instance `definitions` keys are returned resolved
- **AND** per-run runtime references such as `<start>`, `<working_root>`, and `prep.*` are returned as
  literal references

#### Scenario: Container Image Exposed

- **GIVEN** a station configured with `execution.container.image`
- **WHEN** the config endpoint is called
- **THEN** the response contains the fully expanded image reference including its version tag

#### Scenario: Freshness Metadata

- **GIVEN** the configuration loaded at daemon startup
- **WHEN** the config endpoint is called
- **THEN** the response includes `content_hash`, `schema_version`, and `effective_at` identifying the
  loaded revision so clients can detect a stale view after restart

#### Scenario: Secret Environment Values Redacted

- **GIVEN** the station configuration references an environment variable whose name matches a secret
  pattern such as token, password, secret, key, or credential
- **WHEN** the config endpoint is called
- **THEN** occurrences of that variable's value in the returned document are replaced with a redaction
  marker instead of the plaintext value

#### Scenario: Reader May Read Config

- **GIVEN** API authentication enabled and a reader-scoped credential
- **WHEN** the reader calls the config endpoint
- **THEN** the request succeeds

#### Scenario: Unknown Station Config

- **GIVEN** a config request for a nonexistent station
- **WHEN** the request is made
- **THEN** the API responds with a not-found error