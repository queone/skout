# skout

Skout is a command-line fantasy baseball advisor being migrated from Rust to Go. This public Go successor preserves the frozen Rust implementation as its behavioral reference while its functional commands move in tested slices.

## Why

Skout brings fantasy-league context, MLB data, player analysis, and concise terminal presentation into one utility. The migration moves active development to Go while retaining observable behavior and useful enhancements from the frozen implementation.

## Implemented Commands

- Run with no arguments, `-h`, `-?`, or `--help` to print root help, or use `-v` and `--version` for the Go binary version.
- Run `i [term]` or `whatis [term]` to browse or search the embedded 113-entry glossary.
- Run `fetch <host> <path>` to inspect a raw path pinned to one of the eight public provider origins named by `fetch`.
- Run `st` to inspect compatible local state without network access, database creation, schema migration, or configuration mutation. Use `st -l <key>` for a one-render league override.
- Run `t [team]` for one or all MLB 40-man rosters, `tt` for standings and team totals, and `sp` for the three-day probable-pitcher slate. Use `-f` to bypass command snapshot freshness.

The shared grammar and command-specific help cover the complete frozen command surface. Valid `reset`, `sync`, `m`, `r`, `rt`, `h`, and `p` executions remain fail-closed with the migration diagnostic until later slices implement them.

## Local State And Providers

The Go runtime reuses the Rust configuration, raw-cache, and SQLite schema-version-6 formats in place. Configuration and cache replacements are private and atomic. SQLite uses one dedicated connection, WAL mode, a five-second busy timeout, and whole-chain transactions for migrations from schema versions 1 through 5.

MLB StatsAPI supplies team, roster, standings, schedule, and season-stat data. ESPN and OddsShark add optional public odds context. Successful complete command payloads are retained as versioned snapshots; a failed refresh uses the last compatible payload with a stale warning when one exists. `fetch` also supports the frozen public aliases for RotoWire, Baseball Savant, Yahoo, FanGraphs, and FantasyPros. No scoped command requests credentials or uses authenticated Yahoo endpoints.

## Frozen Baseline

The behavioral reference is `queone/skout-rust` tag `v0.36.3` at commit `13d8141eef8e1f36b295d651a91a1298e145f0d6`.

| Axis | Frozen or current value |
| --- | --- |
| Frozen Rust repository release | `v0.36.3` |
| Frozen Rust CLI version | `0.22.1` |
| Go binary and release | `0.3.1` |
| SQLite schema target | `6` |
| Govna executable | `v0.7.8` |
| Govna canon | `v0.36.1` |

These values describe separate version axes. The Go runtime now preserves SQLite schema 6 without introducing schema 7.

## Local Build

Run `./build.sh` for formatting, tests, vetting, static analysis, compilation, and installation into the active Go workspace. The runtime uses CGo-free `modernc.org/sqlite v1.56.0` and its pinned `modernc.org/libc v1.74.4` runtime dependency; CLI, JSON, HTTP, caching, hashing, time, filesystem, and terminal orchestration use the Go standard library. The resolved module graph also contains `github.com/hashicorp/golang-lru/v2 v2.0.7` under MPL-2.0 as a reviewed graph-only exception; it is absent from production and project-test package closures and must be reevaluated before vendoring dependencies or distributing a module cache.

## Deferred Commands

Execution remains deferred for `reset`, Yahoo `sync`, matchup `m`, fantasy roster views `r` and `rt`, and player decision views `h` and `p`. Persistent `st -l` selection, saved-team clearing, authenticated Yahoo acquisition, Savant/FanGraphs/FantasyPros/RotoWire enrichment, and fantasy analysis remain later migration work.

Intentional Go-owned differences in this slice are subcommand `-?` help, provider user agents derived from the Go version, deterministic lowercase response-header ordering, whole-chain atomic schema migration, and temporary non-persisting `st -l` behavior.

Final parity review and archival of `skout-rust` remain separate Director decisions.

## Governance

See [`AGENTS.md`](AGENTS.md) for repository rules and [`govna/operator-contract-rationale.md`](govna/operator-contract-rationale.md) for their design rationale.
