# Development Cycle

This repo uses an acceptance-criteria-first workflow.

The lifecycle makes recurring programming checkpoints and their settled context reusable across phases and sessions. This reduces process reconstruction and avoidable rework without weakening authorization, review, verification, or release gates.

## AC Workflow

- Apply `AGENTS.md` `### Four-Phase Workflow` to every phase of this cycle.
- Apply `AGENTS.md` `### Phase-Advancement Rules` to every action instruction and release batch.
- Apply `AGENTS.md` `### Audit Adoption` to every integrated audit cycle.

## Required Artifacts

- `AGENTS.md`
- `README.md`
- `arch.md`
- `plan.md`
- `govna/`

## Cycle

1. **Draft.** Write the authorized AC from `govna/ac-template.md`.
2. **Audit.**
   - Review the AC for missing scope, unsafe assumptions, and untestable requirements without editing it.
   - Start this review immediately when an explicit agent-mediated `govna audit` request emits or reuses one guarded adoption AC.
3. **Refine.**
   - Update a hand-authored AC with settled findings and Director decisions.
   - Keep an audit-emitted AC unchanged.
   - Record its resolved decisions in the active session.
4. **Implement.**
   - Deliver the settled scope.
   - Test the settled scope.
   - Verify the settled scope.
   - Correct implementation defects.
   - Map every scoped path and test in the final read-only closure audit.
5. **Ratify.**
   - Perform the Director-triggered final review.
   - Reuse the Implement evidence snapshot when `AGENTS.md` defines it as current.
   - Revalidate affected evidence when `AGENTS.md` defines it as missing or stale.
   - Apply bounded correction behavior.
6. **Package.** Run `govna/build-release.md` release preparation for the established Ratified or empty release batch only after separate Director authorization.

### Implement Evidence Snapshot

- Define the Implement evidence snapshot as the session-only identity record used to prove that validation evidence remains unchanged.
- Record the active AC content identity and acceptance-requirement identity.
- Record the primary repository's HEAD, index, tracked-worktree, untracked-path, and untracked-content identities.
- Record each relevant ignored-path and external-input identity.
- Use exact values or deterministic content digests for every recorded identity.
- Record exact validation commands, parameters, working directories, and relevant environment or configuration inputs.
- Record every validation result and acceptance-test disposition without changing its status.
- Record each resolved tool path, executable identity, tool version, and canon identity.
- Use the complete primary-repository state as the default dependency boundary.
- Narrow the dependency boundary only when repository evidence proves that a reused check cannot read the excluded state.
- Treat the snapshot as incomplete when a relevant ignored or external input cannot be identified.

Apply the complete phase, scope, correction, contract-integrity, and advancement rules in `AGENTS.md` throughout this cycle.

The `govna` executable ends after deterministic audit comparison and emission. The Operator performs the integrated Audit, Refine, and Pre-Implementation Verification steps. A required change to an immutable emitted AC needs a new audit emission. The pre-Implement fit check uses one private provisional string only to prevent an oversized pending batch. Package requires every implemented batch member to be Ratified, compares the complete pending batch with the exact message, and rejects partial or oversized batches before prep. An empty release batch packages direct-handled changes without AC references only when no implemented AC awaits release.

During Director-authorized Implement, a bounded completeness correction fixes a missed path or instruction when the active AC already settles the required result. The Operator may complete at most three correction rounds within the existing artifact family. Each round updates the AC in Refine, reruns the final AC wording and scope check called Pre-Implementation Verification, and returns to Implement. A Director-owned decision or fourth round pauses for the Director.

A completed Draft flows into Audit, and a clean Audit flows into Refine, because those actions mutate nothing except the active AC document and stay cheap to redo before implementation. Automatic Refine entry requires every finding to be advancement-eligible: outside every Director-owned category with exactly one materially valid correction. Refine-to-Implement, Ratify, and Package stay Director-gated because they mutate the repository, accept work, or prepare a release.

## Notes

- Keep roadmap decisions and follow-on `IE<N>:` items in `plan.md`.
- Keep architecture in `arch.md`.
- Keep repo governance in `AGENTS.md`.
- Remove an IE when rejected, retired, or shipped through its AC pointer.
- Keep ACs in `govna/ac<N>-<slug>.md`.
- Summarize ACs rather than reproduce them in chat.
- Mark an unscoped stub in `## Summary`.
- Keep an unscoped stub's scope and tests TBD.
- Leave an unscoped stub `PENDING` until scoped.
