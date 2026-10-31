# Architecture Specification (delta): Docker entrypoint override

## ADDED Requirements

### Requirement: Docker container entrypoint override

A station with `execution.mode: slurm-docker` MAY declare
`execution.container.entrypoint` as a non-empty string (a single entrypoint
token, per the `docker run --entrypoint` contract). When set, the executor
MUST pass it to the container runtime as the entrypoint override so that the
station `executable` and args are interpreted as the container command
instead of being passed to the image's built-in ENTRYPOINT. When unset,
behavior MUST remain exactly as today (image entrypoint applies).

#### Scenario: Wrapper script in a processor-entrypoint image
- **GIVEN** a station image whose ENTRYPOINT is the processor binary and
  `execution.container.entrypoint` set to a shell
- **WHEN** the run is dispatched
- **THEN** the station executable script executes inside the container instead
  of being passed as an argument to the processor binary

#### Scenario: Default unchanged
- **GIVEN** a station without `entrypoint`
- **WHEN** the run is dispatched
- **THEN** the generated container command is identical to the current output
