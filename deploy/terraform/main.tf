locals {
  public_url = var.domain != "" ? "https://${var.domain}" : null

  # Values written into an env file on the server. Single quotes keep the
  # values literal for Docker Compose, so they must not contain one.
  config_env = {
    SITE_ADDRESS      = var.domain != "" ? var.domain : ":80"
    SLACK_WEBHOOK_URL = var.slack_webhook_url
    SMTP_HOST         = var.smtp.host
    SMTP_PORT         = tostring(var.smtp.port)
    SMTP_USERNAME     = var.smtp.username
    SMTP_PASSWORD     = var.smtp.password
    SMTP_FROM         = var.smtp.from
    SMTP_TO           = var.smtp.to
  }
}

resource "hcloud_ssh_key" "admin" {
  name       = "${var.name}-admin"
  public_key = var.ssh_public_key
}

resource "hcloud_firewall" "web" {
  name = "${var.name}-fw"

  rule {
    description = "SSH from trusted ranges only"
    direction   = "in"
    protocol    = "tcp"
    port        = "22"
    source_ips  = var.ssh_allowed_cidrs
  }

  rule {
    description = "HTTP (ACME challenges and redirect to HTTPS)"
    direction   = "in"
    protocol    = "tcp"
    port        = "80"
    source_ips  = ["0.0.0.0/0", "::/0"]
  }

  rule {
    description = "HTTPS"
    direction   = "in"
    protocol    = "tcp"
    port        = "443"
    source_ips  = ["0.0.0.0/0", "::/0"]
  }

  rule {
    description = "Ping"
    direction   = "in"
    protocol    = "icmp"
    source_ips  = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_server" "app" {
  name         = var.name
  server_type  = var.server_type
  image        = var.image
  location     = var.location
  ssh_keys     = [hcloud_ssh_key.admin.id]
  firewall_ids = [hcloud_firewall.web.id]
  backups      = var.enable_backups

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }

  user_data = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    repo_url      = var.repo_url
    git_ref       = var.git_ref
    public_url    = local.public_url
    monitors_yaml = var.monitors_yaml
    config_env    = local.config_env
  })

  labels = {
    app = "uptime-monitor"
  }

  lifecycle {
    # user_data only runs on first boot, and changing it would force a new
    # server (losing the database). Roll out config changes over SSH instead.
    ignore_changes = [user_data]

    precondition {
      condition     = alltrue([for v in values(local.config_env) : !strcontains(v, "'")])
      error_message = "Configuration values must not contain single quotes."
    }
  }
}
