# Build and use terra-translate

## 1. Prerequisites

You need:

- Go 1.21 or newer
- Terraform CLI on `PATH`
- Terragrunt only if translating a Terragrunt repository
- Internet access only when refreshing the catalog

Confirm:

```bash
go version
terraform version
terragrunt --version  # optional
```

## 2. Build

```bash
git clone https://github.com/CyclicallyDependentPhantomEngineer/TerraTranslate.git
cd TerraTranslate

go mod download
go test ./...

mkdir -p bin
go build -trimpath -o ./bin/terra-translate .
```

Re-run the `go build` command whenever the source changes or after pulling new
commits. The executable in `bin/` is not updated automatically.

Verify:

```bash
./bin/terra-translate help
```

If your environment denies access to the default Go build cache, point
`GOCACHE` somewhere writable:

```bash
GOCACHE="$(mktemp -d)" go build -trimpath -o ./bin/terra-translate .
```

Optionally put it on your `PATH`:

```bash
export PATH="$PWD/bin:$PATH"
```

Then you can use `terra-translate` instead of `./bin/terra-translate`.

## 3. Check the included catalog

A complete catalog is already stored in the repository:

```bash
./bin/terra-translate catalog status -catalog ./catalog
```

It contains AWS, Google, and AzureRM provider schemas, all Registry module
details, and mappings for all six directions.

## 4. Test your first module translation

Start with the repository's small
[`storage` test module](example/terragrunt/modules/storage/main.tf). It contains
one AWS S3 bucket, a variable, tags, and an output, making it the quickest way
to verify parsing, provider detection, catalog loading, reference rewriting,
code generation, and Terraform formatting.

From the repository root, translate the test module from AWS to Google:

```bash
./bin/terra-translate terraform \
  -input ./example/terragrunt/modules/storage \
  -output /tmp/terra-translate-first-module \
  -from auto \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0.5
```

`-from auto` detects AWS from the `aws_s3_bucket` resource. A successful run
prints the mapped coverage and the paths of both generated files.

Inspect the result:

```bash
terraform -chdir=/tmp/terra-translate-first-module fmt -check
sed -n '1,240p' /tmp/terra-translate-first-module/main.tf
sed -n '1,240p' /tmp/terra-translate-first-module/translation_report.json
```

The output should contain:

```text
/tmp/terra-translate-first-module/
├── main.tf
└── translation_report.json
```

## 5. Translate other Terraform modules

General command:

```bash
./bin/terra-translate terraform \
  -input /path/to/source-module \
  -output /path/to/generated-module \
  -from auto \
  -to google \
  -catalog ./catalog
```

Valid provider names are:

```text
aws
google
azurerm
```

### Try the realistic AWS module with Google

```bash
./bin/terra-translate terraform \
  -input ./example/realtime/aws-web-platform \
  -output /tmp/terra-translate-realtime-google \
  -from aws \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0
```

### Try the realistic AWS module with AzureRM

```bash
./bin/terra-translate terraform \
  -input ./example/realtime/aws-web-platform \
  -output /tmp/terra-translate-realtime-azure \
  -from aws \
  -to azurerm \
  -catalog ./catalog \
  -min-accuracy 0
```

The generated HCL contains TODO comments wherever provider semantics or values
require review. The realistic module intentionally demonstrates partial
coverage and resources without exact cross-cloud equivalents.

## 6. Validate generated Terraform

After completing the first-module translation above, initialize its generated
Google module without a backend:

```bash
terraform -chdir=/tmp/terra-translate-first-module init -backend=false
```

Rerun translation with Terraform validation enabled:

```bash
./bin/terra-translate terraform \
  -input ./example/terragrunt/modules/storage \
  -output /tmp/terra-translate-first-module \
  -from auto \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0.5 \
  -validate
```

You can then inspect a plan after configuring Google credentials and supplying
the test module's bucket name:

```bash
terraform -chdir=/tmp/terra-translate-first-module plan \
  -var='bucket_name=replace-with-a-globally-unique-name'
```

The tool never runs `terraform init` automatically.

If Terraform is not installed and you only want translation output:

```bash
./bin/terra-translate terraform \
  -input ./example/terragrunt/modules/storage \
  -output /tmp/terra-translate-first-module \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0.5 \
  -fmt-check=false
```

## 7. Translate a Terragrunt repository

```bash
./bin/terra-translate terragrunt \
  -root ./live \
  -output ./translated/google-stack \
  -from auto \
  -to google \
  -catalog ./catalog
```

Output structure:

```text
translated/google-stack/
├── modules/
│   └── ...
└── terragrunt_translation_report.json
```

The Terragrunt adapter:

- Discovers `terragrunt.hcl` files recursively.
- Resolves literal local `terraform.source` paths.
- Translates shared modules only once.
- Preserves variables and outputs.
- Skips dynamic and remote sources without downloading them.

If skipped remote modules are expected:

```bash
./bin/terra-translate terragrunt \
  -root ./live \
  -output ./translated/google-stack \
  -to google \
  -catalog ./catalog \
  -fail-on-skipped=false
```

## 8. Refresh all provider and module data

A full refresh downloads current providers and queries every Registry module.
It can take tens of minutes:

```bash
./bin/terra-translate catalog refresh \
  -output ./catalog \
  -modules=true \
  -module-limit=0 \
  -module-details=true \
  -detail-workers=6 \
  -detail-rps=10 \
  -overrides ./catalog-overrides.json
```

Check the result:

```bash
./bin/terra-translate catalog status -catalog ./catalog
```

To collect provider schemas and module summaries without thousands of detail
requests:

```bash
./bin/terra-translate catalog refresh \
  -output ./catalog \
  -modules=true \
  -module-limit=0 \
  -module-details=false
```

To pin versions rather than selecting the latest:

```bash
./bin/terra-translate catalog refresh \
  -output ./catalog \
  -aws-version 6.57.1 \
  -google-version 7.42.0 \
  -azurerm-version 5.0.1 \
  -module-details=true
```

## 9. Edit mappings without downloading anything

Edit [`catalog-overrides.json`](catalog-overrides.json), then run:

```bash
./bin/terra-translate catalog remap \
  -catalog ./catalog \
  -overrides ./catalog-overrides.json
```

This regenerates all six mapping files using the stored schemas and module
records.

## 10. Important flags

```text
-input           Source .tf file or module directory
-output          Generated module directory
-from            auto, aws, google, or azurerm
-to              aws, google, or azurerm
-catalog         Refreshed catalog directory
-min-accuracy    Required mapped-attribute ratio; default 0.90
-fmt-check       Run terraform fmt -check; default true for
                 `terra-translate terraform`, false in legacy flag-only mode
-validate        Run terraform validate; output must already be initialized
-v               Print PID iterations
-pid-report      Print the full PID history
```

Full help:

```bash
./bin/terra-translate terraform -help
./bin/terra-translate terragrunt -help
./bin/terra-translate catalog refresh -help
./bin/terra-translate catalog remap -help
```

Exit codes are:

- `0`: required coverage achieved and checks passed
- `2`: partial translation or skipped Terragrunt units
- `1`: parsing, translation, catalog, or verification failure

Always review TODOs, IAM/security behavior, target-region values, state
migration, and the Terraform plan before applying generated code. See
[`README.md`](README.md) for the full architecture.
