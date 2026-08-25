# Skout Go Migration Plan

## Product Direction

Move active Skout development to the public Go successor while preserving the frozen Rust implementation as the behavioral reference. Publish bounded releases without claiming deferred behavior is implemented.

## Migration Stages

| Stage | Status | Boundary |
| --- | --- | --- |
| Freeze | Complete | Preserve the Rust behavior and durable regression evidence at `queone/skout-rust` v0.36.3. |
| Rename | Complete | Keep the frozen reference under the `skout-rust` name. |
| Port | Active | Move behavior into this Go repository through bounded, tested slices. |
| Parity review | Deferred | Compare the completed Go successor with the frozen reference and record intentional differences. |
| Archive | Deferred | Archive the Rust reference only after parity and explicit Director authorization. |

## Completed Port Surface

- Establish module `github.com/queone/skout` with Go `1.27.0` and the independent Go binary version line.
- Preserve exact plain root help, version output, `i [term]`, and `whatis [term]` behavior.
- Port the complete shared command grammar and command-specific help while retaining fail-closed dispatch for deferred commands.
- Port compatible private configuration, Rust-format raw caching, SQLite schema-version-6 creation and migration, command snapshots, refresh runs, and local status inspection.
- Port bounded public HTTP transport plus MLB StatsAPI, ESPN, and OddsShark adapters.
- Port executable `fetch`, non-persisting `st`, MLB roster `t`, MLB totals `tt`, and probable-pitcher `sp` commands with deterministic display and stale fallback.
- Retain a CGo-free SQLite runtime and the reviewed graph-only MPL-2.0 exception for `github.com/hashicorp/golang-lru/v2 v2.0.7` while it remains outside production and project-test package closures.
- Retain frozen Rust CLI version `0.22.1`, Govna executable `v0.7.8`, and Govna canon `v0.36.1` as separate reference axes.

## Deferred Port Scope

- Port Yahoo synchronization and persistent league/team selection.
- Port destructive reset only with its frozen confirmation and preservation boundaries.
- Port matchup `m`, fantasy roster `r`, fantasy totals `rt`, hitter `h`, and pitcher `p` behavior.
- Port Yahoo acquisition plus Savant, FanGraphs, FantasyPros, and RotoWire enrichment, reconciliation, analysis, and fantasy presentation.
- Remove the temporary non-persisting `st -l` difference when Go synchronization restores compatible persistence.

## Completion Sequence

1. Continue porting the deferred Yahoo and fantasy vertical slices through separately authorized ACs.
2. Preserve local-state compatibility and public release boundaries in each slice.
3. Perform a final parity review against the frozen reference.
4. Archive `skout-rust` only after parity and separate explicit authorization.
