# Git Synchronization Implementation Roadmap

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this roadmap plan-by-plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the approved Git synchronization design through eight independently reviewable implementation plans with explicit dependency gates and safe parallel subagent waves.

**Architecture:** Backend contracts, worktree safety, and manual synchronization land sequentially because they define shared state and lock order. Once those contracts are stable, backend conflict work and the basic frontend can proceed in parallel; later frontend conflict handling and autosync resilience use internal parallel lanes but serialize integrations touching `App.svelte` or `GitManager`.

**Tech Stack:** Go 1.26, system Git 2.28+, SQLite, Svelte 5, Vite 8, Vitest 4, Tailwind CSS 4, Node.js 24, GitHub Actions.

---

## Source Of Truth

Approved design:

- `docs/superpowers/specs/2026-09-01-git-synchronization-design.md`

Implementation plans:

1. `docs/superpowers/plans/2026-09-01-git-foundation.md`
2. `docs/superpowers/plans/2026-09-01-git-worktree-safety.md`
3. `docs/superpowers/plans/2026-09-01-git-manual-sync.md`
4. `docs/superpowers/plans/2026-09-01-git-conflict-backend.md`
5. `docs/superpowers/plans/2026-09-01-git-settings-frontend.md`
6. `docs/superpowers/plans/2026-09-01-git-conflict-frontend.md`
7. `docs/superpowers/plans/2026-09-01-git-autosync-resilience.md`
8. `docs/superpowers/plans/2026-09-01-git-integration-hardening.md`

Every plan begins with its own scope, frozen contracts, exact file ownership, TDD tasks, verification commands, commit steps, and `Parallel Dispatch` section. This roadmap does not replace those details.

## Dependency Graph

```text
Plan 1: Foundation
  |
  v
Plan 2: Worktree safety and note revisions
  |
  v
Plan 3: Initial connect and manual sync
  |\
  | +----------------------+
  v                        v
Plan 4: Conflict backend   Plan 5: Settings/status frontend
  |                        |
  +------------+-----------+
               |
               v
Plan 6: Stale-note and conflict frontend
               |
               v
Plan 7: Autosync and circuit breaker
               |
               v
Plan 8: Integration, cross-platform, CI, and docs
```

Plan 7 backend-only scheduler tasks may start after Plans 3 and 4 while Plan 6 component tasks are running. Plan 7 frontend integration must wait for Plans 5 and 6 because all three touch `App.svelte`, `NotesWorkspace.svelte`, and shared frontend API contracts.

## Execution Waves

### Wave 0: Isolated Workspace

- [ ] Create a dedicated implementation worktree from the reviewed planning commit.
- [ ] Run the baseline Go and frontend suites with Node.js 24.
- [ ] Record any pre-existing failures before implementation.

Do not create one worktree per complete plan by default. Use one integration worktree, then create short-lived subagent worktrees only for parallel tasks explicitly listed inside the active plan.

### Wave 1: Backend Contracts

- [ ] Execute Plan 1 serially, using its internal parallel lanes after the model contract lands.
- [ ] Review runner redaction, config ownership, migrations, API guards, and the proof that unconfigured startup runs no Git command.
- [ ] Run Plan 1 full verification before starting Plan 2.

Plan 1 owns the shared Git DTOs and low-level runner contracts. No later plan may rename them opportunistically; contract changes require updating all dependent plans first.

### Wave 2: Filesystem Safety

- [ ] Execute Plan 2.
- [ ] Keep coordinator/settings lock-order work serial.
- [ ] Dispatch active-filesystem transaction and note-revision tasks in parallel only after constructor and lock contracts are merged.
- [ ] Run service race tests and all existing transaction rollback tests.

Required lock order after this wave:

```text
BaseOperationCoordinator
  -> SettingsService.mu
  -> NoteService.baseMu
  -> repository/SQLite
```

### Wave 3: Manual Git Operation

- [ ] Execute Plan 3.
- [ ] Freeze operation/journal contracts before dispatching connect, sync/recovery, and repository workers.
- [ ] Merge engine lanes before implementing `GitManager`.
- [ ] Verify exact-OID push, one retry only, backup refs, startup recovery, and `202 Accepted` API behavior.

At the end of this wave the product has manually configurable, manually triggered synchronization and safely stops at conflicts, but does not resolve conflicts or schedule autosync.

### Wave 4: Backend Conflict And Basic Frontend

The following complete plans may run concurrently because their production file ownership does not overlap:

- [ ] Execute Plan 4 in a backend worktree.
- [ ] Execute Plan 5 in a frontend worktree.

Plan 4 owns Go conflict parsing/resolution/recovery and conflict routes. Plan 5 owns basic frontend Git settings, status polling, and manual sync. Plan 5 may display `conflict` status but must not implement conflict APIs or stale-note behavior.

Merge order:

1. Plan 4 backend.
2. Plan 5 frontend.
3. Full Go and frontend verification.

### Wave 5: Conflict Frontend And Scheduler Core

- [ ] Execute Plan 6 component/API tasks using its internal parallel lanes.
- [ ] In parallel, execute only the repository and fake-clock scheduler tasks from Plan 7 that exclusively own Go backend files.
- [ ] Merge Plan 6 component lanes and perform its serial `App.svelte` integration.
- [ ] Merge Plan 7 backend lanes, then perform its serial `GitManager` and lifecycle integration.
- [ ] Perform Plan 7 pause/resume frontend integration only after Plan 6 is complete.

Unsafe parallel combinations:

- Two workers editing `App.svelte` or `App.test.js`.
- Two workers editing `GitManager` or its status transition tests.
- Settings lifecycle changes running in parallel with coordinator changes.
- Route registration workers editing the same route/method/origin-guard lists.
- Two migration workers editing the same migration registry or schema version.

### Wave 6: Final Hardening

- [ ] Execute Plan 8 after Plans 1-7 pass independently.
- [ ] Dispatch native Git fixtures, platform process handling, CI/build scripts, and documentation only according to Plan 8 exclusive file lanes.
- [ ] Run the final two-device, conflict, timeout, restart, five-failure pause, race, security, and cross-platform acceptance matrix serially.
- [ ] Update feature status documentation only after the matching acceptance evidence passes.

## Subagent Rules

Each implementation task uses a fresh subagent. The coordinator provides:

- the exact plan task and dependency commit;
- exclusive files the subagent may edit;
- commands it must run;
- expected RED and GREEN evidence;
- prohibition on unrelated refactors, amend, reset, and force operations.

For every subagent result:

1. Review the diff against the task, not just the summary.
2. Run the focused command again in the integration worktree.
3. Run contract/review checks before merging the next dependent task.
4. Run the plan's full suite at each wave boundary.
5. Never merge two agents that modified the same central file without a deliberate serial reconciliation task.

## Completion Gate

The feature is complete only when Plan 8 records fresh evidence that:

- non-Git bases still work without installed Git;
- two devices exchange non-conflicting changes;
- initial unrelated histories retain both sides and create a backup ref;
- stale browser saves and Git conflicts cannot silently overwrite data;
- conflict state and partial resolutions survive restart;
- five consecutive failures persistently pause autosync;
- disable/forget never delete `.git` or user files;
- secrets never enter config, API, logs, or commit messages;
- Linux, Windows, and macOS native Git integration tests pass;
- frontend tests/build, Go tests/race/vet, and production build pass.
