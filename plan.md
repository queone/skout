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

## Current Port Slice

- Establish module `github.com/queone/skout` with Go `1.27.0`.
- Start the Go binary and release line at `0.1.0`.
- Retain frozen Rust CLI version `0.22.1` as reference evidence only.
- Retain Govna executable `v0.7.6` and canon `v0.35.0` as separate governance axes.
- Implement exact plain root help and version output.
- Fail closed for every unported invocation.
- Keep configuration, storage, networking, and product behavior absent.
- Publish the bootstrap as an explicitly incomplete first Go release.

## Deferred Port Scope

- Port command parsing and command-specific help.
- Port `fetch`, `st`, `sync`, `reset`, `m`, `t`, `tt`, `sp`, `r`, `rt`, `h`, `p`, and `i`.
- Preserve the `whatis` alias.
- Port configuration, caching, SQLite schema 6, providers, reconciliation, analysis, and rendering.
- Define ordering and package boundaries in the acceptance document for each later behavior slice.

## Completion Sequence

1. Complete, validate, and publish the Go bootstrap as `v0.1.0`.
2. Port functional behavior through separately authorized slices.
3. Perform a final parity review against the frozen reference.
4. Archive `skout-rust` only after parity and separate explicit authorization.
