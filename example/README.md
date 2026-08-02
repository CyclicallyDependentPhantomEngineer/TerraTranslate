# terra-translate examples

All runnable examples live under this directory:

- `terragrunt/` is a minimal two-unit stack that demonstrates local module
  discovery and shared-module deduplication.
- `realtime/aws-web-platform/` is a realistic reusable AWS application module
  with compute, networking, storage, database, and serverless resources.
- `realtime/terragrunt/` has development and production units that both use
  the realistic module.

Build the CLI first by following [`setup.md`](../setup.md).

## Minimal Terraform translation

```bash
./bin/terra-translate terraform \
  -input ./example/terragrunt/modules/storage \
  -output /tmp/terra-translate-storage-google \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0.5
```

## Minimal Terragrunt stack

```bash
./bin/terra-translate terragrunt \
  -root ./example/terragrunt \
  -output /tmp/terra-translate-terragrunt-google \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0.5
```

## Realistic Terraform module

```bash
./bin/terra-translate terraform \
  -input ./example/realtime/aws-web-platform \
  -output /tmp/terra-translate-realtime-google \
  -from aws \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0
```

## Realistic Terragrunt stack

```bash
./bin/terra-translate terragrunt \
  -root ./example/realtime/terragrunt \
  -output /tmp/terra-translate-realtime-stack-google \
  -from aws \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0
```

Every generated module is a migration draft. Review its TODOs, report, target
provider values, IAM and security behavior, and Terraform plan before applying.
