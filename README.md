# terraform-provider-iaas

A Terraform / OpenTofu provider for managing virtual machine infrastructure via the IaaS platform's user REST API.

## Authentication

> **Important — IP-locked token:** This provider authenticates using a **Bearer token that is
> validated against the IP address it was registered with.** Requests from any other egress IP
> are rejected. Run `tofu`/`terraform` from a static CI runner, bastion host, or workstation
> whose IP matches the token registration. Dynamic-IP CI environments are **not supported.**
> The token may also be scoped to a subuser with limited permissions.

### Environment variables (recommended)

Avoid hardcoding credentials. Set these before running Terraform/OpenTofu:

```sh
export IAAS_API_ENDPOINT="https://panel.example.com/api"
export IAAS_API_TOKEN="your-token-here"
tofu apply
```

### Inline configuration

```hcl
provider "iaas" {
  endpoint = "https://panel.example.com/api"
  # token is sensitive — prefer IAAS_API_TOKEN env var
}
```

## Provider configuration

| Attribute         | Env var              | Required | Description                                              |
|-------------------|----------------------|----------|----------------------------------------------------------|
| `endpoint`        | `IAAS_API_ENDPOINT`  | yes      | Base API URL including the `/api` path suffix            |
| `token`           | `IAAS_API_TOKEN`     | yes      | Bearer token (IP-locked — see Authentication above)      |
| `request_timeout` | —                    | no       | HTTP timeout in seconds (default: 30)                    |
| `insecure`        | —                    | no       | Skip TLS verification — staging only, never production   |

## Development

```sh
# Build
make build

# Run tests
make test

# Vet
make vet

# Format source
make fmt

# Install doc-generation tool (tfplugindocs)
make tools

# Regenerate reference docs under docs/
make docs
```

Reference docs are generated via [tfplugindocs](https://github.com/hashicorp/terraform-plugin-docs)
and committed under `docs/` so they can be published to the Terraform Registry.

## License

See [LICENSE](LICENSE).
