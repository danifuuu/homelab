variable "github_token" {
  description = "GitHub personal access token with repo and workflow permissions"
  type        = string
  sensitive   = true
}

variable "github_org" {
  description = "GitHub organization or username where the repository will be created"
  type        = string
}

variable "repository_name" {
  description = "Name of the GitHub repository for GitOps"
  type        = string
  default     = "homelab"
}