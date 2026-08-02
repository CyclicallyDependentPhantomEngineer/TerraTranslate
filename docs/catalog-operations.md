# Operating the catalog

The catalog is the data plane behind provider translation. This document covers
what it contains, what it costs to keep in git, and how to keep that cost from
becoming a problem. For what the catalog is *for*, see the "Provider and module
catalog" section of [`README.md`](../README.md).

## What a snapshot contains

```text
catalog/
├── latest.json                        # atomic pointer to the current snapshot
└── snapshots/<timestamp>/
    ├── manifest.json                  # versions, counts, artifact paths
    ├── providers/{aws,google,azurerm}/
    │   ├── schema.json.gz             # verbatim `terraform providers schema -json`
    │   └── index.json.gz              # normalized resource/attribute index
    ├── modules/{aws,google,azurerm}.json.gz   # Registry module metadata
    └── mappings/<source>-to-<target>.json.gz  # six directed pairs
```

Measured sizes for the snapshot committed today:

| Artifact | On disk | Decompressed |
|---|---:|---:|
| `modules/aws.json.gz` | 36 MB | 310 MB |
| `modules/azurerm.json.gz` | 9.2 MB | 82 MB |
| `modules/google.json.gz` | 6.8 MB | 56 MB |
| `providers/aws/schema.json.gz` | 2.3 MB | 110 MB |
| `providers/aws/index.json.gz` | 1.3 MB | 53 MB |
| all six `mappings/*.json.gz` | 792 KB | 15 MB |
| **full snapshot** | **~59 MB** | — |

Two facts follow from that table.

**Translation only reads `mappings/`.** `LoadMapping` resolves one
`<source>-to-<target>.json.gz` and nothing else. The 59 MB of provider schemas
and module records exist to *generate* those mappings; `catalog remap` consumes
them, `terra-translate terraform` does not.

**Decompressed sizes are large enough to matter.** The AWS module artifact
expands to 310 MB and is unmarshalled whole. `readCatalogFile` enforces a
ceiling so a malicious or corrupt artifact cannot expand without bound, but the
legitimate memory cost of `catalog remap` is still hundreds of megabytes.

## The growth cost of tracking snapshots

`catalog/snapshots/**` is committed, and
[`refresh-catalog.yml`](../.github/workflows/refresh-catalog.yml) opens a pull
request with a **new full snapshot every Sunday**. Git stores each snapshot's
compressed blobs essentially in full, because gzip output does not delta-compress
against the previous week's gzip output.

```text
~59 MB per snapshot × 52 weeks ≈ 3 GB of history per year
```

That will push — no single file approaches GitHub's 100 MB per-file hard limit —
but it crosses GitHub's recommended 1 GB repository size within about four
months, and clone times grow with it. History cannot be shrunk later without a
rewrite that invalidates every existing clone.

Decide deliberately which of these you want:

**Keep every snapshot.** Full reproducibility: any past translation can be
re-run against the exact data that produced it. Accept the growth and plan a
history rewrite eventually.

**Prune on a schedule.** Keep the newest N snapshots in git and delete older
ones in the same pull request the refresh opens. Reproducibility is retained for
the retention window only. This is a small change to the refresh workflow:

```bash
# Keep the four most recent snapshots.
ls -1d catalog/snapshots/*/ | sort | head -n -4 | xargs -r rm -rf
```

**Track mappings, publish the rest.** Commit `mappings/`, `manifest.json`, and
`latest.json` — about 800 KB per snapshot, so roughly 40 MB per year — and
attach the provider and module artifacts to a GitHub release instead. Cloning
stays cheap and translation still works offline out of the box, but
`catalog remap` needs the release assets downloaded first.

The repository currently uses the first option. Nothing in the code depends on
that choice; it is a storage policy, and `docs-are-executable` in CI will tell
you immediately if a change to it breaks a documented command.

## Running a refresh yourself

A full refresh resolves provider versions, initialises them in an isolated
temporary directory, captures schemas, pages through the Registry, and
regenerates all six mapping files. It takes tens of minutes and needs network
access and Terraform on `PATH`.

```bash
./bin/terra-translate catalog refresh \
  -output ./catalog \
  -modules=true \
  -module-limit=0 \
  -module-details=true \
  -detail-rps=10 \
  -overrides ./catalog-overrides.json
```

Useful flags:

| Flag | Default | Why you would change it |
|---|---:|---|
| `-module-details` | `false` | `true` collects inputs/outputs/resources per module; one extra request each, so it dominates runtime |
| `-module-limit` | `0` | A small number gives a fast partial refresh for testing |
| `-detail-rps` | `10` | Registry request budget; `Retry-After` is always honoured |
| `-detail-workers` | `6` | Concurrency for detail requests |
| `-aws-version` etc. | `latest` | Pin exact provider versions for a reproducible snapshot |
| `-registry-url` | `https://registry.terraform.io` | Point at a private registry |

A refresh is atomic: `latest.json` advances only after every artifact is
complete, so an interrupted refresh leaves the previous snapshot current.

## Regenerating mappings without the network

After editing [`catalog-overrides.json`](../catalog-overrides.json) or the
ranking heuristics:

```bash
./bin/terra-translate catalog remap -catalog ./catalog -overrides ./catalog-overrides.json
```

This reads the stored provider indexes and module records from the previous
snapshot and writes a new snapshot containing only regenerated `mappings/`,
referencing the unchanged artifacts. It makes no Registry requests and downloads
no providers.

## Checking what you have

```bash
./bin/terra-translate catalog status -catalog ./catalog
```

```text
Catalog snapshot: 20260802T043246.201269000Z (2026-08-02T04:32:46Z)
Terraform: Terraform v1.15.8
  aws      v6.57.1       resources=1687 data_sources=673 ephemeral=10 functions=4 modules=11190 detailed=11190
  azurerm  v5.0.1        resources=1103 data_sources=394 ephemeral=2  functions=2 modules=2499  detailed=2499
  google   v7.42.0       resources=1330 data_sources=461 ephemeral=6  functions=6 modules=1377  detailed=1377
Generated mappings: 6 provider pairs
```

`detailed=` equal to `modules=` means the snapshot was taken with
`-module-details=true`. A lower number means module detail records are partial,
which weakens module-level mapping candidates but does not affect resource or
attribute mappings.
