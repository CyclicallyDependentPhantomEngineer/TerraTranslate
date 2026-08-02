# Contributing to terra-translate

## Getting set up

```bash
git clone <this repository>
cd terra-translate

go mod download
go test ./...
go build -trimpath -o ./bin/terra-translate .
```

You need Go 1.21 or newer. Terraform on `PATH` is optional for development: the
unit tests do not shell out to it, but `-fmt-check` and `-validate` do, and so
does the `docs-are-executable` CI job.

The full walkthrough is in [`setup.md`](setup.md).

## Before opening a pull request

```bash
gofmt -l . | grep -v '^catalog/'   # must print nothing
go vet ./...
go test -race ./...
```

CI runs the same three checks, on Go 1.21 and current stable, plus two jobs
worth knowing about:

- **`docs-are-executable`** runs the commands from `README.md`, `setup.md`, and
  `example/README.md` and asserts the documented files and exit codes. If you
  change a flag, a default, or an output path, this job will fail until the
  documentation is updated to match. That is the point of it.
- **`repo-hygiene`** rejects tracked binaries, tracked Terraform state, files
  over GitHub's 100 MB limit, and credential-shaped strings.

## What good tests look like here

Test names describe the property being protected, and the failure message says
what broke rather than printing two values. Where a test exists because of a
past defect, say so in a comment — for example, `aws_db_instance` contains the
substring `instance` and would classify as a VM if the database rule stopped
taking precedence, so there is a test pinning that.

Security-relevant behaviour needs a test that fails when the protection is
removed. `TestReadCatalogFileRejectsDecompressionBomb` and
`TestSafeCatalogPathRejectsEscapes` are the models to copy.

## Changing mappings

Mapping quality is the product. Three layers exist, in precedence order:

1. **Curated** built-in mappings in `internal/translator/mappings.go`. These win
   over everything and are the only place a 1:N expansion can be expressed.
2. **Overrides** in [`catalog-overrides.json`](catalog-overrides.json), which
   supply authoritative equivalents for the generator.
3. **Generated** candidates from a catalog snapshot, admitted only above a
   conservative score and always emitted with a `TODO`.

After editing overrides or heuristics, regenerate mappings without touching the
network:

```bash
./bin/terra-translate catalog remap -catalog ./catalog -overrides ./catalog-overrides.json
```

A mapping that is structurally valid but semantically wrong is worse than no
mapping, because it produces configuration that plans cleanly and then behaves
differently. When in doubt, leave it unmapped and let the warning stand.

## Commits and reviews

Write commit messages that say what changed and why the change was needed. Every
path has a code owner, so a pull request needs a code owner review before it can
merge.

## The catalog

`catalog/` is tracked, generated data, refreshed weekly by
[`refresh-catalog.yml`](.github/workflows/refresh-catalog.yml). Do not hand-edit
snapshots. Read [`docs/catalog-operations.md`](docs/catalog-operations.md)
before changing how it is stored — the current arrangement has a growth cost
that needs managing.
