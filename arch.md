# skout Architecture

## Purpose

Skout is a terminal-oriented fantasy baseball advisor. This repository is the Go successor to the frozen `queone/skout-rust` implementation.

## Current System

The current Go system routes four bounded outcomes from `cmd/skout/main.go`:

1. Exact root-help invocations write the frozen plain help with the intentional Go version reset to stdout and exit successfully.
2. Exact version invocations write `skout 0.2.0` to stdout and exit successfully.
3. `i [term]` and `whatis [term]` parse glossary-scoped flags and render the embedded glossary without product I/O.
4. Every remaining invocation writes one migration diagnostic to stderr and exits with status 2.

The command runner receives arguments, streams, environment values, and terminal evidence through a bounded runtime context. Production populates that context from the process while tests inject it deterministically. The implemented surface performs no configuration lookup, network access, filesystem mutation, caching, database work, or provider operation.

## Implemented Components

- `cmd/skout/main.go`: process entrypoint, version declaration, root-help content, and bounded routing.
- `cmd/skout/main_test.go`: exact help, version, glossary CLI, stream, terminal, exit-code, and fail-closed coverage.
- `cmd/skout/testdata/root-help.txt`: Go-owned golden help derived once from the frozen reference.
- `cmd/skout/testdata/glossary-help.txt`: governed Go-owned glossary help shared by `i` and `whatis`.
- `internal/glossary`: embedded glossary data, validation, lookup, suggestions, selection, and rendering.
- `internal/terminal`: injected color selection and the terminal roles used by glossary output and help.
- `build.sh`: canonical Go validation, installation, preparation, and release entrypoint.

## Migration Boundary

The Rust repository is a migration-time behavioral reference only. Production code and permanent Go tests do not read it, execute it, or depend on its layout.

The target product still includes parsing for deferred commands, configuration, SQLite schema 6, caches, provider clients, synchronization, analysis, and broader terminal rendering. None of those deferred systems exists in the Go implementation yet.

## Version Axes

| Axis | Value |
| --- | --- |
| Frozen Rust repository release | `v0.36.3` |
| Frozen Rust CLI | `0.22.1` |
| Go binary and release | `0.2.0` |
| SQLite schema target | `6` |
| Govna executable | `v0.7.6` |
| Govna canon | `v0.35.0` |

The axes advance independently. Rust versions and the schema value describe the frozen target; Go binary and release version `0.1.0` starts the successor's own history.

## Deferred Architecture

Future migration slices will define product packages, provider boundaries, storage ownership, caching, reconciliation, and presentation only when their behavior enters scope. Final parity review and Rust-reference archival remain separately authorized stages.

## Governance Files

- `AGENTS.md`: repository operating contract and migration invariants.
- `plan.md`: migration stages and current delivery boundary.
- `govna/development-cycle.md`: governed development lifecycle.
- `govna/build-release.md`: build and release contract.
