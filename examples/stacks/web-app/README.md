# Web application stack

A composed OpenTofu / Terraform stack that provisions a complete public-facing
web application on the IaaS platform using the `iaas` provider. It exists to
demonstrate end-to-end resource binding - every resource references another via
a real Terraform reference, so a single `tofu apply` builds the whole graph in
dependency order.

## What it provisions

```
data: iaas_location → iaas_plan → iaas_image
                           │
iaas_ssh_key               │
     │                     ▼
     │                iaas_vpc ──► iaas_vpc_subnet
     │                     │              │
     │                     └──────┬───────┘
     ▼                            ▼
iaas_security_group ──► iaas_instance (×N, in the subnet, with the SSH key)
                              │
                              ▼
iaas_load_balancer ──► iaas_lb_backend ──► iaas_lb_target (×N → the instances)
     │
     └──────────────► iaas_lb_frontend (:80 → default backend)
```

- **Catalog data sources** resolve the deploy location, instance plan, and OS
  image by name so no opaque UUIDs are hardcoded for those.
- **`iaas_ssh_key`** - the public key injected into the instances at deploy time.
- **`iaas_vpc` + `iaas_vpc_subnet`** - a private network and a public web-tier
  subnet.
- **`iaas_security_group`** - allows inbound HTTP/HTTPS/SSH, attached to every
  instance.
- **`iaas_instance` (×N)** - the web servers, placed in the subnet, deployed
  from the image with the SSH key. Count is controlled by `web_instance_count`.
- **`iaas_load_balancer` + `iaas_lb_backend` + `iaas_lb_target` (×N) +
  `iaas_lb_frontend`** - an HTTP load balancer that fans port 80 traffic across
  the instances. One target per instance, linked via `instance_id` and routed by
  `target_ip` (the instance's private subnet IP).

## Prerequisites

- The `iaas` provider plugin (built from this repository, or installed from a
  registry once published).
- A platform **user API** account with an **IP-locked Bearer token**. The token
  only works from the IP address it was issued for - run `tofu` from a host whose
  egress IP matches the IP the token is locked to, otherwise every call returns
  401/403. See the provider docs for issuing a token.
- A VPC-enabled and load-balancer-enabled hypervisor group, an enabled instance
  plan and load balancer plan, and an OS image available in that location.

## Configure

Supply the API credentials via environment variables (preferred - keeps the
secret off disk):

```sh
export IAAS_API_ENDPOINT="https://panel.example.com/api"
export IAAS_API_TOKEN="your-ip-locked-bearer-token"
```

Then provide the stack inputs:

```sh
cp terraform.tfvars.example terraform.tfvars
# edit terraform.tfvars: location_name, plan_name, image_name,
# hypervisor_group_id, lb_plan_id, ssh_public_key, ...
```

`terraform.tfvars` is gitignored by convention - do not commit real values. You
can alternatively set `iaas_api_endpoint` / `iaas_api_token` as variables, but
the environment variables are recommended.

## Run

```sh
tofu init
tofu plan
tofu apply
```

(`terraform init/plan/apply` works identically.)

Useful outputs after apply:

- `load_balancer_public_ip` - point your DNS here.
- `instance_ids`, `instance_public_ips`, `instance_private_ips`.
- `vpc_id`, `vpc_subnet_id`, `security_group_id`.

To tear everything down:

```sh
tofu destroy
```

## Validating without a live panel

You can confirm the stack is internally consistent (references resolve,
attribute names and types match the provider schema) without contacting a panel:

```sh
# from the repo root, build and register the provider via a dev override
go build -o terraform-provider-iaas .
cat > /tmp/iaas-dev.tfrc <<EOF
provider_installation {
  dev_overrides { "iaas/iaas" = "$(pwd)" }
  direct {}
}
EOF

cd examples/stacks/web-app
TF_CLI_CONFIG_FILE=/tmp/iaas-dev.tfrc tofu validate
```

With a dev override, `tofu init` is short-circuited; `tofu validate` runs
directly and reports whether the configuration is valid.
