# Publishing sourcelens to PyPI

This guide publishes the Python package `sourcelens` to PyPI. The workflow
`.github/workflows/publish.yml` does the upload with PyPI trusted publishing:
GitHub proves its identity to PyPI, so no API token is stored anywhere.

You do the one-time setup once. After that, every release is a version bump
and a tag.

## What the workflow does

| trigger | jobs |
|---|---|
| a pushed tag `vX.Y.Z` | build, check, test, publish to **PyPI** |
| Actions > publish > Run workflow, target `testpypi` | build, check, test, publish to **TestPyPI** |
| Actions > publish > Run workflow, target `pypi` | build, check, test, publish to **PyPI** |

The build job:

1. Checks that the version in `src/sourcelens/__init__.py` equals the one in
   `cmd/sourcelens/main.go`, and the tag (`v` + version) when there is one.
2. Builds the sdist and the wheel with `python -m build`.
3. Runs `twine check --strict`, which also checks that the README renders on
   PyPI.
4. Installs the wheel into a fresh environment and runs `sourcelens version`,
   `sourcelens test` and the unit tests.

The tag also starts `.github/workflows/release.yml`, which attaches the Go
binaries to a GitHub release.

## One-time setup

### 1. Accounts

1. Create an account on [pypi.org](https://pypi.org/account/register/) and
   turn on two-factor authentication. PyPI requires it.
2. Create a separate account on
   [test.pypi.org](https://test.pypi.org/account/register/), also with
   two-factor authentication. TestPyPI is a sandbox for trial uploads;
   nothing there affects PyPI.

### 2. GitHub environments

In the GitHub repository go to **Settings > Environments** and create two
environments:

- `pypi`
- `testpypi`

The names must match the workflow exactly. Optionally, under `pypi`, add
yourself as a **required reviewer**. Every PyPI upload then waits for your
approval in the Actions tab.

### 3. Trusted publisher on PyPI

The project does not exist on PyPI yet, so register a *pending* publisher.
The first upload creates the project.

1. Sign in to pypi.org and open **Your account > Publishing**
   (https://pypi.org/manage/account/publishing/).
2. Under **Add a new pending publisher**, choose **GitHub** and fill in:

   | field | value |
   |---|---|
   | PyPI Project Name | `sourcelens` |
   | Owner | `bhagesh-h` |
   | Repository name | `sourcelens` |
   | Workflow name | `publish.yml` |
   | Environment name | `pypi` |

3. Click **Add**.

### 4. Trusted publisher on TestPyPI

Repeat step 3 on https://test.pypi.org/manage/account/publishing/ with the
same values, except **Environment name** `testpypi`.

Setup is complete. A pending publisher becomes a normal publisher after the
first successful upload.

## Releasing a version

The example releases `0.0.2`. For the first release, use `0.0.1` and skip
step 1.

### 1. Set the version

Edit both files to the same version:

- `src/sourcelens/__init__.py`: `__version__ = "0.0.2"`
- `cmd/sourcelens/main.go`: `var version = "0.0.2"`

Versions follow [semantic versioning](https://semver.org): patch (`0.0.2`)
for fixes, minor (`0.1.0`) for new features, major (`1.0.0`) for changes
that break existing commands or files.

### 2. Update the changelog

Add a section `## 0.0.2 (YYYY-MM-DD)` at the top of `CHANGELOG.md` that
lists what changed.

### 3. Check locally

```bash
make python          # editable install with the test and build tools
make test            # unit tests and the append-only check (Go and Python)
make dist            # builds dist/*.tar.gz and dist/*.whl and runs twine check
```

`make test` needs Go for its Go half. To check only the Python package:

```bash
python -m pytest -q
sourcelens test
rm -rf dist && python -m build && python -m twine check --strict dist/*
```

### 4. Commit and push

```bash
git add src/sourcelens/__init__.py cmd/sourcelens/main.go CHANGELOG.md
git commit -m "Release 0.0.2"
git push origin main
```

Wait until the **ci** workflow on `main` is green in the Actions tab.

### 5. Trial upload to TestPyPI (recommended)

1. Open **Actions > publish > Run workflow**.
2. Choose branch `main`, target `testpypi`, and click **Run workflow**.
3. When it finishes, test the upload in a clean environment:

   ```bash
   python -m venv /tmp/try && . /tmp/try/bin/activate
   pip install --index-url https://test.pypi.org/simple/ \
               --extra-index-url https://pypi.org/simple/ sourcelens
   sourcelens version
   sourcelens test
   deactivate
   ```

   The second index provides the dependencies (requests, PyYAML, lxml),
   which are not on TestPyPI.

TestPyPI, like PyPI, never accepts the same version twice. To test again
after a fix, use a pre-release version such as `0.0.2rc1`. Set it in both
version files and commit before running the workflow again.

### 6. Tag and publish

```bash
git tag -a v0.0.2 -m "sourcelens 0.0.2"
git push origin v0.0.2
```

The tag starts two workflows:

- **publish** uploads to PyPI. If `pypi` requires a reviewer, approve the
  deployment in the Actions tab.
- **release** creates a GitHub release with the Go binaries.

### 7. Verify

- The project page https://pypi.org/project/sourcelens/ shows the new version
  and the README.
- A clean install works:

  ```bash
  pipx install sourcelens        # or: pip install sourcelens
  sourcelens version
  ```

## Publishing without GitHub Actions

Use this only if the workflow cannot be used. It needs an API token.

1. On pypi.org open **Account settings > API tokens** and create a token. For
   the very first upload the scope must be "Entire account"; afterwards,
   create a token scoped to the `sourcelens` project and delete the broad one.
2. Build and upload:

   ```bash
   rm -rf dist
   python -m pip install --upgrade build twine
   python -m build
   python -m twine check --strict dist/*
   python -m twine upload dist/*
   ```

   When asked, the username is `__token__` and the password is the token,
   including its `pypi-` prefix.

For TestPyPI, create the token on test.pypi.org and upload with
`python -m twine upload --repository testpypi dist/*`.

## Troubleshooting

| message | cause and fix |
|---|---|
| `invalid-publisher` / `Trusted publishing exchange failure` | the pending publisher does not match the run. Check owner `bhagesh-h`, repository `sourcelens`, workflow `publish.yml` and environment (`pypi` or `testpypi`) letter for letter. |
| `File already exists` / `400 ... version already exists` | PyPI never accepts a version twice, even after deleting it. Raise the version and release again. |
| `tag vX does not match version Y` | the tag and `__version__` differ. Delete the tag (`git push --delete origin vX && git tag -d vX`), fix the version, tag again. |
| `... differ` in "Check that the versions agree" | `src/sourcelens/__init__.py` and `cmd/sourcelens/main.go` have different versions. |
| `twine check` fails | the README does not render on PyPI. Run `python -m twine check dist/*` locally and fix the reported line. |
| the job waits | the `pypi` environment has a required reviewer. Approve it in the run's page. |

## Files that make up the package

| file | role |
|---|---|
| `pyproject.toml` | name, version source, dependencies, the `sourcelens` command, metadata shown on PyPI, build settings (hatchling) |
| `src/sourcelens/` | the package |
| `cmd/sourcelens/defaults/default.yaml` | the default configuration; included in the wheel as `sourcelens/defaults/default.yaml` |
| `README.md` | the PyPI project description (links are absolute so they work on PyPI) |
| `LICENSE` | GPL-3.0, included in both distributions |
| `CHANGELOG.md`, `docs/`, `tests/` | included in the sdist |
