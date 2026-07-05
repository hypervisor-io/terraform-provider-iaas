package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Docker app-deployment endpoints (Gap G2), verified against
// UserApi\DockerController + DockerService + routes/api.php. Every path is
// scoped under an instance:
//
//	INDEX    GET    /instance/{instanceID}/docker
//	                 → {success, docker_enabled (0/1), deployments:[...]}. The
//	                 deployments array is a BARE array under "deployments"
//	                 (NOT a Laravel paginator) - the same shape family as the
//	                 Kubernetes ssl-certificates "certs" list.
//	INSTALL  POST   /instance/{instanceID}/docker/install
//	                 → 200 {success,message} (no id/object). NOT idempotent:
//	                 422 "Docker is already installed on this instance." when
//	                 instance.docker_enabled is already 1. The install itself
//	                 is asynchronous - a script is dispatched over the
//	                 hypervisor/QGA channel and calls back LATER
//	                 (installCallback) to flip instance.docker_enabled to 1;
//	                 this endpoint only reports that the install was kicked
//	                 off. Callers must poll DockerEnabled to converge.
//	DEPLOY   POST   /instance/{instanceID}/docker
//	                 body {app_slug (req), env_variables? (map string->string),
//	                 port_mappings? ([]{container_port,host_port,protocol?})}
//	                 → 200 {success,message,deployment:{...}} (row created,
//	                 status "deploying") - or 422/500 {success:false,message}
//	                 with NO deployment object (guard rejection, e.g. Docker
//	                 not enabled / instance not running / another deployment
//	                 already in-flight - or, on 500, the hypervisor dispatch
//	                 itself threw: the row is still inserted server-side in
//	                 status "error" but ITS ID IS NEVER RETURNED to the caller
//	                 on this specific failure path, so an orphaned "error" row
//	                 is a known, unavoidable possibility of the real API).
//	DEPLOY   POST   /instance/{instanceID}/docker/custom
//	  CUSTOM         body {compose_url (req - an HTTPS URL; the compose YAML
//	                 is FETCHED SERVER-SIDE via an SSRF-guarded request, this
//	                 is NOT literal compose file content), app_name (req),
//	                 env_variables?, port_mappings?}
//	                 → same envelope/failure shape as DEPLOY.
//	CONTROL  POST   /instance/{instanceID}/docker/{depID}/{action}
//	                 action ∈ start|stop|restart|remove → {success,message}
//	                 (no deployment object). "remove" deletes the row
//	                 synchronously (same as DESTROY below). NOT wired into
//	                 this client - v1 of iaas_docker_deployment has no
//	                 in-place update/action support (see docker_deployment.go).
//	RETRY    POST   /instance/{instanceID}/docker/{depID}/retry
//	                 → {success,message,deployment:{...}}. NOT wired (v1).
//	CHECK    POST   /instance/{instanceID}/docker/{depID}/check-status
//	                 → {success,message}; queues an async slave/QGA probe whose
//	                 result reaches the row via the SAME callback path as a
//	                 normal deploy - no direct return value to decode. NOT
//	                 wired (v1); GetDockerDeployment's own poll is sufficient
//	                 because the in-VM deploy script calls back on its own.
//	DESTROY  DELETE /instance/{instanceID}/docker/{depID}
//	                 → {success,message}. Under the hood DockerController's
//	                 destroy() calls control($dep,'remove') directly - the row
//	                 is hard-deleted synchronously right after the (fire-and-
//	                 forget) hypervisor command is enqueued, so no delete-side
//	                 waiter is needed (same reasoning as iaas_image's Delete).
//
// There is NO per-deployment SHOW route. GetDockerDeployment therefore lists
// and matches by id (user_script.go pattern), synthesising a 404 *APIError
// recognised by IsNotFound.
//
// STATUS (deployment.status) values reachable via THIS client's DEPLOY/DEPLOY
// CUSTOM path: "pending" (initial insert) -> "deploying" (hypervisor dispatch
// ack'd) -> terminal "running" (the in-VM python script's callback reported
// success) or terminal "error" (either the dispatch itself threw, or the
// callback reported a failure). "stopped"/"failed"/"deployed"/"building" are
// reachable only via the separate control()/git-buildpack pipelines this
// client does not drive; "failed" is still included in the resource's
// waiter fail-set defensively, since handleCallback persists whatever status
// string a callback sends verbatim.
//
// env_variables and compose_yaml are $hidden (encrypted at rest) - never
// present in the INDEX/create response bodies. port_mappings is a plain
// `array` cast column and IS technically present in the response, but
// internal/resources treats it (like env_variables) as WRITE-ONLY, always
// echoing the plan/prior value rather than round-tripping it - there is no
// update path anyway (every input is RequiresReplace), so this avoids
// brittle float64-vs-int64 JSON-number nested-list comparisons for no
// behavioural benefit.

