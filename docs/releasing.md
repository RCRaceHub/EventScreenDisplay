# Releasing

Each release is three Debian packages, one per architecture (`arm64`,
`armhf`, `amd64`), and an SD card image (`eventscreen-<version>-arm64.img.xz`
with `eventscreen-imager.json` for Raspberry Pi Imager, see
[sd-card-image.md](sd-card-image.md)), attached to a GitHub release.

## Cut a release

The tag is the version. There's nothing to bump in the code first.

1. Create a tag `vMAJOR.MINOR.PATCH` on `main`, either way:
   - **GitHub UI:** Releases → Draft a new release → *Choose a tag* → type
     `v0.5.1` → *Create new tag on publish*, target `main` → Publish.
   - **Command line:**

     ```bash
     git checkout main && git pull
     git tag v0.5.1
     git push origin v0.5.1
     ```

2. The **Build** workflow runs the tests, builds `eventscreen_0.5.1_arm64.deb`,
   `eventscreen_0.5.1_armhf.deb` and `eventscreen_0.5.1_amd64.deb` with that
   version stamped into the binary (the screen reports `0.5.1` in `/info`),
   and the SD card image, and attaches them to the release, creating the
   release with generated notes if you pushed only a tag. The `.deb`s and the
   image appear about 15 minutes after the tag, once the image is built.

Pre-releases work the same way: `v0.6.0-rc1` builds `0.6.0~rc1`, which apt
treats as older than `0.6.0`. A tag that isn't a version (for example
`v1.2`) fails the build with a message saying so.

If the workflow fails, fix the problem on `main`, delete the tag
(`git push origin :v0.5.1 && git tag -d v0.5.1`, or delete the release and
tag in the UI) and tag again.

`Version` in `internal/version/version.go` is only used for builds that
aren't from a tag (pull requests, pushes to `main`, local builds). Bump it
now and then, usually to the last release, so those builds report something
close.

## Checks

The **Build** workflow runs on every push to `main`, every pull request and
every `v*` tag, as separate checks:

| Check | What it does |
| --- | --- |
| `Go tests` | Every unit, API and script test (`go test -race ./...`). The run's summary shows the pass count and coverage per package |
| `Lint (gofmt, vet, shellcheck)` | Go formatting and `go vet`, and `shellcheck` on the installer, `eventscreen-setup`, `eventscreen-check` and the package scripts |
| `Package (arm64)` / `(armhf)` / `(amd64)` | Runs only if both pass. Builds that `.deb`, runs its binary (ARM ones under qemu) to check the version, and installs and purges the `amd64` one on Ubuntu |
| `Docker image` | Builds the headless image for amd64, arm64 and arm/v6, and smoke-tests its API |
| `SD card image` | Builds the image from the official Pi OS Lite and the `arm64` package, runs `image/check-image.sh` on it, and uploads it as the `eventscreen-image` artifact (kept 5 days on branches). About 12 minutes |
| `Attach to release` | Tags only: attaches the three `.deb`s, the image and `eventscreen-imager.json` to the release |

To stop anything being merged with failing tests, make the checks required:
**Settings → Branches → Add branch protection rule** (or **Rulesets**) for
`main`, tick **Require status checks to pass before merging** and add
`Go tests`, `Lint (gofmt, vet, shellcheck)` and the three `Package` checks.
A check only appears in that list after it has run once.

## Versions

- `MAJOR.MINOR.PATCH`. Bump PATCH for fixes, MINOR for new features, and
  MAJOR for anything that breaks the API (which would also mean `/api/v2`)
  or needs manual steps on the Pis.
- `API` in `internal/version/version.go` changes only with a new API
  path; adding fields to v1 responses doesn't change it.

## Build a package locally

```bash
make debs                       # all three, version from version.go
make deb-arm64 VERSION=0.8.0~rc1 # or one, with an explicit version
ARCH=armhf packaging/build-deb.sh 0.8.0~rc1   # the same without make
ls dist/
```

Needs Go and `dpkg-deb` (any Debian, Ubuntu or Raspberry Pi OS machine; the
binaries are cross-compiled). Copy the `.deb` for the Pi's architecture to
it and `sudo apt install ./eventscreen_*_arm64.deb` to try it.

Every push and PR also uploads the built packages as workflow artifacts
(`eventscreen-deb-arm64` and so on), which is handy for testing a branch on
a real Pi.
