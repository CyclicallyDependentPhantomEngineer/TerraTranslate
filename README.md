# terra-translate

`terra-translate` is a migration assistant for translating Terraform modules
between cloud providers. It now exposes the translation engine through two
adapters:

- `terra-translate terraform` translates and verifies one Terraform module.
- `terra-translate terragrunt` discovers Terragrunt units and translates each
  unique local Terraform module once.

The generated configuration is a review artifact, not something to apply
blindly. Cross-cloud resources often differ structurally and semantically.

## Architecture

```text
 Terraform Registry + pinned providers
              │ scheduled/manual refresh
              ▼
 ┌──────────────────────────────────────────────┐
 │ Versioned catalog snapshot                   │
 │ raw schemas + normalized indexes + modules  │
 │ ranked AWS ↔ Google ↔ AzureRM mappings       │
 └──────────────────────────────────────────────┘

                       ┌────────────────────────┐
 Terraform module ────►│ Terraform CLI adapter  │─── fmt/syntax/validate checks
                       └───────────┬────────────┘
                                   │
 Terragrunt repo ─────►┌───────────▼────────────┐
  terragrunt.hcl       │ Terragrunt adapter      │
  local module sources│ discover → resolve      │
                       │ deduplicate → report    │
                       └───────────┬────────────┘
                                   │ module path
                         ┌─────────▼─────────┐
                         │ HCL parser and    │
                         │ classifier        │
                         └─────────┬─────────┘
                                   │
                         ┌─────────▼─────────┐
                         │ Cloud-neutral IR  │
                         │ + reference graph │
                         └─────────┬─────────┘
                                   │
                ┌──────────────────▼───────────────────┐
 catalog ──────►│ Mapping + PID feedback loop          │
 candidates     │ curated → generated → learned/fuzzy  │
                │ score → effort → next translation   │
                └──────────────────┬───────────────────┘
                                   │
                         ┌─────────▼─────────┐
                         │ HCL code generator│
                         └─────────┬─────────┘
                                   │
              main.tf + translation_report.json
```

### Why this is a companion CLI

