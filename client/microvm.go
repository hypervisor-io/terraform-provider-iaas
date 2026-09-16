package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// MicroVM endpoints (unified surface, MVU2-2; user_api, bearer token). A
// MicroVM runs an image version with a plan, lifetime, hooks, network and
// ingress; the old sandbox/app/runner shapes are attribute combinations of
// this one resource.
//
//	INDEX   GET    /microvm/vms               (?search=&state=&image_id=&connector_id=)
//	                                                                 -> {success,microvms:{data:[...]}}
//	CREATE  POST   /microvm/vms               body per the U3 Create MicroVM body
//	                                                                 -> {success,microvm:{...}} (201)
//	SHOW    GET    /microvm/vm/{id}                                -> {success,microvm:{...},runs:[...],
//	                                                                   versions:[...],domains:[...],env_keys:[...]}
//	PAUSE   POST   /microvm/vm/{id}/pause                          -> {success}
//	RESUME  POST   /microvm/vm/{id}/resume                         -> {success}
//	STOP    POST   /microvm/vm/{id}/stop                           -> {success}
//	START   POST   /microvm/vm/{id}/start                          -> {success}
//	KILL    POST   /microvm/vm/{id}/kill                           -> {success}
//	TIMEOUT POST   /microvm/vm/{id}/timeout   body {timeout}       -> {success}
//	DEPLOY  POST   /microvm/vm/{id}/deploy    body {image_version_id}
//	                                                                 -> {success,run:{...}}
//	ROLLBACK POST  /microvm/vm/{id}/rollback  body {image_version_id}
//	                                                                 -> {success,run:{...}}
//	ENV     PUT    /microvm/vm/{id}/env       body {env}           -> {success,keys:[...]}
//	DOMAIN+ POST   /microvm/vm/{id}/domain    body {hostname}      -> {success,domain:{...}} (201)
//	DOMAIN- DELETE /microvm/vm/{id}/domain/{domainId}              -> {success}
//	LOGS    GET    /microvm/vm/{id}/logs      (?limit=)            -> {success,lines:[{line}]}
//	METRICS GET    /microvm/vm/{id}/metrics                        -> {success,metrics:{...}}
//	CATALOG GET    /microvm/catalog                                -> {success,locations:[...],
//	                                                                   images:[...],security_groups:[...],
//	                                                                   vpc_subnets:[...],limits:{...}}
//	DELETE  DELETE /microvm/vm/{id}                                -> {success,message}
//
// A DomainException on a state-changing verb surfaces as 409 (illegal state
// transition) or 422 (any other domain error); validation failures are 422.

func microvm(id string) string { return "/microvm/vm/" + url.PathEscape(id) }

// ListMicrovms returns the account's MicroVMs, filtered by name substring
// (search), state, image id and connector id. Empty filters are omitted from
// the query.
func (c *Client) ListMicrovms(ctx context.Context, search, state, imageID, connectorID string) ([]map[string]any, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	if state != "" {
		q.Set("state", state)
	}
	if imageID != "" {
		q.Set("image_id", imageID)
	}
	if connectorID != "" {
		q.Set("connector_id", connectorID)
	}
	path := "/microvm/vms"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.listNamedPaginator(ctx, path, "microvms")
}

// GetMicrovm fetches the SHOW envelope for one MicroVM: the presented microvm
// object plus its recent runs, the image's versions, domains and env key
// names (values are never serialized). A missing id surfaces as a 404
// *APIError.
func (c *Client) GetMicrovm(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetMicrovm: empty id")
	}
	return c.doItem(ctx, "GET", microvm(id), nil, "")
}

// CreateMicrovm creates a MicroVM. body follows the U3 Create MicroVM body:
// {hypervisor_group_id, image_id, name, image_version_id?, plan_id?,
// ingress?, network?, max_lifetime_seconds?, on_timeout?,
// idle_timeout_seconds?, always_on?, env?, lifecycle_hooks?, domain?,
// secure?}.
func (c *Client) CreateMicrovm(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/vms", body, "microvm")
}

// PauseMicrovm pauses a running MicroVM (snapshots it on the node).
func (c *Client) PauseMicrovm(ctx context.Context, id string) error {
	return c.microvmVerb(ctx, id, "pause")
}

// ResumeMicrovm resumes a paused MicroVM.
func (c *Client) ResumeMicrovm(ctx context.Context, id string) error {
	return c.microvmVerb(ctx, id, "resume")
}

// StopMicrovm stops a MicroVM. Its hostname and DNS record are kept.
func (c *Client) StopMicrovm(ctx context.Context, id string) error {
	return c.microvmVerb(ctx, id, "stop")
}

// StartMicrovm starts a stopped MicroVM again.
func (c *Client) StartMicrovm(ctx context.Context, id string) error {
	return c.microvmVerb(ctx, id, "start")
}

// KillMicrovm permanently destroys the MicroVM's current run on the node. The
// MicroVM row survives (delete removes it).
func (c *Client) KillMicrovm(ctx context.Context, id string) error {
	return c.microvmVerb(ctx, id, "kill")
}

// microvmVerb runs one of the state-changing POST verbs, all of which answer
// a bare {success} envelope.
func (c *Client) microvmVerb(ctx context.Context, id, verb string) error {
	if id == "" {
		return fmt.Errorf("microvmVerb(%s): empty id", verb)
	}
	return c.doVoid(ctx, "POST", microvm(id)+"/"+verb, nil)
}

