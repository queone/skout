# skout

Read-only decision-support CLI for Yahoo Fantasy Baseball.

## Why

Fantasy baseball managers make dozens of small decisions every day: which categories need attention, how an MLB club is performing, and which pitchers are likely to start. The useful data is spread across Yahoo, MLB, Baseball Savant, FanGraphs, FantasyPros, and game and odds providers. skout assembles that context locally and provides compact terminal views.

## Overview

skout reads a configured public Yahoo league, enriches its players with MLB and analytical data, and saves complete local snapshots. Synchronization runs in the foreground, isolates provider failures, and retains the last usable data when an optional refresh fails.

Yahoo access is unauthenticated and public-only. skout does not require a developer application, OAuth token, browser login, cookie, or credential, and it never changes a Yahoo roster.

The command views cover daily and weekly fantasy matchups, fantasy rosters and totals, hitter and pitcher pools and detail cards, local status, MLB 40-man rosters, standings and team totals, probable pitchers, provider diagnostics, and an embedded baseball glossary. For architecture and design details, see [arch.md](arch.md).

## Setup

Select your Yahoo league and fantasy team with one foreground sync:

```bash
skout sync -l 170874 -T Toros
```

The league may be a numeric ID or a full Yahoo league key. The team may be a team key or name. In an interactive terminal, `skout sync` prompts when a required selection is missing and saves the result for later commands.

```bash
skout st       # show local provider and snapshot status
skout sync     # refresh the saved league and team
```

Yahoo's public fantasy endpoints are unofficial and may deny access or change without notice. skout does not attempt to bypass those restrictions; it retains complete prior data and reports recovery guidance.

## Usage

```bash
# Synchronization and local state
skout sync                 # refresh the saved public league and team
skout sync -l 170874       # select and refresh a public Yahoo league
skout sync -T Toros        # select the primary fantasy team
skout st                   # inspect local provider and snapshot status
skout st -l 170874         # save a league selection and clear an incompatible team
skout reset                # explicitly delete the local skout database

# MLB-wide views
skout t                    # every MLB 40-man roster
skout t pirates            # select by abbreviation, city, or nickname
skout tt                   # MLB standings and team season totals
skout sp                   # three-day probable-pitcher slate
skout sp -f                # bypass the slate freshness gate

# Fantasy views
skout m                    # today's matchup with live MLB context
skout m -W                 # current weekly matchup totals
skout m -w 7               # a selected Yahoo matchup week
skout m -D Jul-01          # one day in the active fantasy season
skout r                    # the saved fantasy team's roster
skout r toros              # another roster without changing the saved team
skout rt                   # every fantasy team's season totals
skout rt -w                # the saved team's current weekly category totals
skout rt -w 7              # weekly category totals for a selected week
skout rt -w 2026-07-01     # weekly category totals containing a date
skout h                    # the top 20 hitters by Yahoo rank
skout p 50                 # the top 50 pitchers
skout h -p OF -s pa        # filter and sort a player pool
skout p -w                 # Yahoo-available pitchers, qualified first
skout h judge              # one hitter detail card and recent game log

# Reference and diagnostics
skout i                    # browse the embedded glossary
skout i xwoba              # look up one term
skout fetch <host> <path>  # inspect an allowlisted provider response
skout --help               # show command help
skout --version            # show the binary version
```

`skout reset` displays the database path and accepts only `y` or `yes` before deleting it. It removes the database and its SQLite auxiliary files while preserving configuration, provider cache entries, and unrelated files. Reset refuses to run while another skout command is using the local database.

Use `-d` or `--debug` to print operation diagnostics. Complete command snapshots remain usable with a stale warning when a provider refresh fails. Output uses semantic color in supported terminals and equivalent plain text when redirected, when `NO_COLOR` is set, or when `TERM=dumb`.

## Example Use Case

Start the day by refreshing your league and checking provider health:

```bash
skout sync
skout st
```

Then review the fantasy and MLB-wide context for lineup decisions:

```bash
skout tt
skout sp
skout t pirates
skout m
skout r
skout h -w
```

The matchup and fantasy-roster views combine Yahoo scoring with current MLB game context. Player pools keep every Yahoo free agent visible while ranking active, sufficiently used waiver candidates first, and detail cards retain the last complete game log when MLB refreshes fail. MLB totals and probable-pitcher views provide league-wide context. skout remains advisory; make any roster move directly in Yahoo.

## Data Sources

| Source | Authentication | Used for |
|--------|----------------|----------|
| Yahoo Fantasy public endpoints | None | League settings, standings, rosters, free agents, ownership, ranks, and weekly matchup statistics |
| [MLB StatsAPI](https://statsapi.mlb.com/api/v1) | None | Rosters, player identities, statistics, schedules, hitter and pitcher game logs, and boxscores |
| [Baseball Savant](https://baseballsavant.mlb.com) | None | Statcast hitting and pitching metrics |
| [FanGraphs](https://www.fangraphs.com) | None | Projections, batted-ball data, and closer roles |
| [FantasyPros](https://www.fantasypros.com) | None | Expert Consensus Rankings |
| ESPN | None | Current game and odds context |
| OddsShark | None | Optional future-game odds |

ESPN and OddsShark are supplemental sources. OddsShark is unofficial and may degrade without failing the probable-pitcher slate.

## Building from Source

Requires Go 1.27 or newer.

```bash
./build.sh
```

`./build.sh` is the canonical repository validation and build command. It formats, tests, vets, analyzes, compiles, and installs `skout` into the active Go workspace.

## Governance

See [AGENTS.md](AGENTS.md) for repository rules and [govna/operator-contract-rationale.md](govna/operator-contract-rationale.md) for their design rationale.
