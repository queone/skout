# skout Architecture

## Purpose

Skout is a terminal-oriented, read-only fantasy baseball advisor. It combines a configured public Yahoo league with MLB and analytical provider data while keeping all durable state local.

## Runtime Paths

`cmd/skout` injects process streams, terminal evidence, environment values, and the binary version into `internal/cli`. The descriptor grammar renders help, parses global and command flags, and dispatches application handlers.

The executable application surface has six paths:

1. `sync` resolves a public Yahoo league and primary team, acquires complete provider snapshots in a fixed foreground order, and records isolated freshness and run outcomes.
2. `st` reads configuration and SQLite status through non-mutating inspection APIs; an explicit `-l` selection updates configuration without opening the database for writes.
3. `t`, `tt`, and `sp` coordinate public provider adapters, versioned command snapshots, typed store writes, optional local Yahoo context, stale fallback, and deterministic terminal renderers.
4. `fetch` validates an allowlisted provider alias and origin-relative path before using the bounded transport.
5. `m`, `r`, `rt`, `h`, and `p` combine durable fantasy reads with command-time public Yahoo, MLB schedule, game-log, boxscore, lineup, and odds acquisition; isolated versioned snapshots preserve the last complete view.
6. `reset` resolves the fixed production database path without opening SQLite, requires explicit confirmation, takes exclusive local-database ownership, and deletes only `skout.db`, `skout.db-wal`, `skout.db-shm`, and `skout.db-journal`.

## Public Synchronization

Foreground synchronization uses this ordered provider pipeline:

1. Yahoo league settings, standings, rosters, free agents, and weekly matchup snapshots.
2. Current MLB hitting and pitching, including bounded pitcher game-log quality starts.
3. Five prior MLB hitting and pitching seasons with completeness manifests.
4. Per-team MLB 40-man rosters with row-level freshness and failure isolation.
5. Baseball Savant batting and pitching snapshots.
6. FanGraphs projections, batted-ball data, and closer roles.
7. FantasyPros Expert Consensus Rankings.
8. ESPN current odds.

Each provider item records attempts, successful freshness, degraded detail, or bounded failure detail. A failed item retains prior successful data. The overall run succeeds in degraded form when at least one provider succeeds and fails with recovery guidance only when every provider fails. A private cross-process lock prevents concurrent foreground synchronization.

Yahoo access uses public endpoints only. The runtime sends no authorization header, OAuth material, cookies, browser state, or credentials, and it performs no roster mutation. Daily Yahoo roster acquisition and short-lived RotoWire lineup acquisition run only for the matching read-only command and are not added to foreground synchronization.

## Components

- `cmd/skout`: process entrypoint, version declaration, and production dependency wiring.
- `internal/cli`: descriptor grammar, help, streams, diagnostics, and command dispatch.
- `internal/app`: synchronization, confirmed local-database reset, origin-pinned fetch, local status, MLB commands, fantasy-player views, and matchup orchestration.
- `internal/analysis`: deterministic pitcher-role, projection-window, percentile, and waiver-eligibility helpers.
- `internal/domain`: provider-neutral fantasy, roster, standings, totals, and slate records.
- `internal/providers`: bounded public Yahoo, MLB StatsAPI, Baseball Savant, FanGraphs, FantasyPros, RotoWire, ESPN, and OddsShark adapters.
- `internal/transport`: validated HTTPS or loopback-test requests, one deadline across redirects and body reads, manual redirect policy, response limits, and deterministic lowercase headers.
- `internal/cache`: `skout-cache-v1` raw payload storage with SHA-256 names, a 32 MiB limit, private atomic writes, symlink rejection, and deterministic pruning.
- `internal/store`: schema-version-6 creation and migration, shared and exclusive database-operation locking, one dedicated SQLite connection, complete fantasy and enrichment replacement, snapshots, freshness, sync runs, and read-only status inspection.
- `internal/display`: deterministic fantasy matchup, roster, totals, pool, detail-card, MLB, glossary, help, and status presentation with ANSI-safe semantic styling.
- `internal/config`: private JSON configuration with atomic replacement and compatible deprecated-field reads.
- `internal/terminal`: injected color selection and semantic roles.
- `build.sh`: canonical Go validation, installation, preparation, and release entrypoint.

## Persistence

The runtime uses `$HOME/.config/skout/config.json`, `$HOME/.config/skout/skout.db`, and the platform cache root under `skout/api-cache`. Configuration and cache replacement are private and atomic. SQLite uses schema version 6, one dedicated connection, WAL mode, a five-second busy timeout, and immediate transactions for complete replacements.

Production SQLite commands hold a shared database-operation lock for their complete connection or inspection lifetime. Reset takes the lock exclusively, so it cannot remove local state beneath another command. The separate synchronization lock continues to prevent overlapping foreground syncs. Reset rejects non-regular database-family targets, deletes the primary database before its auxiliary files, and preserves configuration, cache, runtime locks, and unrelated files.

League settings, categories, roster positions, fantasy teams, players, roster slots, and free agents replace as one league-scoped transaction. MLB seasons, individual 40-man rosters, Savant groups, the FanGraphs snapshot, and FantasyPros ranks use bounded complete replacements that preserve unrelated scopes. Yahoo matchup and roster payloads, player game logs, optional detail-card schedules and boxscores, and ESPN odds use source-, scope-, and version-isolated command snapshots.

Fantasy roster and pool reads join role-distinct MLB identities to current and prior season statistics, Statcast, projections, closer roles, ECR, ownership, roster slots, and active-roster injury state. Command-time analysis is deterministic and does not persist recommendations. Daily matchup overlays fetch hitting and pitching concurrently, require reconciled identities for the entire displayed roster, and replace the daily values only after both acquisitions succeed.

## Boundaries

Credentials, Yahoo roster mutation, background scheduling, and long-running services remain outside the runtime boundary. Every advertised command now has an executable Go path. The Rust reference repository is already archived. Only the final cross-repository parity review remains outside this repository's executable migration work.

The application remains on SQLite schema version 6 and introduces no schema version 7 behavior.

## Dependencies

`modernc.org/sqlite v1.56.0` provides a CGo-free SQLite runtime, with `modernc.org/libc v1.74.4` pinned to the matching runtime version. Standard-library packages provide CLI, JSON, CSV, HTTP, caching, hashing, time, filesystem, and terminal orchestration.

The resolved module graph contains `github.com/hashicorp/golang-lru/v2 v2.0.7` under MPL-2.0 through `modernc.org/libc`. It is absent from production and project-test package closures because it belongs to upstream compiler, generator, and dependency-test machinery. Reevaluate it before vendoring dependencies or distributing a module cache.

## Governance Files

- `AGENTS.md`: repository operating contract.
- `plan.md`: product direction and ideas.
- `govna/development-cycle.md`: governed development lifecycle.
- `govna/build-release.md`: build and release contract.
