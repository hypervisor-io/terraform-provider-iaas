# iaas_project_assignment links ONE resource (instance, vpc, load_balancer,
# s3_bucket, or managed_database) to ONE iaas_project. It is a STANDALONE
# resource, not an attribute on the resource being assigned: the assignment
# endpoint (POST /project/assign-resource) is an orthogonal tagging action,
# not part of the underlying resource's own create/update lifecycle, and this
# shape lets you assign a resource you didn't create with this Terraform run
# (e.g. one adopted via `terraform import`) without importing its entire
# other state too.
#
# Every attribute is immutable (RequiresReplace) — there is no "move"
# operation, only assign/unassign. Changing project_id, resource_type, or
# resource_id unassigns the old link and assigns a new one.

resource "iaas_project" "production" {
  name = "production"
}

resource "iaas_instance" "web" {
  # ... instance configuration ...
}

resource "iaas_project_assignment" "web_in_production" {
  project_id    = iaas_project.production.id
  resource_type = "instance"
  resource_id   = iaas_instance.web.id
}

# resource_type must be one of exactly: instance, vpc, load_balancer,
# s3_bucket, managed_database — the set ProjectController's assign-resource
# endpoint accepts. Assigning a VPC works the same way:
resource "iaas_vpc" "main" {
  # ... vpc configuration ...
}

resource "iaas_project_assignment" "vpc_in_production" {
  project_id    = iaas_project.production.id
  resource_type = "vpc"
  resource_id   = iaas_vpc.main.id
}

# Deleting this resource unassigns the resource from the project (sets its
# project_id back to null) — it does NOT delete iaas_instance.web or
# iaas_project.production themselves.

# Import with a 3-part composite id "<project_id>/<resource_type>/<resource_id>":
#   terraform import iaas_project_assignment.web_in_production 11111111-1111-1111-1111-111111111111/instance/22222222-2222-2222-2222-222222222222
