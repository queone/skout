# skout

Skout is a command-line fantasy baseball advisor being migrated from Rust to Go. This public Go successor preserves the frozen Rust implementation as its behavioral reference while its functional commands move in tested slices.

## Why

Skout brings fantasy-league context, MLB data, player analysis, and concise terminal presentation into one utility. The migration moves active development to Go while retaining observable behavior and useful enhancements from the frozen implementation.

## Current Migration Slice

The implemented Go surface is intentionally small:

- Run with no arguments, `-h`, `-?`, or `--help` to print the frozen plain root help with the new Go version.
- Run with `-v` or `--version` to print `skout 0.1.0`.
- Treat every other invocation as unported, exit with status 2, and perform no product operation.

Configuration, caching, databases, providers, synchronization, analysis, and every functional command remain deferred. The first public Go release is an explicitly incomplete bootstrap milestone.

## Frozen Baseline

The behavioral reference is `queone/skout-rust` tag `v0.36.3` at commit `13d8141eef8e1f36b295d651a91a1298e145f0d6`.

| Axis | Frozen or current value |
| --- | --- |
| Frozen Rust repository release | `v0.36.3` |
| Frozen Rust CLI version | `0.22.1` |
| Go binary and release | `0.1.0` |
| SQLite schema target | `6` |
| Govna executable | `v0.7.6` |
| Govna canon | `v0.35.0` |

These values describe separate version axes. The Go bootstrap does not yet implement SQLite schema 6.

## Local Build

Run `./build.sh` for formatting, tests, vetting, static analysis, compilation, and installation into the active Go workspace. The bootstrap has no third-party Go module dependencies.

## Deferred Commands

The target command surface remains `fetch`, `st`, `sync`, `reset`, `m`, `t`, `tt`, `sp`, `r`, `rt`, `h`, `p`, and `i`, including the `whatis` alias. Their behavior will move in separately authorized migration slices.

Final parity review and archival of `skout-rust` remain separate Director decisions.

## Governance

See [`AGENTS.md`](AGENTS.md) for repository rules and [`govna/operator-contract-rationale.md`](govna/operator-contract-rationale.md) for their design rationale.
