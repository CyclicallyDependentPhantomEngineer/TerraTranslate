# Security policy

## Reporting a vulnerability

Report suspected vulnerabilities through GitHub's private vulnerability
reporting on this repository (Security → Report a vulnerability). Please do not
open a public issue for anything exploitable.

Include the command you ran, the input that triggered it, and what you expected
instead. A minimal Terraform module or catalog directory that reproduces the
problem is the most useful thing you can attach.

## What this tool is, in security terms

`terra-translate` reads Terraform and Terragrunt configuration, reads or writes
a catalog directory, optionally makes HTTP requests to a Terraform Registry, and
optionally shells out to a Terraform-compatible binary. It never applies
infrastructure, never reads Terraform state, and never handles cloud
credentials.

Its output is a **migration draft for human review**, not something to apply.
Generated modules carry `TODO` comments wherever provider semantics differ, and
`AUDIT.md` records the semantic gaps that no amount of mapping can close.

## Trust boundaries

| Input | Trust | Notes |
|---|---|---|
| Source `.tf` / `terragrunt.hcl` | Untrusted | Parsed, never evaluated. Dynamic expressions become `TODO` markers rather than being executed. |
| `-catalog` directory | Untrusted | Paths inside it are confined to the catalog root, and compressed artifacts are read under an explicit size ceiling. |
| `-registry-url` | Operator-supplied | The tool will talk to whatever host you point it at, by design, so private registries work. Point it somewhere you trust. |
| `-terraform-bin` | Operator-supplied | Executed as a subprocess. Equivalent to running that binary yourself. |
| `-schema` JSON | Untrusted | Parsed as data only. |

Two properties are enforced in code and covered by tests:

- **Catalog paths cannot escape the catalog root.** `safeCatalogPath` rejects
  absolute paths and any relative path that resolves outside the root, so a
  crafted `latest.json` cannot make the tool read `/etc/passwd`.
- **Compressed catalog artifacts have a decompressed ceiling.** A small archive
  of repeated bytes expands by three orders of magnitude; `readCatalogFile`
  reads through a limit and fails rather than expanding without bound.

## Things this tool deliberately does not do

- It never runs `terraform init`. `-validate` requires you to have initialised
  the output directory yourself, so translation cannot download providers or
  touch backend state as a side effect.
- It never downloads remote Terragrunt sources. Remote and dynamic
  `terraform.source` values are reported as skipped units.
- It never refreshes the catalog implicitly. Network access happens only when
  you run `catalog refresh`.

## Reviewing generated output

Generated configuration is the least trustworthy artifact in the pipeline. Before
applying anything:

- Read every `TODO` and every warning in `translation_report.json`.
- Re-derive IAM, security group, firewall, and public-access settings by hand.
  Cross-cloud access models are structurally different, and a plausible-looking
  translation can silently widen access.
- Check that secrets have not been carried across as literals. Values such as
  database passwords should come from a secret store in the target module, not
  from the translated source.
- Run `terraform plan` and read it in full.
