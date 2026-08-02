variable "bucket_name" {
  description = "Name of the source AWS bucket"
  type        = string
}

resource "aws_s3_bucket" "assets" {
  bucket        = var.bucket_name
  force_destroy = false

  tags = {
    ManagedBy = "terragrunt"
  }
}

output "bucket_id" {
  value = aws_s3_bucket.assets.id
}