// SetMicrovmTimeout overwrites the MicroVM's auto-pause/kill TTL in seconds.
func (c *Client) SetMicrovmTimeout(ctx context.Context, id string, timeoutSeconds int) error {
	if id == "" {
		return fmt.Errorf("SetMicrovmTimeout: empty id")
	}
	return c.doVoid(ctx, "POST", microvm(id)+"/timeout", map[string]any{"timeout": timeoutSeconds})
}

// DeployMicrovm deploys a newer image version to the MicroVM (health-gated
// before ingress switches) and returns the new run.
func (c *Client) DeployMicrovm(ctx context.Context, id, imageVersionID string) (map[string]any, error) {
	return c.microvmDeploy(ctx, id, "deploy", imageVersionID)
}

// RollbackMicrovm deploys an older image version and returns the new run.
func (c *Client) RollbackMicrovm(ctx context.Context, id, imageVersionID string) (map[string]any, error) {
	return c.microvmDeploy(ctx, id, "rollback", imageVersionID)
}

func (c *Client) microvmDeploy(ctx context.Context, id, verb, imageVersionID string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("microvmDeploy(%s): empty id", verb)
	}
	return c.doItem(ctx, "POST", microvm(id)+"/"+verb,
		map[string]any{"image_version_id": imageVersionID}, "run")
}

// SetMicrovmEnv replaces the MicroVM's environment variables (a null value
// removes the key) and returns the merged env's key names; values are never
// returned by the API.
func (c *Client) SetMicrovmEnv(ctx context.Context, id string, env map[string]string) ([]string, error) {
	if id == "" {
		return nil, fmt.Errorf("SetMicrovmEnv: empty id")
	}
	envelope, err := c.doItem(ctx, "PUT", microvm(id)+"/env", map[string]any{"env": env}, "")
	if err != nil {
		return nil, err
	}
	keys := []string{}
	if arr, ok := envelope["keys"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				keys = append(keys, s)
			}
		}
	}
	return keys, nil
}

// AddMicrovmDomain attaches a custom hostname to the MicroVM and returns the
// domain row. A taken hostname surfaces as 409 duplicate_hostname.
func (c *Client) AddMicrovmDomain(ctx context.Context, id, hostname string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("AddMicrovmDomain: empty id")
	}
	return c.doItem(ctx, "POST", microvm(id)+"/domain", map[string]any{"hostname": hostname}, "domain")
}

// RemoveMicrovmDomain detaches a custom hostname. A domain of another MicroVM
// 404s server-side (the bind asserts the domain belongs to the URL's microvm).
func (c *Client) RemoveMicrovmDomain(ctx context.Context, id, domainID string) error {
	if id == "" || domainID == "" {
		return fmt.Errorf("RemoveMicrovmDomain: empty id or domain id")
	}
	return c.doVoid(ctx, "DELETE", microvm(id)+"/domain/"+url.PathEscape(domainID), nil)
}

// GetMicrovmLogs returns recent console log lines ([{line: ...}]), newest
// last, capped at limit server-side.
func (c *Client) GetMicrovmLogs(ctx context.Context, id string, limit int) ([]map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetMicrovmLogs: empty id")
	}
	path := microvm(id) + "/logs"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	return c.namedList(ctx, "GET", path, "lines")
}

// GetMicrovmMetrics returns the metrics envelope: the latest sample plus the
// per-minute series (both may be empty for a fresh MicroVM).
func (c *Client) GetMicrovmMetrics(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetMicrovmMetrics: empty id")
	}
	return c.doItem(ctx, "GET", microvm(id)+"/metrics", nil, "metrics")
}

// GetMicrovmCatalog returns the unified MicroVM placement catalog: locations,
// ready images, security groups, VPC subnets and limits.
func (c *Client) GetMicrovmCatalog(ctx context.Context) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/microvm/catalog", nil, "")
}

// DeleteMicrovm kills (when not already killed) and soft-deletes the MicroVM;
// the name is released by the server-side tombstone rename.
func (c *Client) DeleteMicrovm(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteMicrovm: empty id")
	}
	return c.doVoid(ctx, "DELETE", microvm(id), nil)
}

// listNamedPaginator fetches a named-paginator endpoint ({key: {data: [...],
// current_page, last_page}}), walking every page (capped at
// maxPaginatorPages) and accumulating the rows.
func (c *Client) listNamedPaginator(ctx context.Context, path, key string) ([]map[string]any, error) {
	items := []map[string]any{}
	for page := 1; page <= maxPaginatorPages; page++ {
		pagePath := path
		if page > 1 {
			var err error
			pagePath, err = withPageParam(path, page)
			if err != nil {
				return nil, err
			}
		}
		envelope, err := c.doItem(ctx, http.MethodGet, pagePath, nil, "")
		if err != nil {
			return nil, err
		}
		paginator, ok := envelope[key].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected paginator under %q", key)
		}
		items = append(items, objSlice(paginator["data"])...)
		current := int(numberValue(paginator["current_page"]))
		last := int(numberValue(paginator["last_page"]))
		if current <= 0 || last <= 0 || current >= last {
			return items, nil
		}
	}
	return items, nil
}

func numberValue(value any) float64 {
	switch value := value.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}
