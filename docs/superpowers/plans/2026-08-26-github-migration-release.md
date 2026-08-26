# GitHub Migration And Release Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate IGoNotes to a public GitHub repository and add secure tag-driven releases for Linux, Windows, and macOS on amd64 and arm64.

**Architecture:** Prepare and verify all source changes locally before creating the remote repository. Migrate through a temporary `github` remote, compare every branch SHA, and only then replace the local GitVerse `origin`; GitVerse remains online as an archive. A single GitHub Actions release job builds the embedded frontend once, runs security and Go checks, cross-compiles six binaries, packages them, generates checksums, and publishes them with the GitHub CLI.

**Tech Stack:** Git, GitHub CLI 2.98, GitHub Actions, Node.js 24, npm, Svelte 5, Vite 8, Go 1.26

---

## File Structure

- Modify `web/package-lock.json`: update vulnerable transitive build dependencies without changing top-level dependency ranges.
- Delete `.gitea/workflows/release.yml`: remove the obsolete GitVerse/Gitea release workflow.
- Create `.github/workflows/release.yml`: build, test, package, checksum, and publish tagged GitHub releases.
- Modify `README.md`: identify GitHub Actions as the release system.
- Modify `AGENTS.md`: mark cross-platform GitHub Actions builds as implemented.
- Modify `docs/developer.md`: document the `v*` release trigger.
- Modify `site/docs/developer.md`: keep the published developer documentation consistent.
- Keep `web/package.json` unchanged: existing SemVer ranges already admit the patched dependency versions.
- Keep `web/dist` ignored: the workflow generates it before Go compilation.

### Task 1: Patch Frontend Build Dependencies

**Files:**
- Modify: `web/package-lock.json`
- Verify unchanged: `web/package.json`

- [ ] **Step 1: Reproduce the dependency audit failure**

Run:

```bash
npm audit
```

Working directory: `web`

Expected: exit code `1`, reporting `nanoid <3.3.18` as `high` and `postcss <=8.5.22` as `moderate`.

- [ ] **Step 2: Apply compatible audit fixes**

Run:

```bash
npm audit fix
```

Working directory: `web`

Expected: only `package-lock.json` changes; npm installs `postcss@8.5.26` and `nanoid@3.3.18` without `--force` or SemVer-major updates.

- [ ] **Step 3: Verify the patched dependency tree**

Run:

```bash
npm ls postcss nanoid
npm audit
```

Working directory: `web`

Expected: the tree contains `postcss@8.5.26` and `nanoid@3.3.18`; the audit reports `found 0 vulnerabilities` and exits with code `0`.

- [ ] **Step 4: Verify reproducible installation and frontend compilation**

Run:

```bash
npm ci
npm audit --audit-level=high
npm run build
```

Working directory: `web`

Expected: all commands exit with code `0`; Vite creates `web/dist`.

### Task 2: Replace The Release Workflow

**Files:**
- Delete: `.gitea/workflows/release.yml`
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Reproduce the clean-checkout embed failure**

Run from the repository root:

```bash
tmp=$(mktemp -d "/tmp/opencode/igonotes-clean.XXXXXX")
git archive HEAD | tar -x -C "$tmp"
go -C "$tmp" build -o "$tmp/igonotes" ./cmd/api
```

Expected: Go fails with `web/embed.go:8:12: pattern all:dist: no matching files found`, proving the workflow must build the frontend first.

- [ ] **Step 2: Remove the obsolete GitVerse workflow**

Delete `.gitea/workflows/release.yml`. Do not create a replacement under `.gitea/` or `.gitverse/`.

- [ ] **Step 3: Add the GitHub release workflow**

Create `.github/workflows/release.yml` with exactly:

