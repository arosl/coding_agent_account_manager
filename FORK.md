# arosl/coding_agent_account_manager: the fork

This fork of
[Dicklesworthstone/coding_agent_account_manager](https://github.com/Dicklesworthstone/coding_agent_account_manager)
lets caam's auth coordinator watch and drive [herdr](https://herdr.dev)'s
panes, beside WezTerm's and tmux's. It is part of the
[ACFS fork](https://github.com/arosl/agentic_coding_flywheel_setup), whose
agents run in herdr, with no tmux or WezTerm (ACFS bead acfs-7mu.3).

The fork follows upstream closely. It carries only the changes below, keeps
them small, and merges upstream regularly. It adds no features of its own.

## The delta

| File | Change |
|---|---|
| `internal/coordinator/herdr.go` | new: `HerdrClient`, a third `PaneClient` beside `WezTermClient` and `TmuxClient` (the table below) |
| `internal/coordinator/coordinator.go` | the backend `herdr` (`--backend herdr`, `"backend": "herdr"` in the config file); `auto` tries herdr first, then WezTerm, then tmux |
| `internal/coordinator/pane_client.go` | the interface comment and the compile-time check name `HerdrClient` |
| `cmd/caam/cmd/coordinator.go` | the help text names the herdr backend |
| `internal/deploy/deploy.go` | `caam setup distributed`: the service PATH check also looks for `herdr`; the installer it runs on a remote of another platform is the fork's |
| `internal/update/update.go` | `caam update` looks for releases in `arosl`, so it never replaces the fork's binary with upstream's |
| `install.sh` | `REPO_OWNER="arosl"`: it installs the fork's releases |
| `install.sh`, `--archive-sha256` | the release archive must have the caller's SHA-256 pin (the section "Releases"); a pinned install never falls back to a source build |
| `internal/installscript/archive_pin_test.go` | tests for the pins |
| `.goreleaser.yaml` | releases go to `arosl`, not as prereleases; no Homebrew formula or Scoop manifest is published (the tap and bucket are upstream's) |
| `.github/workflows/release.yml` | `workflow_dispatch`, so a release can start on a tag by hand |
| `.github/workflows/notify-acfs.yml` | the job runs only in upstream's repository; the fork has no `ACFS_DISPATCH_TOKEN`, and the ACFS fork pins caam by tag |
| `internal/coordinator/herdr_test.go`, `testdata/herdr_pane_list.json` | tests with a stand-in `herdr`; the fixture has herdr 0.9.3's pane list shape |
| `internal/deploy/deploy_test.go` | the PATH check's cases for herdr |
| `.beads/` | not tracked, except its `.gitignore`, which ignores everything: the fork keeps its bead queue local, because br writes the local user name and checkout path into every bead and the fork is public |
| `FORK.md` | this file |

## How the coordinator uses herdr

| `PaneClient` call | herdr command | Note |
|---|---|---|
| `ListPanes` | `herdr pane list` | every pane of every workspace, as tmux's and WezTerm's lists are. A herdr pane id (`w1:p2`) gets the next integer the first time it is listed; an integer is never reused. `Domain` holds the herdr pane id. A pane moved to another workspace gets a new herdr id, so the coordinator sees a new pane |
| `GetText(id, -N)` | `herdr pane read <pane> --source recent-unwrapped --lines N` | soft wraps are joined, as tmux's `capture-pane -J` does, so a wide OAuth URL stays whole. herdr reads from the end only: a positive start line is refused |
| `SendText(id, text, noPaste)` | `herdr pane send-text <pane> <text>` | herdr writes the bytes as they are, like typing, also when the program has turned on bracketed paste (herdr 0.9.3: `\x1b` → 1b, `\x05` → 05, `\x15` → 15, `\r` → 0d). The text goes as one argument with nothing after the pane id: herdr types every argument, `--` too |
| `IsAvailable` | `herdr pane list` | true when the server answers |

The herdr CLI finds its server through `HERDR_SOCKET_PATH`, else the
default session, so a coordinator that systemd or cron starts reaches it.
`herdr --session <name>` is not used.

**Why herdr, not nhm.** The ACFS fork's agent manager,
[nhm](https://github.com/arosl/nhm), cannot give caam what it needs today
(nhm 0.1.6): it has no command that types raw keys or text without
submitting it; `nhm send` submits one prompt, and its gate refuses a blocked
agent, which is the case caam handles (a usage-limit menu, a login prompt);
and `nhm read` has no unwrapped source. The evidence is on ACFS bead
acfs-7mu.3. The change can move to nhm when nhm has such a path.

## Releases

The fork tags `v<upstream version>-herdr.<n>` (for example
`v0.1.23-herdr.3`) on its `main`. Upstream's `release.yml` builds the
release with GoReleaser and signs `SHA256SUMS` with cosign, keyless, as
this repository's workflow on the tag. On the ACFS fork's tool forks, a tag
push starts no workflow run, so a release is started by hand:
`gh workflow run release.yml --ref <tag>`. The workflow's own last step
checks the signature and one archive's checksum. A tag without a release
is never referenced.

`install.sh` checks that signature with cosign (the identity
`https://github.com/arosl/coding_agent_account_manager/.github/workflows/release.yml@refs/tags/<tag>`),
because a fork release has no `SHA256SUMS.minisig`: that signature needs
upstream's key. So the installer needs `cosign` (upstream's releases need
`minisign`); without it, it refuses, unless `CI=true` or `NONINTERACTIVE=1`
lets it fall back to the checksum alone, as upstream's installer does.

`--archive-sha256 [<platform>=]<hex>` (repeatable; `linux_amd64=…`, or
`linux-amd64=…` as ACFS spells nhm's platforms) gives the SHA-256 the
release archive must have, a pin kept outside the release, as nhm's
installer takes it. Pins that are malformed,
or name no archive for this machine, refuse the install before anything is
downloaded. The archive must match its pin before anything else in the
release is trusted; then the installer checks `SHA256SUMS` and the
signature as before. Only where cosign is missing does the matched pin
stand in for the signature: Ubuntu 24.04 packages no cosign. A pinned
install never falls back to a source build. An installed binary of the
requested version is kept without a check, as upstream's installer keeps
it; `--force` installs again.

The ACFS fork pins the tagged `install.sh` and each Linux archive in its
`checksums.yaml`, and runs the installer with `--version=<tag>` and one
`--archive-sha256` per platform.

`caam update` refuses a fork release: it requires the minisign signature
for every release from 0.1.18 on, and fails closed without it. Update the
fork through its installer (or ACFS's `acfs update`).

## Not changed, on purpose

- **`caam wezterm login-all|oauth-urls|recover`** stay WezTerm commands,
  as their name says. The coordinator does this work on herdr.
- **The docs** (README, `docs/`) still name WezTerm and tmux only. Renaming
  them would conflict with upstream merges. Read the coordinator's backend
  list as including herdr.
- **The module path** stays `github.com/Dicklesworthstone/coding_agent_account_manager`.

A change to this list gets a bead first, so the delta stays listed.
