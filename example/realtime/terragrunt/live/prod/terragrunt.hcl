terraform {
  source = "../../../aws-web-platform"
}

inputs = {
  environment        = "production"
  availability_zone  = "us-east-1a"
  ami_id             = "ami-replace-with-a-current-image"
  instance_type      = "m6i.large"
  assets_bucket_name = "terra-translate-production-assets"
  db_password        = get_env("TF_VAR_db_password", "replace-through-a-secret-store")
  lambda_role_arn    = "arn:aws:iam::123456789012:role/event-processor"
  lambda_code_bucket = "terra-translate-production-releases"
}