```yaml
name: Release

on:
  push:
    tags:
      - 'v*'

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v6

      - name: Setup Node.js
        uses: actions/setup-node@v7
        with:
          node-version: '24'
          cache: npm
          cache-dependency-path: web/package-lock.json

      - name: Install frontend dependencies
        working-directory: web
        run: npm ci

      - name: Audit frontend dependencies
        working-directory: web
        run: npm audit --audit-level=high

      - name: Build frontend
        working-directory: web
        run: npm run build

      - name: Setup Go
        uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: true

      - name: Test
        run: go test ./...

      - name: Build release archives
        env:
          VERSION: ${{ github.ref_name }}
        run: |
          set -euo pipefail
          mkdir -p release build

          build_tar() {
            local goos="$1"
            local goarch="$2"
            local output="build/${goos}-${goarch}/igonotes"
            mkdir -p "$(dirname "$output")"
            CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
              go build -trimpath -ldflags="-s -w" -o "$output" ./cmd/api
            tar -C "$(dirname "$output")" -czf \
              "release/igonotes-${VERSION}-${goos}-${goarch}.tar.gz" igonotes
          }

          build_zip() {
            local goos="$1"
            local goarch="$2"
            local output="build/${goos}-${goarch}/igonotes.exe"
            mkdir -p "$(dirname "$output")"
            CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
              go build -trimpath -ldflags="-s -w" -o "$output" ./cmd/api
            zip -j "release/igonotes-${VERSION}-${goos}-${goarch}.zip" "$output"
          }

          build_tar linux amd64
          build_tar linux arm64
          build_zip windows amd64
          build_zip windows arm64
          build_tar darwin amd64
          build_tar darwin arm64

          cd release
          sha256sum ./*.tar.gz ./*.zip > checksums.txt

      - name: Publish GitHub release
        env:
          GH_TOKEN: ${{ github.token }}
        run: >-
          gh release create "${GITHUB_REF_NAME}"
          release/*.tar.gz
          release/*.zip
          release/checksums.txt
          --verify-tag
          --generate-notes
          --title "${GITHUB_REF_NAME}"
```

- [ ] **Step 4: Run the workflow commands locally through compilation**

Run from the repository root after `web/dist` has been built:

```bash
go test ./...
tmp=$(mktemp -d "/tmp/opencode/igonotes-release.XXXXXX")
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$tmp/igonotes-linux-amd64" ./cmd/api
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o "$tmp/igonotes-linux-arm64" ./cmd/api
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$tmp/igonotes-windows-amd64.exe" ./cmd/api
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o "$tmp/igonotes-windows-arm64.exe" ./cmd/api
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$tmp/igonotes-darwin-amd64" ./cmd/api
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o "$tmp/igonotes-darwin-arm64" ./cmd/api
```

Expected: Go tests and all six builds exit with code `0`.

- [ ] **Step 5: Check formatting and the intended diff**

Run:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors; only the status-bar fix, dependency lock update, workflow replacement, and Superpowers documentation are changed.

### Task 3: Update Release Documentation

**Files:**
- Modify: `README.md:19`
- Modify: `AGENTS.md:27`
- Modify: `docs/developer.md:148-150`
- Modify: `site/docs/developer.md:94-96`

- [ ] **Step 1: Update the README release technology**

Replace the release technology line in `README.md` with:

```markdown
- **Сборка и релиз**: GitHub Actions
```

- [ ] **Step 2: Mark cross-platform release builds as implemented**

Replace the release roadmap item in `AGENTS.md` with:

```markdown
- [x] Сборка бинарников под Linux, Windows и macOS для amd64 и arm64 через GitHub Actions
```

- [ ] **Step 3: Update both developer guides**

Replace the release sentence in both `docs/developer.md` and `site/docs/developer.md` with:

```markdown
Релизы публикуются через GitHub Actions при отправке тегов вида `v*` из стабильной ветки `master`.
```

- [ ] **Step 4: Confirm no active project documentation still assigns releases to GitVerse**

Search `README.md`, `AGENTS.md`, `docs/developer.md`, and `site/docs/developer.md` for `gitverse.ru`.

Expected: no matches. Do not change `site/_config.yml`, because GitHub Pages migration is out of scope.

### Task 4: Commit The Prepared Migration Branch

**Files:**
- Modify: `web/src/lib/Sidebar.svelte`
- Modify: `web/package-lock.json`
- Delete: `.gitea/workflows/release.yml`
- Create: `.github/workflows/release.yml`
- Modify: `README.md`
- Modify: `AGENTS.md`
- Modify: `docs/developer.md`
- Modify: `site/docs/developer.md`
- Create: `docs/superpowers/specs/2026-08-26-status-bar-layout-design.md`
- Create: `docs/superpowers/plans/2026-08-26-status-bar-layout.md`
- Create: `docs/superpowers/specs/2026-08-26-github-migration-release-design.md`
- Create: `docs/superpowers/plans/2026-08-26-github-migration-release.md`

- [ ] **Step 1: Review repository state before committing**

Run:

```bash
git status --short
git diff
git log --oneline -10
```

Expected: current branch is `fix/status-bar-layout`; no unrelated files are included.

- [ ] **Step 2: Commit the status-bar fix**

Run:

```bash
git add web/src/lib/Sidebar.svelte docs/superpowers/specs/2026-08-26-status-bar-layout-design.md docs/superpowers/plans/2026-08-26-status-bar-layout.md
git commit -m "fix: keep status bar below note tree"
```

Expected: one commit containing the Sidebar height correction and its design records.

- [ ] **Step 3: Commit the GitHub migration and release automation**

Run:

