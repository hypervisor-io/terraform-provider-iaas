package client

import (
	"context"
	"fmt"
	"net/url"
)

// Volume protection policies store wall-clock times in their effective IANA
// timezone. Null timezone follows the account owner; never convert times to UTC.
func (c *Client) ListVolumeProtectionPolicies(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/storage/volume-policies", nil)
}
func (c *Client) CreateVolumeProtectionPolicy(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItemOnce(ctx, "POST", "/storage/volume-policies", body, "policy")
}
func volumeProtectionPath(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("volume protection policy: empty id")
	}
	return "/storage/volume-policy/" + url.PathEscape(id), nil
}

// GetVolumeProtectionPolicy returns policy including volumes and recent_runs.
func (c *Client) GetVolumeProtectionPolicy(ctx context.Context, id string) (map[string]any, error) {
	p, err := volumeProtectionPath(id)
	if err != nil {
		return nil, err
	}
	return c.doItem(ctx, "GET", p, nil, "policy")
}

// GetVolumeProtectionPolicyDetails also preserves the attachable_volumes list.
func (c *Client) GetVolumeProtectionPolicyDetails(ctx context.Context, id string) (map[string]any, error) {
	p, err := volumeProtectionPath(id)
	if err != nil {
		return nil, err
	}
	return c.doItem(ctx, "GET", p, nil, "")
}
func (c *Client) UpdateVolumeProtectionPolicy(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	p, err := volumeProtectionPath(id)
	if err != nil {
		return nil, err
	}
	return c.doItemOnce(ctx, "PATCH", p, body, "policy")
}
func (c *Client) DeleteVolumeProtectionPolicy(ctx context.Context, id string) error {
	p, err := volumeProtectionPath(id)
	if err != nil {
		return err
	}
	return c.doVoidOnce(ctx, "DELETE", p, nil)
}
func (c *Client) AttachVolumeToProtectionPolicy(ctx context.Context, policyID, volumeID string) error {
	return c.volumeProtectionMember(ctx, policyID, volumeID, "attach")
}
func (c *Client) DetachVolumeFromProtectionPolicy(ctx context.Context, policyID, volumeID string) error {
	return c.volumeProtectionMember(ctx, policyID, volumeID, "detach")
}
func (c *Client) volumeProtectionMember(ctx context.Context, policyID, volumeID, action string) error {
	p, err := volumeProtectionPath(policyID)
	if err != nil {
		return err
	}
	if volumeID == "" {
		return fmt.Errorf("volume protection policy: empty volume_id")
	}
	return c.doVoidOnce(ctx, "POST", p+"/"+action, map[string]any{"volume_id": volumeID})
}
func (c *Client) ResetVolumeProtectionPolicyFailures(ctx context.Context, id string) (map[string]any, error) {
	p, err := volumeProtectionPath(id)
	if err != nil {
		return nil, err
	}
	return c.doItemOnce(ctx, "POST", p+"/reset-failures", nil, "policy")
}
