# Provider Fixture Provenance

The Yahoo, Baseball Savant, FanGraphs, FantasyPros, and RotoWire fixtures in this directory were derived once from the response shapes and fixture corpus associated with `skout-rust` v0.36.3 at commit `13d8141eef8e1f36b295d651a91a1298e145f0d6`.

All copied examples were reduced to the minimum fields needed by permanent Go tests. Names, league and team identifiers, ranks, statistics, URLs, and lineup details are synthetic or scrubbed. No fixture contains an authorization header, OAuth material, cookie, browser state, credential, or developer-specific path.

| Fixture family | Preserved evidence |
| --- | --- |
| `yahoo/` | Numeric-key JSON shapes, league settings, standings, rosters, free agents, matchups, weekly statistics, public ranks, and incomplete responses |
| `savant/` | Batting and pitching CSV column shapes and numeric normalization |
| `fangraphs/` | Leaderboard, projection, and closer-chart structures |
| `fantasypros/` | Embedded ECR payload structure |
| `rotowire/` | Confirmed and unconfirmed daily-lineup markup |

Production code and permanent tests use only repository-local fixtures and never read or execute the source repository.
