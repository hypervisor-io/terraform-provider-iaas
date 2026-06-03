provider "iaas" {
  # Base API URL including the /api suffix. Can also be set via IAAS_API_ENDPOINT.
  endpoint = "https://panel.example.com/api"

  # Bearer token. Prefer the IAAS_API_TOKEN env var over hardcoding.
  # NOTE: the token is IP-LOCKED — it only works from the IP it was registered with.
  # token = "..."  # or export IAAS_API_TOKEN
}
