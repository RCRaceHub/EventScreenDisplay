# Releasing

Each release is a Debian package attached to a GitHub release.

## Cut a release

1. Bump `__version__` in `eventscreen/__init__.py` (for example `0.3.0`) and
   merge that to `main`.
2. Tag the merge commit and push the tag:

   ```bash
   git checkout main && git pull
   git tag v0.3.0
   git push origin v0.3.0
   ```

3. The **Build** workflow runs the tests, checks that the tag matches
   `__version__`, builds `eventscreen_0.3.0_all.deb` and creates the GitHub
   release with the package attached and generated release notes.

If the workflow fails, fix the problem on `main`, delete the tag
(`git push origin :v0.3.0 && git tag -d v0.3.0`) and tag again.

## Checks

The **Build** workflow runs on every push to `main`, every pull request and
every `v*` tag, as separate checks:

| Check | What it does |
| --- | --- |
| `Tests (Python 3.11)` / `Tests (Python 3.13)` | All the unit and API tests in `tests/`, on the Python in Raspberry Pi OS Bookworm (3.11) and Trixie (3.13) |
| `Shell scripts` | `shellcheck` on the installer, `eventscreen-setup`, `eventscreen-check` and the package scripts |
| `Package` | Runs only if both pass. Builds the `.deb`, installs and purges it on Ubuntu, and on a tag attaches it to the release |

To stop anything being merged with failing tests, make the checks required:
**Settings → Branches → Add branch protection rule** (or **Rulesets**) for
`main`, tick **Require status checks to pass before merging** and add
`Tests (Python 3.11)`, `Tests (Python 3.13)`, `Shell scripts` and `Package`.
A check only appears in that list after it has run once.

## Versions

- `MAJOR.MINOR.PATCH`. Bump PATCH for fixes, MINOR for new features, and
  MAJOR for anything that breaks the API (which would also mean `/api/v2`)
  or needs manual steps on the Pis.
- `API_VERSION` in `eventscreen/__init__.py` changes only with a new API
  path; adding fields to v1 responses doesn't change it.

## Build a package locally

```bash
packaging/build-deb.sh          # version from __init__.py
packaging/build-deb.sh 0.3.0~rc1 # or an explicit version
ls dist/
```

Needs only `dpkg-deb` (any Debian, Ubuntu or Raspberry Pi OS machine). Copy
the `.deb` to a Pi and `sudo apt install ./eventscreen_*.deb` to try it.

Every push and PR also uploads the built package as a workflow artifact
(`eventscreen-deb`), which is handy for testing a branch on a real Pi.
