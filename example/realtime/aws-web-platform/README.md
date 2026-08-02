# Realistic AWS web-platform module

This reusable example models a small application platform with a VPC, public
and private subnets, security rules, an EC2 web server, an S3 asset bucket, a
PostgreSQL RDS instance, and a Lambda event processor. It intentionally
includes references, nested blocks, sensitive inputs, lifecycle settings, and
resources with imperfect cross-cloud equivalents so the translation report is
representative of a real migration review.

The example is safe to translate locally. Do not apply it with placeholder
values from `terraform.tfvars.example`.

From the repository root, translate it to Google:

```bash
./bin/terra-translate terraform \
  -input ./example/realtime/aws-web-platform \
  -output /private/tmp/terra-translate-realtime-google \
  -from aws \
  -to google \
  -catalog ./catalog \
  -min-accuracy 0
```

Translate it to AzureRM:

```bash
./bin/terra-translate terraform \
  -input ./example/realtime/aws-web-platform \
  -output /private/tmp/terra-translate-realtime-azure \
  -from aws \
  -to azurerm \
  -catalog ./catalog \
  -min-accuracy 0
```

Inspect the generated HCL, TODO comments, and `translation_report.json` before
initializing or planning either target module.
