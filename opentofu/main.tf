terraform {
  required_providers {
    flux = {
      source  = "fluxcd/flux"
      version = ">= 1.8.3"
    }
    github = {
      source  = "integrations/github"
      version = ">= 6.11.1"
    }
  }
}

# 1. Configure the GitHub Provider
provider "github" {
  token = var.github_token
  owner = var.github_org
}

# 2. Configure the Flux Provider (Points to Talos Cluster)
provider "flux" {
  kubernetes = {
    config_path = "../talos/kubeconfig"
  }
  git = {
    url = "https://github.com/${var.github_org}/${var.repository_name}.git"
    http = {
      username = "git"
      password = var.github_token
    }
  }
}

# 3. Create/Manage the GitHub Repository
data "github_repository" "main" {
  name        = var.repository_name
}

# 4. Bootstrap Flux into the Cluster
resource "flux_bootstrap_git" "this" {
  depends_on = [data.github_repository.main]
  path       = "k8s"
}