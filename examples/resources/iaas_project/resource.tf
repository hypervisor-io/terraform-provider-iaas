# Minimal project - only name is required.
resource "iaas_project" "minimal" {
  name = "production"
}

# Full project with an optional description and UI color badge.
resource "iaas_project" "full" {
  name        = "staging"
  description = "Staging infrastructure for pre-production testing"
  color       = "#F59E0B"
}
