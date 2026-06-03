# terraform-provider-iaas

A Terraform / OpenTofu provider for managing virtual machine infrastructure via the IaaS platform's user REST API.

## Authentication

> **Important:** This provider authenticates using an **IP-locked Bearer token**. The token only works from the IP address it was registered with. Requests from any other IP will be rejected.

Set the `IAAS_API_TOKEN` environment variable to your token before running Terraform:

```sh
export IAAS_API_TOKEN="your-token-here"
terraform apply
```

Full provider configuration and resource documentation will be added as the provider matures.

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
```

## License

See [LICENSE](LICENSE).
