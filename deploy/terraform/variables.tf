variable "hcloud_token" {
  description = "Hetzner Cloud API token (read/write). Prefer TF_VAR_hcloud_token over a tfvars file."
  type        = string
  sensitive   = true
}

variable "name" {
  description = "Name prefix for all resources."
  type        = string
  default     = "uptime-monitor"
}

variable "location" {
  description = "Hetzner location, e.g. nbg1, fsn1, hel1, ash."
  type        = string
  default     = "nbg1"
}

variable "server_type" {
  description = "Server size. The smallest shared-vCPU type is plenty for hundreds of monitors."
  type        = string
  default     = "cx22"
}

variable "image" {
  description = "OS image. cloud-init assumes Ubuntu (apt package names)."
  type        = string
  default     = "ubuntu-24.04"
}

variable "ssh_public_key" {
  description = "Public key installed for root SSH access."
  type        = string
}

variable "ssh_allowed_cidrs" {
  description = "CIDRs allowed to reach SSH. No default on purpose: choose your own IP range."
  type        = list(string)

  validation {
    condition     = length(var.ssh_allowed_cidrs) > 0
    error_message = "Provide at least one CIDR, for example your home IP as x.x.x.x/32."
  }
}

variable "repo_url" {
  description = "Git URL the server clones to build the app."
  type        = string
}

variable "git_ref" {
  description = "Branch or tag to deploy."
  type        = string
  default     = "main"
}

variable "domain" {
  description = "Domain pointing at the server. When set, Caddy obtains a TLS certificate for it; when empty, the page is served over plain HTTP on the IP."
  type        = string
  default     = ""
}

variable "monitors_yaml" {
  description = "Contents of the monitors file synced at startup. Monitors can also be managed via the API."
  type        = string
  default     = "monitors: []\n"
}

variable "slack_webhook_url" {
  description = "Optional Slack incoming webhook URL for alerts."
  type        = string
  default     = ""
  sensitive   = true
}

variable "smtp" {
  description = "Optional SMTP settings for email alerts. Leave host empty to disable."
  type = object({
    host     = string
    port     = number
    username = string
    password = string
    from     = string
    to       = string # comma-separated
  })
  default = {
    host     = ""
    port     = 587
    username = ""
    password = ""
    from     = ""
    to       = ""
  }
  sensitive = true
}

variable "enable_backups" {
  description = "Enable Hetzner's daily server backups (adds 20% to the server price)."
  type        = bool
  default     = false
}
