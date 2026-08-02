output "vpc_id" {
  description = "Application VPC ID"
  value       = aws_vpc.main.id
}

output "web_instance_id" {
  description = "Web EC2 instance ID"
  value       = aws_instance.web.id
}

output "web_public_ip" {
  description = "Public IP address of the web instance"
  value       = aws_instance.web.public_ip
}

output "assets_bucket_id" {
  description = "Application asset bucket ID"
  value       = aws_s3_bucket.assets.id
}

output "database_address" {
  description = "PostgreSQL database endpoint"
  value       = aws_db_instance.app.address
  sensitive   = true
}

output "processor_arn" {
  description = "Event processor Lambda ARN"
  value       = aws_lambda_function.processor.arn
}
