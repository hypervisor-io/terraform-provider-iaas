# Import by snapshot UUID. sandbox_id is read from the platform; description and consistency are not returned,
# so configure them to adopt them (this does not replace the snapshot).
terraform import iaas_microvm_snapshot.golden 5c0d7a52-3e8f-4a8e-9c31-2f6a7b8c9d01
