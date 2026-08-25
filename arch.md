# skout Architecture

## Purpose

Skout is a terminal-oriented fantasy baseball advisor. This repository is the Go successor to the frozen `queone/skout-rust` implementation.

## Current System

`cmd/skout` injects process streams, terminal evidence, environment values, and the Go version into `internal/cli`. The shared descriptor grammar renders root and command help, parses global flags in frozen placements, dispatches implemented commands, and keeps the deferred fantasy command family fail-closed.

The implemented application surface has three paths:

1. `fetch` validates an allowlisted provider alias and origin-relative path before using the bounded transport.
2. `st` reads configuration and SQLite status through non-mutating APIs and never opens a writable store.
3. `t`, `tt`, and `sp` coordinate public provider adapters, versioned command snapshots, compatible typed store writes, optional local Yahoo context, and deterministic terminal renderers.

## Implemented Components

- `cmd/skout/main.go`: process entrypoint, version declaration, and production dependency wiring.
- `internal/cli`: complete descriptor grammar, help, streams, diagnostics, dispatch, glossary integration, and deferred-command boundary.
- `internal/app`: origin-pinned fetch, local-only status, and MLB command orchestration with freshness and stale fallback.
- `internal/domain`: provider-neutral roster, standings, totals, and slate records.
- `internal/providers`: bounded public MLB StatsAPI, ESPN, and OddsShark adapters.
- `internal/transport`: validated HTTPS or loopback-test requests, one deadline across redirects and body reads, manual redirect policy, response limits, and deterministic lowercase headers.
- `internal/cache`: Rust-compatible `skout-cache-v1` raw payload storage with SHA-256 names, a 32 MiB limit, atomic private writes, symlink rejection, and deterministic pruning.
- `internal/store`: schema-version-6 creation and whole-chain migration, one dedicated SQLite connection, typed MLB persistence, command snapshots, sync runs, and read-only status inspection.
- `internal/display`: deterministic MLB roster, totals, and probable-pitcher presentation with ANSI-safe semantic styling.
- `internal/config`: compatible private JSON configuration with atomic replacement and deprecated-field read compatibility.
- `cmd/skout/testdata/root-help.txt`: Go-owned golden help derived once from the frozen reference.
- `cmd/skout/testdata/glossary-help.txt`: governed Go-owned glossary help shared by `i` and `whatis`.
- `internal/glossary`: embedded glossary data, validation, lookup, suggestions, selection, and rendering.
- `internal/terminal`: injected color selection and semantic roles used by help, glossary, status, and MLB output.
- `build.sh`: canonical Go validation, installation, preparation, and release entrypoint.

## Migration Boundary

The Rust repository is a migration-time behavioral reference only. Production code and permanent Go tests do not read it, execute it, or depend on its layout.

Production and permanent tests do not read or execute the frozen repository. The copied scrubbed fixtures record `v0.36.3` at commit `13d8141eef8e1f36b295d651a91a1298e145f0d6` as their one-time provenance.

Execution remains deferred for destructive reset, authenticated Yahoo synchronization, matchup orchestration, fantasy roster totals, hitter and pitcher decision views, and their Savant, FanGraphs, FantasyPros, and RotoWire enrichment. Schema version 7, background work, credentials, and roster mutation remain outside the implemented boundary.

## Persistence And Dependency Decisions

The runtime reuses `$HOME/.config/skout/config.json`, `$HOME/.config/skout/skout.db`, and the platform cache root under `skout/api-cache`. `st -l` is intentionally render-only until Go synchronization can rebuild compatible saved state. Complete successful snapshots remain usable after provider failures and are marked stale when refreshed data cannot be acquired.

`modernc.org/sqlite v1.56.0` is the sole new direct runtime capability. `modernc.org/libc v1.74.4` is pinned to the selected driver's matching runtime version. The build remains CGo-free.

The resolved module graph contains `github.com/hashicorp/golang-lru/v2 v2.0.7` under MPL-2.0 through `modernc.org/libc`. This reviewed exception is absent from the production and project-test package closures because it belongs to upstream compiler, generator, and dependency-test machinery. Reevaluate the exception before vendoring dependencies or distributing a module cache.

## Version Axes

| Axis | Value |
| --- | --- |
| Frozen Rust repository release | `v0.36.3` |
| Frozen Rust CLI | `0.22.1` |
| Go binary and release | `0.3.0` |
| SQLite schema target | `6` |
| Govna executable | `v0.7.6` |
| Govna canon | `v0.35.0` |

The axes advance independently. Rust versions remain reference evidence; the Go binary and release line has its own history.

## Intentional Differences

- Accept `-?` for every subcommand help page.
- Derive provider product user agents from the Go version.
- Normalize response-header names to deterministic lowercase order.
- Migrate every supported source schema to version 6 in one immediate transaction.
- Keep `st -l` non-persisting until the Go Yahoo sync slice.

Final parity review and Rust-reference archival remain separately authorized stages.

## Governance Files

- `AGENTS.md`: repository operating contract and migration invariants.
- `plan.md`: migration stages and current delivery boundary.
- `govna/development-cycle.md`: governed development lifecycle.
- `govna/build-release.md`: build and release contract.
