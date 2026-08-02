locals {
  common_tags = {
    Application = "web-platform"
    Environment = var.environment
    ManagedBy   = "terraform"
  }
}

resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(local.common_tags, {
    Name = "${var.environment}-web-vpc"
  })
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = var.public_subnet_cidr
  availability_zone       = var.availability_zone
  map_public_ip_on_launch = true

  tags = merge(local.common_tags, {
    Name = "${var.environment}-public"
    Tier = "public"
  })
}

resource "aws_subnet" "private" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = var.private_subnet_cidr
  availability_zone = var.availability_zone

  tags = merge(local.common_tags, {
    Name = "${var.environment}-private"
    Tier = "private"
  })
}

resource "aws_subnet" "private_secondary" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = var.secondary_private_subnet_cidr
  availability_zone = var.secondary_availability_zone

  tags = merge(local.common_tags, {
    Name = "${var.environment}-private-secondary"
    Tier = "private"
  })
}

resource "aws_security_group" "web" {
  name        = "${var.environment}-web"
  description = "Allow public HTTP and HTTPS traffic"
  vpc_id      = aws_vpc.main.id

  ingress {
    description = "HTTP"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.common_tags
}

resource "aws_security_group" "database" {
  name        = "${var.environment}-database"
  description = "Allow PostgreSQL from the web tier"
  vpc_id      = aws_vpc.main.id

  ingress {
    description     = "PostgreSQL from web instances"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.web.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.common_tags
}

resource "aws_instance" "web" {
  ami                         = var.ami_id
  instance_type               = var.instance_type
  subnet_id                   = aws_subnet.public.id
  vpc_security_group_ids      = [aws_security_group.web.id]
  availability_zone           = var.availability_zone
  associate_public_ip_address = true

  user_data = <<-USER_DATA
    #!/usr/bin/env bash
    set -euo pipefail
    apt-get update
    apt-get install -y nginx
    echo "terra-translate ${var.environment}" > /var/www/html/index.html
    systemctl enable --now nginx
  USER_DATA

  tags = merge(local.common_tags, {
    Name = "${var.environment}-web"
    Role = "frontend"
  })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_s3_bucket" "assets" {
  bucket        = var.assets_bucket_name
  force_destroy = false

  tags = merge(local.common_tags, {
    Name = "${var.environment}-assets"
  })
}

resource "aws_s3_bucket_public_access_block" "assets" {
  bucket = aws_s3_bucket.assets.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_db_subnet_group" "app" {
  name       = "${var.environment}-database"
  subnet_ids = [aws_subnet.private.id, aws_subnet.private_secondary.id]

  tags = local.common_tags
}

resource "aws_db_instance" "app" {
  identifier              = "${var.environment}-app"
  engine                  = "postgres"
  engine_version          = "16"
  instance_class          = "db.t3.medium"
  allocated_storage       = 20
  db_subnet_group_name    = aws_db_subnet_group.app.name
  vpc_security_group_ids  = [aws_security_group.database.id]
  username                = var.db_username
  password                = var.db_password
  backup_retention_period = 7
  multi_az                = false
  skip_final_snapshot     = true

  tags = merge(local.common_tags, {
    Name = "${var.environment}-database"
  })
}

resource "aws_lambda_function" "processor" {
  function_name = "${var.environment}-event-processor"
  role          = var.lambda_role_arn
  runtime       = "python3.12"
  handler       = "handler.main"
  memory_size   = 512
  timeout       = 30
  s3_bucket     = var.lambda_code_bucket
  s3_key        = var.lambda_code_key

  environment {
    variables = {
      APP_ENV       = var.environment
      ASSETS_BUCKET = aws_s3_bucket.assets.id
    }
  }

  tags = local.common_tags
}
