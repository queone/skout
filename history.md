# skout History

> One section per release, newest first: what the release added, changed, removed, or deferred in the product, its design, and its code, and why. Governance, toolchain, and release mechanics stay in `CHANGELOG.md` and git. Settled design is in `arch.md`. Search it to learn why a release changed the product.

## v0.16.0

Added:
- `skout st` ends with an `Update:` row that names a newer release and its exact `go install` command.
- The update check asks the Go module proxy at most once a day and caches the answer in `skout/update-check`.
- `NO_UPDATE_CHECK` set to any non-empty value skips the check.

Changed:
- The frozen `st` field order gains one final row after `Config`, so users learn about new releases.
- `st` runs the update check before it takes the database guard, so a slow proxy never blocks `reset`.
- skout requires Go 1.27.2, because gkit `v0.96.0` sets that `go` directive.
- The SQLite driver moves from `modernc.org/sqlite v1.56.0` to `v1.58.0` through gkit's module requirements.

Decisions:
- The Director chose gkit's `update` package over an in-repo port, as the program conventions name for Go programs.
- The Director accepted the Go directive and SQLite driver upgrades that gkit `v0.96.0` brings.
- Only `st` runs the check, so scripted commands stay free of update-check network requests.

Risks:
- A cold network can exceed the two-second timeout; `st` then shows `unknown` and retries one hour later.
