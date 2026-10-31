# Shared apt repository (design)

Status: **built, waiting to be published.** The tooling is ready to go
into a new public `RCRaceHub/apt` repository; see that repo's README for the
one-time setup (Pages, signing key secret). Packages are public.

Goal: one signed apt repository for every RCRaceHub package (this viewer
and the other projects' packages), so a Pi or server is set up once and then
gets everything with `apt install` and updates with `apt upgrade`.

```
 EventScreenDisplay ──tag v0.3.0──▶ GitHub release (eventscreen_0.3.0_all.deb)
 other project      ──tag v1.2.0──▶ GitHub release (other_1.2.0_arm64.deb …)
                                         │
                       RCRaceHub/apt  ◀──┘ workflow collects the .debs,
                       (GitHub Action)     builds indexes, signs them
                                         │
                       GitHub Pages ─────▶ https://rcracehub.github.io/apt
                                         │
                       Pis and servers ◀─┘ apt update / apt install
```

## Recommended setup

**A dedicated `RCRaceHub/apt` repository, published with GitHub Pages, built
from the GitHub releases of each project.**

- Each project keeps doing what this one now does: tag a version, CI attaches
  the `.deb` to a GitHub release. Projects don't need to know the apt
  repository exists.
- `RCRaceHub/apt` holds a list of source projects and one workflow. The
  workflow downloads the `.deb` assets from those projects' releases, lays
  them out as a standard Debian repository (`pool/` and `dists/`) with
  `apt-ftparchive`, signs the `Release` file with the repository's GPG key and
  deploys the result to GitHub Pages.
- The repository is **rebuilt from scratch every run**. The GitHub releases
  are the source of truth, so there's no database to keep (unlike `reprepro`
  or `aptly`), and deleting a release removes it from apt on the next run.

### Source list

```
# RCRaceHub/apt: sources.txt
RCRaceHub/EventScreenDisplay
RCRaceHub/<other-project>
```

The newest 5 releases of each repo are kept (`KEEP_RELEASES`), so older
versions stay downloadable for rollbacks.

### Suites

| Suite | Contains | For |
| --- | --- | --- |
| `stable` | Normal GitHub releases | Every screen and server |
| `testing` | Releases marked "pre-release" | Trying a version on one Pi first |

One component, `main`. Architectures `all`, `armhf`, `arm64` and `amd64`, so
the other project can ship architecture-specific packages alongside this
`all` one.

### When it rebuilds

- **Hourly**, on every push to the apt repo, and **by hand** from the
  Actions tab. Needs no extra secrets for public source repos.
- **Straight after a release**, optionally. This project's release job sends
  a `repository_dispatch` to `RCRaceHub/apt` when the `APT_DISPATCH_TOKEN`
  secret is set (a fine-grained token with Contents read/write on
  `RCRaceHub/apt` only). Without it, releases appear within the hour.

### Signing

- A dedicated GPG signing key, used for nothing else, stored as a secret in
  `RCRaceHub/apt` (`APT_SIGNING_KEY`), with an offline backup kept somewhere
  safe.
- Ed25519 (supported by apt on Bookworm and later), set to expire after two
  or three years, so that rotation is planned rather than forgotten.
- The public key is published in the repository as
  `rcracehub-archive-keyring.gpg`. Later it can also be packaged as
  `rcracehub-archive-keyring`, so key rotations arrive through apt itself.

### Using it on a Pi

```bash
sudo curl -fsSL https://rcracehub.github.io/apt/rcracehub-archive-keyring.gpg \
  -o /usr/share/keyrings/rcracehub-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/rcracehub-archive-keyring.gpg] https://rcracehub.github.io/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/rcracehub.list
sudo apt update
sudo apt install eventscreen
sudo eventscreen-setup --hostname screen-01
```

Updating is then `sudo apt update && sudo apt upgrade` like any other package.

### Automatic updates on screens

`unattended-upgrades` could install new versions by itself, but an update
restarts the viewer, and that shouldn't happen in the middle of a race
meeting. Start with manual upgrades between events. Later, the central
manager could trigger upgrades on screens when it knows nothing is running.

## Limits of GitHub Pages

- Site size up to 1 GB and files up to 100 MB, with a soft bandwidth limit of
  100 GB a month. A few versions of small packages are far below that.
- GitHub Pages for a private repository needs a paid GitHub plan, and the
  packages are publicly downloadable either way. If the packages must be
  private, see the alternatives.

## Alternatives considered

| Option | Good | Less good |
| --- | --- | --- |
| **GitHub Pages + apt-ftparchive** (recommended) | Free, no servers, no state, everything in GitHub | Public only (unless on a paid plan); size limits above |
| Same workflow, publishing to Cloudflare R2 or S3 | No size worries, can be private behind a token | An account to run, credentials to manage |
| `aptly` or `reprepro` on a small VPS | Full control, private easily | A server to patch and back up |
| Hosted (Cloudsmith, packagecloud, Gemfury) | Least work, nice UI | Free tiers are limited or open-source only; another vendor |

## Steps to set it up

1. Create `RCRaceHub/apt` (public) with the prepared tooling, and turn on
   GitHub Pages with the source set to GitHub Actions.
2. Run `scripts/new-signing-key.sh` there, add the private key as the
   `APT_SIGNING_KEY` secret and keep an offline backup.
3. Tag a release here (`v0.2.0`) so there's a package to publish, then run
   the publish workflow.
4. Point one Pi at it and check `apt install eventscreen` works.
5. Add the other project to `sources.txt`.
6. Optionally add `APT_DISPATCH_TOKEN` to each project.
