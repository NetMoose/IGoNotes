# Automatic Pre-release Tags Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mark SemVer-suffixed tags as GitHub pre-releases and publish `v0.0.1-alpha.1` from `develop`.

**Architecture:** Keep one tag-driven release workflow and derive a boolean from whether `github.ref_name` contains a hyphen. Convert that boolean into an optional `--prerelease` argument in the publish shell step, leaving stable tags and all build steps unchanged. Integrate the verified change into `develop`, tag that exact commit, and verify the resulting GitHub Release end-to-end.

**Tech Stack:** GitHub Actions, Bash, GitHub CLI 2.98, npm, Go 1.26

---

## File Structure

- Modify `.github/workflows/release.yml`: conditionally pass `--prerelease` to `gh release create`.
- Create `docs/superpowers/specs/2026-08-26-prerelease-tags-design.md`: record the approved tag classification rule.
- Create `docs/superpowers/plans/2026-08-26-prerelease-tags.md`: record implementation and release verification steps.

### Task 1: Add Pre-release Classification

**Files:**
- Modify: `.github/workflows/release.yml:84-94`

- [ ] **Step 1: Create an isolated branch**

Run:

```bash
git switch -c ci/prerelease-tags
```

Expected: the current branch becomes `ci/prerelease-tags`; the untracked design document remains available.

- [ ] **Step 2: Run the structural check and verify it fails**

Run:

```bash
if ! grep -q "PRERELEASE:.*contains(github.ref_name, '-')" .github/workflows/release.yml; then
  printf '%s\n' 'FAIL: release workflow does not classify pre-release tags'
  exit 1
fi
```

Expected: exit code `1` with `FAIL: release workflow does not classify pre-release tags`.

- [ ] **Step 3: Implement conditional pre-release publication**

Replace the existing `Publish GitHub release` step with:

```yaml
      - name: Publish GitHub release
        env:
          GH_TOKEN: ${{ github.token }}
          PRERELEASE: ${{ contains(github.ref_name, '-') }}
        run: |
          set -euo pipefail
          prerelease_args=()
          if [[ "$PRERELEASE" == "true" ]]; then
            prerelease_args+=(--prerelease)
          fi

          gh release create "${GITHUB_REF_NAME}" \
            release/*.tar.gz \
            release/*.zip \
            release/checksums.txt \
            --verify-tag \
            --generate-notes \
            --title "${GITHUB_REF_NAME}" \
            "${prerelease_args[@]}"
```

- [ ] **Step 4: Re-run the structural check**

Run:

```bash
grep -q "PRERELEASE:.*contains(github.ref_name, '-')" .github/workflows/release.yml
grep -q 'prerelease_args+=(--prerelease)' .github/workflows/release.yml
```

Expected: both commands exit with code `0`.

- [ ] **Step 5: Verify both argument branches with the same shell logic**

Run:

```bash
release_flag() {
  local prerelease="$1"
  local prerelease_args=()
  if [[ "$prerelease" == "true" ]]; then
    prerelease_args+=(--prerelease)
  fi
  printf '%s' "${prerelease_args[*]}"
}

test "$(release_flag true)" = "--prerelease"
test -z "$(release_flag false)"
```

Expected: exit code `0`; alpha tags receive the flag and stable tags do not.

### Task 2: Verify And Commit The Workflow Change

**Files:**
- Modify: `.github/workflows/release.yml`
- Create: `docs/superpowers/specs/2026-08-26-prerelease-tags-design.md`
- Create: `docs/superpowers/plans/2026-08-26-prerelease-tags.md`

- [ ] **Step 1: Run project verification**

Run:

```bash
npm audit
npm run build
```

Working directory: `web`

Then run from the repository root:

```bash
go test ./...
git diff --check
```

Expected: zero vulnerabilities, successful frontend build, passing Go tests, and no whitespace errors.

- [ ] **Step 2: Review intended changes before committing**

Run:

```bash
git status --short
git diff
git log --oneline -10
```

Expected: only the release workflow and the two pre-release design/plan documents are changed.

- [ ] **Step 3: Commit the change**

Run:

```bash
git add .github/workflows/release.yml docs/superpowers/specs/2026-08-26-prerelease-tags-design.md docs/superpowers/plans/2026-08-26-prerelease-tags.md
git commit -m "ci: mark version suffixes as prereleases"
```

Expected: one commit and a clean worktree.

### Task 3: Integrate Into Develop

**Branches:**
- Source: `ci/prerelease-tags`
- Target: `develop`

- [ ] **Step 1: Update and fast-forward develop**

Run:

```bash
git switch develop
git pull --ff-only
git merge --ff-only ci/prerelease-tags
```

Expected: `develop` advances to the pre-release workflow commit without a merge commit.

- [ ] **Step 2: Verify the integrated result**

Run:

```bash
npm audit
npm run build
```

Working directory: `web`

Then run from the repository root:

```bash
go test ./...
git status --short
```

Expected: zero vulnerabilities, successful frontend build, passing Go tests, and a clean worktree.

- [ ] **Step 3: Push develop and remove the local feature branch**

Run:

```bash
git push origin develop
git branch -d ci/prerelease-tags
```

Expected: `origin/develop` contains the workflow change and the local feature branch is removed.

### Task 4: Publish And Verify The Alpha Release

**Tag:**
- Create: `v0.0.1-alpha.1`

- [ ] **Step 1: Confirm the release tag is unused**

Run:

```bash
git tag --list v0.0.1-alpha.1
gh release view v0.0.1-alpha.1
```

Expected: the local tag command has no output and GitHub reports that the release does not exist.

- [ ] **Step 2: Create and push the annotated alpha tag**

Run:

```bash
git tag -a v0.0.1-alpha.1 -m "Release v0.0.1-alpha.1"
git push origin v0.0.1-alpha.1
```

Expected: GitHub receives the new tag and starts the `Release` workflow.

- [ ] **Step 3: Wait for the matching workflow run**

Run:

```bash
run_id=""
for attempt in {1..30}; do
  run_id=$(gh run list --workflow release.yml --limit 10 --json databaseId,headBranch --jq 'map(select(.headBranch == "v0.0.1-alpha.1"))[0].databaseId // empty')
  [[ -n "$run_id" ]] && break
  sleep 2
done
test -n "$run_id"
gh run watch "$run_id" --exit-status
```

Expected: the workflow appears within 60 seconds and completes successfully.

- [ ] **Step 4: Verify the published pre-release and assets**

Run:

```bash
gh release view v0.0.1-alpha.1 --json tagName,isPrerelease,isDraft,url,assets
```

Expected:

- `tagName` is `v0.0.1-alpha.1`.
- `isPrerelease` is `true`.
- `isDraft` is `false`.
- Assets contain six platform archives and `checksums.txt`.

- [ ] **Step 5: Verify final repository state**

Run:

```bash
git status --short --branch
git tag --points-at HEAD
```

Expected: clean `develop` synchronized with `origin/develop`; `v0.0.1-alpha.1` points at `HEAD`.