```bash
git add web/package-lock.json .gitea/workflows/release.yml .github/workflows/release.yml README.md AGENTS.md docs/developer.md site/docs/developer.md docs/superpowers/specs/2026-08-26-github-migration-release-design.md docs/superpowers/plans/2026-08-26-github-migration-release.md
git commit -m "ci: migrate releases to GitHub Actions"
```

Expected: one commit containing the patched lock-file, GitHub release workflow, obsolete workflow deletion, and migration records.

- [ ] **Step 4: Re-run verification after commits**

Run:

```bash
npm audit
npm run build
go test ./...
git status --short
```

Run the first two commands in `web`, then the remaining commands at the repository root.

Expected: zero vulnerabilities, successful frontend build, passing Go tests, and a clean worktree.

### Task 5: Create And Verify The GitHub Repository

**Repository:**
- Create: `https://github.com/NetMoose/IGoNotes`
- Preserve as archive: `git@gitverse.ru:netmoose/IGoNotes.git`

- [ ] **Step 1: Verify GitHub authentication and repository absence**

Run:

```bash
gh auth status
gh repo view NetMoose/IGoNotes
```

Expected: authentication succeeds as `NetMoose`; repository lookup reports that `NetMoose/IGoNotes` does not exist.

- [ ] **Step 2: Create the empty public GitHub repository**

Run:

```bash
gh repo create NetMoose/IGoNotes --public --description "Local Markdown notes editor written in Go and Svelte"
```

Expected: GitHub creates `https://github.com/NetMoose/IGoNotes` without an initial commit.

- [ ] **Step 3: Add GitHub as a temporary remote**

Run:

```bash
git remote add github git@github.com:NetMoose/IGoNotes.git
git remote -v
```

Expected: `origin` still points to GitVerse and `github` points to GitHub.

- [ ] **Step 4: Push all preserved branches**

Run:

```bash
git push github master develop pages Refactor
git push -u github fix/status-bar-layout
```

Expected: GitHub receives all five branches; the current feature branch tracks `github/fix/status-bar-layout`.

- [ ] **Step 5: Set and verify the default branch**

Run:

```bash
gh repo edit NetMoose/IGoNotes --default-branch develop
gh repo view NetMoose/IGoNotes --json nameWithOwner,url,visibility,defaultBranchRef
```

Expected: repository is public and `defaultBranchRef.name` is `develop`.

- [ ] **Step 6: Compare every migrated branch SHA**

Run:

```bash
for branch in master develop pages Refactor fix/status-bar-layout; do
  local_sha=$(git rev-parse "$branch")
  remote_sha=$(gh api "repos/NetMoose/IGoNotes/git/ref/heads/$branch" --jq '.object.sha')
  test "$local_sha" = "$remote_sha" || {
    printf 'SHA mismatch for %s: local=%s remote=%s\n' "$branch" "$local_sha" "$remote_sha" >&2
    exit 1
  }
done
```

Expected: exit code `0` with no mismatch output.

- [ ] **Step 7: Replace the local GitVerse remote only after SHA verification**

Run:

```bash
git remote remove origin
git remote rename github origin
git branch --set-upstream-to=origin/master master
git branch --set-upstream-to=origin/develop develop
git branch --set-upstream-to=origin/pages pages
git branch --set-upstream-to=origin/Refactor Refactor
git branch --set-upstream-to=origin/fix/status-bar-layout fix/status-bar-layout
git remote -v
git branch -vv
```

Expected: the only remote is `origin` at `git@github.com:NetMoose/IGoNotes.git`; all five local branches track matching GitHub branches. The GitVerse repository remains online and unchanged.

### Task 6: Open The Migration Pull Request

**Repository:**
- Pull request: `fix/status-bar-layout` -> `develop`

- [ ] **Step 1: Create the pull request**

Run:

```bash
gh pr create \
  --base develop \
  --head fix/status-bar-layout \
  --title "Fix status bar layout and add GitHub releases" \
  --body "## Summary
- keep the notes tree above the global status bar
- patch vulnerable frontend build dependencies
- replace the obsolete GitVerse workflow with tag-driven GitHub releases
- build Linux, Windows, and macOS archives for amd64 and arm64

## Verification
- npm audit
- npm run build
- go test ./...
- cross-compiled all six release targets"
```

Expected: GitHub returns the URL of a new pull request targeting `develop`.

- [ ] **Step 2: Verify the migrated repository and PR state**

Run:

```bash
gh repo view NetMoose/IGoNotes --json url,visibility,defaultBranchRef
gh pr view --json url,state,baseRefName,headRefName
git status --short
```

Expected: public repository, default branch `develop`, open PR from `fix/status-bar-layout` to `develop`, and a clean local worktree.