Terraform's plugin protocol is designed for providers that manage remote APIs
and resources. A source-to-source migration tool belongs before Terraform's
normal `init`, `plan`, and `apply` workflow, so `terra-translate` is a companion
CLI rather than a provider plugin. See HashiCorp's
[plugin architecture](https://developer.hashicorp.com/terraform/plugin/how-terraform-works)
and [CLI workflow](https://developer.hashicorp.com/terraform/cli/run).

### Translation core

1. The parser reads a `.tf` file or all `.tf` files in one module directory.
2. Resources are classified into cloud-neutral IR classes such as
   `compute.instance`, `network.vpc`, and `storage.bucket`.
3. The translator applies exact resource/attribute mappings, rewrites resource
   references, supports selected 1:N expansions, and marks values requiring
   manual intervention.
4. A PID controller adjusts fuzzy-matching effort between iterations.
5. Code generation writes formatted HCL, preserves variables/locals/outputs,
   and writes a JSON report containing scores, PID history, and warnings.

The PID process variable is a weighted score:

```text
composite = 0.40 × coverage + 0.35 × validity + 0.25 × semantics
error     = 1.0 - composite
```

The loop stops at 99% composite accuracy, after two stalled measurements, or
after `-max-iter` iterations. The CLI exit threshold is based on mapped
attribute coverage and is independently configurable with `-min-accuracy`.

## Install

```bash
go mod download
go build -o terra-translate .
```

Put the resulting binary on `PATH` if it will be called from Terragrunt hooks.

For a complete copy-and-paste walkthrough, see [`setup.md`](setup.md). Runnable
minimal and realistic examples are indexed in [`example/README.md`](example/README.md).

## Terraform extension

Translate one module and run Terraform's formatting check:

```bash
terra-translate terraform \
  -input ./infra/aws \
  -output ./migration/gcp \
  -from auto \
  -to google \
  -fmt-check
```

`-from auto` infers `aws`, `google`, or `azurerm` from resource prefixes. The
original flag-only invocation remains supported for compatibility:

```bash
terra-translate -from aws -to google -input ./infra/aws -output ./migration/gcp
```

To ask Terraform to perform semantic validation as well:

```bash
terraform -chdir=./migration/gcp init -backend=false
terra-translate terraform \
  -input ./infra/aws \
  -output ./migration/gcp \
  -to google \
  -validate
```

`-validate` never runs `terraform init` automatically. This prevents the
translator from unexpectedly downloading providers or changing backend state.

### Terraform flags

| Flag | Default | Description |
|---|---:|---|
| `-input` | `.` | Input `.tf` file or module directory |
| `-output` | `./terra-translate-output` | Generated module directory |
| `-from` | `auto` | Source provider (`aws` in legacy mode) |
| `-to` | `google` | Target provider |
| `-schema` | | Optional provider-schema JSON path |
| `-catalog` | | Optional refreshed catalog used to extend built-in mappings |
| `-fmt-check` | `true` | Run `terraform fmt -check -diff`; defaults to `false` in the legacy flag-only invocation |
| `-validate` | `false` | Run `terraform validate` without init |
| `-terraform-bin` | `terraform` | Terraform-compatible binary used for checks |
| `-min-accuracy` | `0.90` | Coverage required for exit code 0; use `0` for report-only mode |
| `-kp`, `-ki`, `-kd` | `0.8`, `0.1`, `0.05` | PID gains |
| `-max-iter` | `8` | Maximum feedback iterations |
| `-v` | `false` | Print per-iteration scores |
| `-pid-report` | `false` | Print full PID history |

### Provider schema augmentation

After a first translation pass, initialize the generated target module so
Terraform can expose the writable attributes supported by the target provider:

```bash
terraform -chdir=./migration/gcp init -backend=false
terraform -chdir=./migration/gcp providers schema -json > target-provider-schema.json
terra-translate terraform \
  -input ./infra/aws \
  -output ./migration/gcp \
  -to google \
  -schema target-provider-schema.json
```

Schema attributes augment fuzzy target candidates; computed-only attributes
are excluded. Schema-driven matches are still marked TODO because a valid
attribute name does not guarantee equivalent semantics.

## Provider and module catalog

The catalog refresh is the data plane behind broader provider translation. It
resolves exact provider versions, initializes them in an isolated temporary
Terraform directory, and captures the complete JSON emitted by
[`terraform providers schema -json`](https://developer.hashicorp.com/terraform/cli/commands/providers/schema).
Alongside the untouched schema, it stores a normalized index of provider
configuration, managed resources, data sources, ephemeral resources,
functions, nested blocks, and attributes.

Run a full refresh with all Registry module summaries:

```bash
terra-translate catalog refresh \
  -output ./catalog \
  -modules=true \
  -module-limit=0 \
  -overrides ./catalog-overrides.json

terra-translate catalog status -catalog ./catalog
```

After changing manual overrides or mapping heuristics, regenerate only the six
mapping files without provider downloads or Registry requests:

```bash
terra-translate catalog remap \
  -catalog ./catalog \
  -overrides ./catalog-overrides.json
```

Remap snapshots reference the unchanged provider and module artifacts from the
previous full snapshot, preserving both immutability and reproducibility.

Provider versions are discovered through Terraform's documented
[Provider Registry protocol](https://developer.hashicorp.com/terraform/internals/provider-registry-protocol).
Module summaries are paginated from the documented
[Module Registry API](https://developer.hashicorp.com/terraform/registry/api-docs).
Pass `-module-details=true` to additionally retrieve every selected module's
detail record, including the Registry's extracted inputs, outputs,
dependencies, and resources. This option is deliberately off by default
because it requires one extra request per module.
Detail requests share the `-detail-rps` rate budget (10 requests per second by
default) and honor Registry `Retry-After` responses.

Each successful refresh creates an immutable snapshot and advances
`catalog/latest.json` atomically only after all artifacts are complete:

```text
catalog/
├── latest.json
└── snapshots/<timestamp>/
    ├── manifest.json
    ├── providers/{aws,google,azurerm}/{schema.json.gz,index.json.gz}
    ├── modules/{aws,google,azurerm}.json.gz
    └── mappings/<source>-to-<target>.json.gz
```

Large payloads are compressed losslessly; manifests and `latest.json` remain
plain JSON. The catalog loader decompresses mapping artifacts transparently.

The mapping generator produces all six directed AWS, Google, and AzureRM
provider pairs. It ranks resource, data-source, attribute, and module
candidates using normalized names, service categories, compatible types, and
schema overlap. [`catalog-overrides.json`](catalog-overrides.json) supplies
authoritative mappings for known equivalents. Generated mappings are admitted
to translation only above conservative score thresholds and are emitted with
TODOs for semantic review; built-in curated mappings always take precedence.

Use the current catalog while translating either Terraform or Terragrunt:

```bash
terra-translate terraform \
  -input ./infra/aws \
  -output ./migration/azure \
  -from aws \
  -to azurerm \
  -catalog ./catalog

terra-translate terragrunt \
  -root ./live \
  -output ./migration/azure-stack \
  -to azurerm \
  -catalog ./catalog
```

Translation never downloads providers or refreshes data implicitly. The
weekly GitHub Actions workflow in
[`refresh-catalog.yml`](.github/workflows/refresh-catalog.yml) performs that
work explicitly every Sunday and opens a pull request containing the new
snapshot. It can also be run manually, with full module-detail collection as
the default; manual runs can disable details for a faster schema-only check.
Scheduled runs always retain full module details. Old snapshots remain
available for reproducibility and diffs.

## Terragrunt extension

Translate every local module referenced by a Terragrunt repository:

```bash
terra-translate terragrunt \
  -root ./live \
  -output ./migration/gcp-stack \
  -from auto \
  -to google
```

The adapter:

1. Recursively finds `terragrunt.hcl` files while ignoring `.terragrunt-cache`,
   `.terraform`, Git metadata, hidden directories, and its output directory.
2. Resolves literal local `terraform.source` paths, including Terragrunt's
   `package//subdirectory` notation.
3. Translates shared source modules only once even when many units use them.
4. Preserves the Terraform module's variable and output interface so existing
   Terragrunt `inputs` still have a migration target.
5. Writes a stack report with unit status and source-to-output mappings.

Example output layout:

```text
migration/gcp-stack/
├── modules/
│   └── storage/
│       ├── main.tf
│       └── translation_report.json
└── terragrunt_translation_report.json
```

Remote sources and dynamic source expressions are skipped without downloading
code or evaluating dependency outputs. They make the command return exit code
2 by default. For a remote source, let Terragrunt materialize it and run the
Terraform adapter against the module in `.terragrunt-cache`, or clone the module
locally first. Use `-fail-on-skipped=false` only when skipped units are expected.

### Terragrunt hook mode

Terragrunt hooks run in the directory where Terraform/OpenTofu runs, which is
the materialized cache for a unit with `terraform.source`. This makes a hook a
useful report-only integration for remote modules:

```hcl
terraform {
  source = "git::https://example.com/iac-modules.git//app?ref=v1.2.0"

  before_hook "cross_cloud_translation_preview" {
    commands = ["plan"]
    execute = [
      "terra-translate", "terraform",
      "-input", ".",
      "-output", "${get_terragrunt_dir()}/.terra-translate-preview",
      "-from", "auto",
      "-to", "google",
      "-min-accuracy", "0"
    ]
  }
}
```

See Terragrunt's [hooks documentation](https://docs.terragrunt.com/features/units/hooks/)
for hook working-directory and exit-code behavior.

The repository includes a two-unit, shared-module example:

```bash
terragrunt hcl validate --working-dir example/terragrunt
terra-translate terragrunt \
  -root example/terragrunt \
  -output /tmp/terra-translate-example \
  -to google \
  -min-accuracy 0.5
```

A larger application-style example is available at
[`example/realtime/aws-web-platform`](example/realtime/aws-web-platform), with
a two-environment Terragrunt stack at
[`example/realtime/terragrunt`](example/realtime/terragrunt). See the
[`example` index](example/README.md) for copy-and-paste commands.

## Supported provider pairs

The built-in curated layer covers AWS→Google, AWS→AzureRM, and selected
Google→AWS resources. Supplying a refreshed catalog extends translation to all
six directed AWS, Google, and AzureRM pairs when a candidate clears the safety
threshold. A pair with neither curated nor accepted catalog mappings fails
explicitly instead of emitting wholly unmapped configuration.

## Output and exit codes

Each module produces:

```text
terra-translate-output/
├── main.tf
└── translation_report.json
```

| Code | Meaning |
|---:|---|
| `0` | Translation met `-min-accuracy` and requested checks passed |
| `2` | Partial translation or, for Terragrunt, skipped units requiring review |
| `1` | Parse, translation, generation, module, or verification failure |

Always review TODOs, warnings, provider-specific values, security/IAM behavior,
state migration, and the plan before applying generated code.

## Further reading

| Document | Contents |
|---|---|
| [`setup.md`](setup.md) | Copy-and-paste build and usage walkthrough |
| [`example/README.md`](example/README.md) | Runnable minimal and realistic examples |
| [`docs/catalog-operations.md`](docs/catalog-operations.md) | Catalog contents, storage cost, refresh and pruning |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Development workflow and what CI enforces |
| [`SECURITY.md`](SECURITY.md) | Trust boundaries and vulnerability reporting |
| [`AUDIT.md`](AUDIT.md) | Historical audit; its header records which findings are fixed and which still apply |

The commands in `README.md`, `setup.md`, and `example/README.md` are executed by
the `docs-are-executable` job in [`ci.yml`](.github/workflows/ci.yml), which
asserts the documented output files and exit codes on every pull request.
