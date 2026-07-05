package client

import (
	"context"
	"fmt"
)

// Project-assignment endpoints (verified against ProjectController on the
// Master, app/Http/Controllers/UserApi/ProjectController.php):
//
//	ASSIGN  POST /project/assign-resource
//	        body {resource_type, resource_id, project_id}
//	        → 200 {success,message} — NO id and NO object is returned at all.
//	        project_id may be omitted/null to UNASSIGN — this is the ONLY
//	        unassign mechanism; there is no dedicated detach/DELETE route.
//	        resource_type is validated server-side against exactly:
//	        instance, vpc, load_balancer, s3_bucket, managed_database.
//
// There is also POST /project/bulk-assign (bulkAssignResources) for
// multi-resource assignment, but iaas_project_assignment models ONE
// resource<->project link per resource instance, so only the single-resource
// ASSIGN endpoint is used.
//
// CRITICALLY, there is no dedicated SHOW/list-membership route for a single
// assignment (GET /project/{id} embeds paginated, per-type resource lists
// which are unsuitable for a targeted "is resource X assigned to project P"
// check — the target could be past the embedded page). Instead every
// assignable resource type carries its OWN project_id un-hidden on its own
// SHOW response (confirmed against each Model's $hidden array on the
// Master: Instance, VPC, LoadBalancer, S3Bucket (nested under "bucket"),
// ManagedDatabase). GetResourceProjectID dispatches to that type's existing
// Get* client method and reads back project_id from the correct location, so
// the resource layer has one call to establish authoritative membership.

// projectAssignableResourceTypes enumerates the resource_type values
// ProjectController's $modelMap accepts (assignResource / bulkAssignResources).
var projectAssignableResourceTypes = map[string]bool{
	"instance":         true,
	"vpc":              true,
	"load_balancer":    true,
	"s3_bucket":        true,
	"managed_database": true,
}

// IsValidProjectResourceType reports whether resourceType is one of the
// types POST /project/assign-resource accepts. Exported so the resource
// layer's schema validator and the client's dispatch stay in sync with a
// single source of truth.
func IsValidProjectResourceType(resourceType string) bool {
	return projectAssignableResourceTypes[resourceType]
}

// AssignResourceToProject assigns resourceType/resourceID to projectID via
// POST /project/assign-resource. Pass projectID == "" to UNASSIGN (sends
// project_id: null) — the same endpoint handles both directions per the
// controller's own doc comment ("Set project_id to null to unassign").
//
// The endpoint's success payload carries no object at all ({success,message}),
// so doVoid — which only checks the success flag — is the correct shared
// helper; there is nothing for doItem to unwrap.
func (c *Client) AssignResourceToProject(ctx context.Context, resourceType, resourceID, projectID string) error {
	if resourceType == "" {
		return fmt.Errorf("AssignResourceToProject: resource_type is required")
	}
	if resourceID == "" {
		return fmt.Errorf("AssignResourceToProject: resource_id is required")
	}
	body := map[string]any{
		"resource_type": resourceType,
		"resource_id":   resourceID,
		"project_id":    nil,
	}
	if projectID != "" {
		body["project_id"] = projectID
	}
	return c.doVoid(ctx, "POST", "/project/assign-resource", body)
}

// GetResourceProjectID fetches resourceType/resourceID's CURRENT project_id by
// dispatching to that type's own existing Get* method (GetInstance/GetVPC/
// GetLoadBalancer/GetS3Bucket/GetManagedDatabase) and reading its project_id
// field back from the shape that type's SHOW envelope actually uses.
//
// Returns ("", nil) when the resource exists but has no project (project_id
// is null/absent — e.g. it was unassigned out of band). A resource that no
// longer exists at all surfaces the underlying *APIError unchanged, which
// IsNotFound recognises (a 404 here means "the resource is gone", NOT merely
// "unassigned" — callers must distinguish the two).
func (c *Client) GetResourceProjectID(ctx context.Context, resourceType, resourceID string) (string, error) {
	switch resourceType {
	case "instance":
		// GetInstance returns the BARE instance model (no envelope).
		obj, err := c.GetInstance(ctx, resourceID)
		if err != nil {
			return "", err
		}
		return projectIDFromObject(obj), nil

	case "vpc":
		// GetVPC unwraps the "vpc" envelope key already.
		obj, err := c.GetVPC(ctx, resourceID)
		if err != nil {
			return "", err
		}
		return projectIDFromObject(obj), nil

	case "load_balancer":
		// GetLoadBalancer unwraps the "load_balancer" envelope key already.
		obj, err := c.GetLoadBalancer(ctx, resourceID)
		if err != nil {
			return "", err
		}
		return projectIDFromObject(obj), nil

	case "s3_bucket":
		// GetS3Bucket returns the ENTIRE SHOW envelope (key ""); the bucket
		// object itself — carrying project_id — is nested under "bucket".
		obj, err := c.GetS3Bucket(ctx, resourceID)
		if err != nil {
			return "", err
		}
		bucket, _ := obj["bucket"].(map[string]any)
		return projectIDFromObject(bucket), nil

	case "managed_database":
		// GetManagedDatabase unwraps the "managed_database" envelope key already.
		obj, err := c.GetManagedDatabase(ctx, resourceID)
		if err != nil {
			return "", err
		}
		return projectIDFromObject(obj), nil

	default:
		return "", fmt.Errorf("GetResourceProjectID: unsupported resource_type %q", resourceType)
	}
}

// projectIDFromObject reads the "project_id" string field from a decoded API
// object, tolerating a nil map, a missing key, or a JSON null — all collapse
// to "" (no project assigned).
func projectIDFromObject(obj map[string]any) string {
	if obj == nil {
		return ""
	}
	if v, ok := obj["project_id"].(string); ok {
		return v
	}
	return ""
}
