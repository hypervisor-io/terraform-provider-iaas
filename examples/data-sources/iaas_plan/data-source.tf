# Look up a plan by name within a location. The data source hides the catalog's
# plan-group nesting; pass plan_group only when the same name spans groups.
data "iaas_location" "nyc" {
  name = "nyc"
}

data "iaas_plan" "small" {
  location_id = data.iaas_location.nyc.id
  name        = "s1.small"

  # plan_group = "general" # optional disambiguator
}

output "plan_id" {
  value = data.iaas_plan.small.id
}
