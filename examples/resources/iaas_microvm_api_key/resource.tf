# Create a MicroVM Sandboxes API key. The plaintext key is returned by the API
# ONLY once, at creation: it is captured into the sensitive `key` attribute
# and preserved in state thereafter - no read or import can recover it.
#
# There is no rename/rotate endpoint, so changing `name` destroys the key and
# creates a fresh one (a new plaintext secret).
resource "iaas_microvm_api_key" "ci" {
  name = "ci-sandbox-runner"
}

# The non-secret prefix is safe to show and identifies the key in listings.
output "microvm_api_key_prefix" {
  value = iaas_microvm_api_key.ci.prefix
}

# The plaintext key is SENSITIVE and available only because it was captured at
# create time; mark any output that exposes it as sensitive.
output "microvm_api_key" {
  value     = iaas_microvm_api_key.ci.key
  sensitive = true
}
