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