// dockerIndex fetches the bare INDEX envelope ({docker_enabled,
// deployments:[...]}) for an instance - the shared building block for
// ListDockerDeployments and DockerEnabled.
func (c *Client) dockerIndex(ctx context.Context, instanceID string) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("dockerIndex: empty instance id")
	}
	return c.doItem(ctx, "GET", "/instance/"+url.PathEscape(instanceID)+"/docker", nil, "")
}

// ListDockerDeployments returns every Docker deployment on the instance.
func (c *Client) ListDockerDeployments(ctx context.Context, instanceID string) ([]map[string]any, error) {
	top, err := c.dockerIndex(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	raw, _ := top["deployments"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// GetDockerDeployment finds a single deployment by id via read-by-scan over
// the instance's deployment LIST (there is no per-deployment SHOW route). An
// absent id surfaces as an *APIError with Status 404 (IsNotFound), so the
// resource's Read removes it from state and Terraform plans a recreate.
func (c *Client) GetDockerDeployment(ctx context.Context, instanceID, depID string) (map[string]any, error) {
	if depID == "" {
		return nil, fmt.Errorf("GetDockerDeployment: empty deployment id")
	}
	deployments, err := c.ListDockerDeployments(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	for _, d := range deployments {
		if id, _ := d["id"].(string); id == depID {
			return d, nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Message: "docker deployment not found"}
}

// DockerEnabled reports whether the Docker engine is installed on the
// instance (instance.docker_enabled), read off the same INDEX envelope used
// by ListDockerDeployments.
func (c *Client) DockerEnabled(ctx context.Context, instanceID string) (bool, error) {
	top, err := c.dockerIndex(ctx, instanceID)
	if err != nil {
		return false, err
	}
	switch v := top["docker_enabled"].(type) {
	case bool:
		return v, nil
	case float64:
		return v != 0, nil
	default:
		return false, nil
	}
}

// InstallDockerEngine kicks off the Docker Engine install on the instance.
// Returns the bare {success,message} envelope (there is nothing else to
// extract). NOT idempotent - the caller must check DockerEnabled first
// (calling this while already enabled 422s "Docker is already installed").
func (c *Client) InstallDockerEngine(ctx context.Context, instanceID string) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("InstallDockerEngine: empty instance id")
	}
	return c.doItem(ctx, "POST", "/instance/"+url.PathEscape(instanceID)+"/docker/install", nil, "")
}

// DeployDockerApp deploys a catalog app (source = "app"). fields must carry
// app_slug; env_variables/port_mappings are optional. Returns the created
// deployment object (status "deploying"); poll GetDockerDeployment to
// converge on "running" (or fail on "error").
func (c *Client) DeployDockerApp(ctx context.Context, instanceID string, fields map[string]any) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("DeployDockerApp: empty instance id")
	}
	return c.doItem(ctx, "POST", "/instance/"+url.PathEscape(instanceID)+"/docker", fields, "deployment")
}

// DeployDockerCompose deploys a custom compose file fetched from
// compose_url (source = "compose"). fields must carry compose_url + app_name;
// env_variables/port_mappings are optional. Same response/converge shape as
// DeployDockerApp.
func (c *Client) DeployDockerCompose(ctx context.Context, instanceID string, fields map[string]any) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("DeployDockerCompose: empty instance id")
	}
	return c.doItem(ctx, "POST", "/instance/"+url.PathEscape(instanceID)+"/docker/custom", fields, "deployment")
}

// DeleteDockerDeployment removes a deployment (DELETE .../docker/{depID},
// DockerController::destroy -> control(remove)). Synchronous: the row is
// gone by the time this returns successfully - no delete-side waiter needed.
func (c *Client) DeleteDockerDeployment(ctx context.Context, instanceID, depID string) error {
	if instanceID == "" {
		return fmt.Errorf("DeleteDockerDeployment: empty instance id")
	}
	if depID == "" {
		return fmt.Errorf("DeleteDockerDeployment: empty deployment id")
	}
	return c.doVoid(ctx, "DELETE", "/instance/"+url.PathEscape(instanceID)+"/docker/"+url.PathEscape(depID), nil)
}
