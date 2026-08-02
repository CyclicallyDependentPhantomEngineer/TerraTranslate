variable "environment" {
  description = "Deployment environment name"
  type        = string
  default     = "development"
}

variable "region" {
  description = "AWS region used by the calling provider configuration"
  type        = string
  default     = "us-east-1"
}

variable "vpc_cidr" {
  description = "CIDR range for the application VPC"
  type        = string
  default     = "10.20.0.0/16"
}

variable "public_subnet_cidr" {
  description = "CIDR range for the public web subnet"
  type        = string
  default     = "10.20.10.0/24"
}

variable "private_subnet_cidr" {
  description = "CIDR range for the private database subnet"
  type        = string
  default     = "10.20.20.0/24"
}

variable "secondary_private_subnet_cidr" {
  description = "CIDR range for the second database subnet"
  type        = string
  default     = "10.20.30.0/24"
}

variable "availability_zone" {
  description = "Availability zone used by the example"
  type        = string
  default     = "us-east-1a"
}

variable "secondary_availability_zone" {
  description = "Second availability zone required by the RDS subnet group"
  type        = string
  default     = "us-east-1b"
}

variable "ami_id" {
  description = "AMI used by the web instance"
  type        = string
}

variable "instance_type" {
  description = "EC2 instance size"
  type        = string
  default     = "t3.medium"
}

variable "assets_bucket_name" {
  description = "Globally unique S3 bucket name for application assets"
  type        = string
}

variable "db_username" {
  description = "Application database administrator username"
  type        = string
  default     = "appuser"
}

variable "db_password" {
  description = "Application database administrator password"
  type        = string
  sensitive   = true
}

variable "lambda_role_arn" {
  description = "Existing IAM role assumed by the event processor"
  type        = string
}

variable "lambda_code_bucket" {
  description = "Existing S3 bucket containing the Lambda deployment archive"
  type        = string
}

variable "lambda_code_key" {
  description = "Object key of the Lambda deployment archive"
  type        = string
  default     = "releases/event-processor.zip"
}
