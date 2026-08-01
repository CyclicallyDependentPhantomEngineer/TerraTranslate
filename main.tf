variable "environment" {
  description = "Deployment environment"
  default     = "production"
}

variable "region" {
  description = "AWS region"
  default     = "us-east-1"
}

resource "aws_vpc" "main" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = {
    Name        = "main-vpc"
    Environment = "production"
  }
}

resource "aws_subnet" "app" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.0.1.0/24"
  availability_zone = "us-east-1a"

  tags = {
    Name = "app-subnet"
  }
}

resource "aws_security_group" "web" {
  name        = "web-sg"
  description = "Allow HTTP and HTTPS inbound"
  vpc_id      = aws_vpc.main.id

  tags = {
    Name        = "web-sg"
    Environment = "production"
  }
}

resource "aws_instance" "web" {
  ami                  = "ami-0c55b159cbfafe1f0"
  instance_type        = "t3.medium"
  subnet_id            = aws_subnet.app.id
  key_name             = "deployer-key"
  availability_zone    = "us-east-1a"
  iam_instance_profile = "web-server-profile"

  user_data = <<-EOF
    #!/bin/bash
    apt-get update
    apt-get install -y nginx
    systemctl start nginx
  EOF

  tags = {
    Name        = "web-server"
    Environment = "production"
    Role        = "web"
  }

  lifecycle {
    create_before_destroy = true
    prevent_destroy       = false
  }
}

resource "aws_s3_bucket" "assets" {
  bucket        = "my-app-assets-bucket"
  force_destroy = false

  tags = {
    Name        = "assets"
    Environment = "production"
  }
}

resource "aws_db_instance" "app_db" {
  identifier             = "app-database"
  engine                 = "postgres"
  engine_version         = "14"
  instance_class         = "db.t3.medium"
  allocated_storage      = 100
  username               = "appuser"
  password               = "changeme"
  multi_az               = true
  backup_retention_period = 7
  skip_final_snapshot    = true

  tags = {
    Name        = "app-db"
    Environment = "production"
  }
}

resource "aws_lambda_function" "processor" {
  function_name = "event-processor"
  runtime       = "python3.11"
  handler       = "handler.main"
  memory_size   = 512
  timeout       = 30

  environment {
    variables = {
      ENV    = "production"
      REGION = "us-east-1"
    }
  }

  tags = {
    Name = "event-processor"
  }
}

output "vpc_id" {
  value       = aws_vpc.main.id
  description = "The VPC ID"
}

output "web_instance_ip" {
  value       = aws_instance.web.public_ip
  description = "Public IP of the web server"
}
