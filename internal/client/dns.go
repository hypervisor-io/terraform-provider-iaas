package client

import (
	"context"
	"fmt"
	"net/url"
)

// DNS endpoints (verified against the real UserApi\VpcDnsZoneController,
// VpcDnsRecordSetController, VpcDnsRecordController + VpcDnsService +
// routes/user_api.php + the 2026_04_06_100000_vpc_dns migration + the
// VpcDns* models).
//
// The DNS subsystem is a THREE-LEVEL hierarchy, all owner-scoped (zones are not
// instance-scoped):
//
//	zone  (vpc_dns_zones)            owner-owned; attached to 0..N VPCs (m2m)
//	  └─ record_set (vpc_dns_record_sets)   name+type+routing_policy+ttl
//	       └─ record (vpc_dns_records)      value+weight+failover_role+enabled
//	            └─ health_check (1:1)        type+port+path+...
//
// ── ZONE CRUD ────────────────────────────────────────────────────────────────
//
//	INDEX   GET    /dns-zones                       (PLURAL)  → Laravel paginator
//	CREATE  POST   /dns-zones                       (PLURAL)  body {name (req),
//	                                                 description?, vpc_ids?:[uuid]}
//	                                                 → {success,message,zone:{id,...}}
//	SHOW    GET    /dns-zone/{id}                    (SINGULAR) → {zone:{...,vpcs:[{id,name}],
//	                                                 record_sets:[{...,records:[{...,
//	                                                 health_check}]}]}, available_vpcs:[...]}
//	UPDATE  PATCH  /dns-zone/{id}                    (SINGULAR) body {description?}
//	                                                 (ONLY description is mutable; name is NOT)
//	                                                 → {success,message,zone}
//	DELETE  DELETE /dns-zone/{id}                    (SINGULAR) → {success,message}
//	                                                 (ASYNC: marks status="deleting" +
//	                                                 dispatches DeleteDnsZone job; the row
//	                                                 is soft-deleted by the job, so the
//	                                                 SHOW keeps returning status="deleting"
//	                                                 until the job finishes, then 404s)
//
// ── ZONE ↔ VPC attach/detach ─────────────────────────────────────────────────
//
//	ATTACH  POST   /dns-zone/{id}/attach-vpc          body {vpc_id} (SINGULAR uuid, not array)
//	                                                  → {success,message}
//	DETACH  DELETE /dns-zone/{id}/detach-vpc/{vpcId}  (vpc id in the PATH, not the body)
//	                                                  → {success,message}
//
// The attached vpc ids ARE present in the zone SHOW under zone.vpcs[].id, so the
// resource models vpc_ids as a set-diff (like security_group instance_ids):
// attach added, detach removed, rebuild from SHOW on Read.
//
// ── RECORD SET CRUD (child of zone; zone_id in the path) ─────────────────────
//
//	CREATE  POST   /dns-zone/{zoneId}/record-sets               body {name,type,
//	                                                            routing_policy,ttl}
//	                                                            → {success,message,record_set:{id,...}}
//	UPDATE  PATCH  /dns-zone/{zoneId}/record-set/{rsId}         body {name?,type?,
//	                                                            routing_policy?,ttl?}
//	                                                            → {success,message,record_set}
//	DELETE  DELETE /dns-zone/{zoneId}/record-set/{rsId}         → {success,message}
//
// There is NO individual record-set SHOW route — record sets are EMBEDDED in the
// zone SHOW under zone.record_sets[]. GetDnsRecordSet reads-by-scan.
//
// ── RECORD CRUD (child of record set; zone_id + rsId in the path) ────────────
//
//	CREATE  POST   /dns-zone/{zoneId}/record-set/{rsId}/records          body {value,
//	                                                                    weight?,failover_role?,enabled?}
//	                                                                    → {success,message,record:{id,...}}
//	UPDATE  PATCH  /dns-zone/{zoneId}/record-set/{rsId}/record/{recId}   body {value?,
//	                                                                    weight?,failover_role?,enabled?}
//	                                                                    → {success,message,record}
//	TOGGLE  POST   .../record/{recId}/toggle                            (flips enabled — NOT modelled;
//	                                                                    enabled is set explicitly via UPDATE)
//	DELETE  DELETE .../record/{recId}                                   → {success,message}
//
// Records are EMBEDDED in the zone SHOW under zone.record_sets[].records[] (each
// with its embedded health_check). GetDnsRecord reads-by-scan.
//
// ── HEALTH CHECK (1:1 with a record; attached/detached) ──────────────────────
//
//	STORE   POST   .../record/{recId}/health-check   body {type,port?,path?,
//	                                                 expected_status?,interval?,timeout?,
//	                                                 unhealthy_threshold?,healthy_threshold?}
//	                                                 → {success,message,health_check}
//	                                                 (create-OR-update: 1:1, storeHealthCheck
//	                                                 updates in place when one already exists)
//	DELETE  DELETE .../record/{recId}/health-check   → {success,message}
//
// The health check is EMBEDDED in the record under record.health_check (object or
// null), so the resource models it as an inline nested single block on the record
// and reconciles it via STORE/DELETE on diff.
//
// Notes:
//   - All writes are SYNCHRONOUS at HTTP 200 EXCEPT zone DELETE, which is async
//     (status→deleting + queued job). Failures surface as success:false at HTTP
//     200 (C3): quota exceeded (max_dns_zones), VPC not owned, duplicate
//     name+type, CNAME/policy violations, type-specific value validation. doItem/
//     doVoid map these to errors.
//   - Empty-id guards are applied on every path-id argument (consistency).
//   - Every id is url.PathEscape'd into the path.

// ── Zone CRUD ────────────────────────────────────────────────────────────────

// ListDnsZones returns all DNS zones for the authenticated owner (paginator-aware).
func (c *Client) ListDnsZones(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/dns-zones", nil)
}

// CreateDnsZone creates a DNS zone from the supplied body (name required;
// description + vpc_ids optional). The create is synchronous: the returned object
// carries the id. The collection path is PLURAL (/dns-zones).
func (c *Client) CreateDnsZone(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/dns-zones", body, "zone")
}

// GetDnsZone fetches a single zone by id, unwrapping the "zone" envelope. The
// returned object carries the EMBEDDED vpcs[], record_sets[] (each with records[]
// and each record's health_check). The SHOW route is SINGULAR (/dns-zone/{id}). A
// 404 (absent / belonging to another owner) is an *APIError recognised by
// IsNotFound.
func (c *Client) GetDnsZone(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetDnsZone: empty id")
	}
	return c.doItem(ctx, "GET", "/dns-zone/"+url.PathEscape(id), nil, "zone")
}

// UpdateDnsZone patches the only mutable scalar field of a zone — description.
// (The zone name is immutable: updateZone only persists description.) The route is
// SINGULAR. The PATCH response carries the fresh zone under "zone".
func (c *Client) UpdateDnsZone(ctx context.Context, id string, fields map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateDnsZone: empty id")
	}
	return c.doItem(ctx, "PATCH", "/dns-zone/"+url.PathEscape(id), fields, "zone")
}

// DeleteDnsZone queues deletion of a zone by id (the service marks it "deleting"
// and dispatches a DeleteDnsZone job that soft-deletes the row). The DELETE route
// is SINGULAR. A failure surfaces as success:false at HTTP 200, so doVoid checks it.
func (c *Client) DeleteDnsZone(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteDnsZone: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/dns-zone/"+url.PathEscape(id), nil)
}

// AttachDnsZoneVpc attaches a single VPC to a zone (body {vpc_id}; SINGULAR id,
// not an array). A failure (VPC not owned, already attached) surfaces as
// success:false at HTTP 200, so doVoid checks it.
func (c *Client) AttachDnsZoneVpc(ctx context.Context, zoneID, vpcID string) error {
	if zoneID == "" {
		return fmt.Errorf("AttachDnsZoneVpc: empty zoneID")
	}
	if vpcID == "" {
		return fmt.Errorf("AttachDnsZoneVpc: empty vpcID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/attach-vpc"
	return c.doVoid(ctx, "POST", path, map[string]any{"vpc_id": vpcID})
}

// DetachDnsZoneVpc detaches a VPC from a zone. The vpc id is in the PATH
// (/dns-zone/{zoneId}/detach-vpc/{vpcId}), NOT the body, and the verb is DELETE.
func (c *Client) DetachDnsZoneVpc(ctx context.Context, zoneID, vpcID string) error {
	if zoneID == "" {
		return fmt.Errorf("DetachDnsZoneVpc: empty zoneID")
	}
	if vpcID == "" {
		return fmt.Errorf("DetachDnsZoneVpc: empty vpcID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/detach-vpc/" + url.PathEscape(vpcID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// ── Record set CRUD ──────────────────────────────────────────────────────────

// CreateDnsRecordSet creates a record set under the given zone from the supplied
// body (name/type/routing_policy/ttl required). The "record_set" envelope is
// unwrapped, returning the created record set WITH its id.
func (c *Client) CreateDnsRecordSet(ctx context.Context, zoneID string, body map[string]any) (map[string]any, error) {
	if zoneID == "" {
		return nil, fmt.Errorf("CreateDnsRecordSet: empty zoneID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/record-sets"
	return c.doItem(ctx, "POST", path, body, "record_set")
}

// UpdateDnsRecordSet patches a record set (name/type/routing_policy/ttl). The
// "record_set" envelope is unwrapped, returning the fresh record set.
func (c *Client) UpdateDnsRecordSet(ctx context.Context, zoneID, rsID string, body map[string]any) (map[string]any, error) {
	if zoneID == "" {
		return nil, fmt.Errorf("UpdateDnsRecordSet: empty zoneID")
	}
	if rsID == "" {
		return nil, fmt.Errorf("UpdateDnsRecordSet: empty rsID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/record-set/" + url.PathEscape(rsID)
	return c.doItem(ctx, "PATCH", path, body, "record_set")
}

// DeleteDnsRecordSet deletes a record set (cascading its records + their health
// checks). doVoid checks the success flag.
func (c *Client) DeleteDnsRecordSet(ctx context.Context, zoneID, rsID string) error {
	if zoneID == "" {
		return fmt.Errorf("DeleteDnsRecordSet: empty zoneID")
	}
	if rsID == "" {
		return fmt.Errorf("DeleteDnsRecordSet: empty rsID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/record-set/" + url.PathEscape(rsID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetDnsRecordSet resolves a single record set by scanning the parent zone SHOW's
// embedded record_sets[] (there is NO individual record-set SHOW route). It
// returns the matching record-set object or a 404-shaped *APIError (IsNotFound)
// when the id is absent. A 404 on the parent zone propagates.
func (c *Client) GetDnsRecordSet(ctx context.Context, zoneID, rsID string) (map[string]any, error) {
	if zoneID == "" {
		return nil, fmt.Errorf("GetDnsRecordSet: empty zoneID")
	}
	if rsID == "" {
		return nil, fmt.Errorf("GetDnsRecordSet: empty rsID")
	}
	zone, err := c.GetDnsZone(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	for _, rs := range dnsChildren(zone, "record_sets") {
		if id, ok := rs["id"].(string); ok && id == rsID {
			return rs, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "DNS record set not found"}
}

// ── Record CRUD ──────────────────────────────────────────────────────────────

// CreateDnsRecord creates a record within the given record set from the supplied
// body (value required; weight/failover_role/enabled optional). The "record"
// envelope is unwrapped, returning the created record WITH its id.
func (c *Client) CreateDnsRecord(ctx context.Context, zoneID, rsID string, body map[string]any) (map[string]any, error) {
	if zoneID == "" {
		return nil, fmt.Errorf("CreateDnsRecord: empty zoneID")
	}
	if rsID == "" {
		return nil, fmt.Errorf("CreateDnsRecord: empty rsID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/record-set/" + url.PathEscape(rsID) + "/records"
	return c.doItem(ctx, "POST", path, body, "record")
}

// UpdateDnsRecord patches a record (value/weight/failover_role/enabled). The
// "record" envelope is unwrapped, returning the fresh record.
func (c *Client) UpdateDnsRecord(ctx context.Context, zoneID, rsID, recID string, body map[string]any) (map[string]any, error) {
	if zoneID == "" {
		return nil, fmt.Errorf("UpdateDnsRecord: empty zoneID")
	}
	if rsID == "" {
		return nil, fmt.Errorf("UpdateDnsRecord: empty rsID")
	}
	if recID == "" {
		return nil, fmt.Errorf("UpdateDnsRecord: empty recID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/record-set/" + url.PathEscape(rsID) + "/record/" + url.PathEscape(recID)
	return c.doItem(ctx, "PATCH", path, body, "record")
}

// DeleteDnsRecord deletes a record (cascading its health check). doVoid checks the
// success flag.
func (c *Client) DeleteDnsRecord(ctx context.Context, zoneID, rsID, recID string) error {
	if zoneID == "" {
		return fmt.Errorf("DeleteDnsRecord: empty zoneID")
	}
	if rsID == "" {
		return fmt.Errorf("DeleteDnsRecord: empty rsID")
	}
	if recID == "" {
		return fmt.Errorf("DeleteDnsRecord: empty recID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/record-set/" + url.PathEscape(rsID) + "/record/" + url.PathEscape(recID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetDnsRecord resolves a single record by scanning the parent zone SHOW's
// embedded record_sets[].records[] (there is NO individual record SHOW route). It
// returns the matching record object (with its embedded health_check) or a
// 404-shaped *APIError (IsNotFound) when the id is absent. A 404 on the parent
// zone propagates.
func (c *Client) GetDnsRecord(ctx context.Context, zoneID, rsID, recID string) (map[string]any, error) {
	if zoneID == "" {
		return nil, fmt.Errorf("GetDnsRecord: empty zoneID")
	}
	if rsID == "" {
		return nil, fmt.Errorf("GetDnsRecord: empty rsID")
	}
	if recID == "" {
		return nil, fmt.Errorf("GetDnsRecord: empty recID")
	}
	zone, err := c.GetDnsZone(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	for _, rs := range dnsChildren(zone, "record_sets") {
		if id, ok := rs["id"].(string); !ok || id != rsID {
			continue
		}
		for _, rec := range dnsChildren(rs, "records") {
			if id, ok := rec["id"].(string); ok && id == recID {
				return rec, nil
			}
		}
	}
	return nil, &APIError{Status: 404, Message: "DNS record not found"}
}

// ── Health check ops ─────────────────────────────────────────────────────────

// StoreDnsHealthCheck creates OR updates (1:1) a record's health check from the
// supplied body (type required; the rest optional). The "health_check" envelope is
// unwrapped, returning the fresh health check.
func (c *Client) StoreDnsHealthCheck(ctx context.Context, zoneID, rsID, recID string, body map[string]any) (map[string]any, error) {
	if zoneID == "" {
		return nil, fmt.Errorf("StoreDnsHealthCheck: empty zoneID")
	}
	if rsID == "" {
		return nil, fmt.Errorf("StoreDnsHealthCheck: empty rsID")
	}
	if recID == "" {
		return nil, fmt.Errorf("StoreDnsHealthCheck: empty recID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/record-set/" + url.PathEscape(rsID) +
		"/record/" + url.PathEscape(recID) + "/health-check"
	return c.doItem(ctx, "POST", path, body, "health_check")
}

// DeleteDnsHealthCheck detaches a record's health check. The service is idempotent
// (it no-ops when no health check exists), returning success:true either way.
func (c *Client) DeleteDnsHealthCheck(ctx context.Context, zoneID, rsID, recID string) error {
	if zoneID == "" {
		return fmt.Errorf("DeleteDnsHealthCheck: empty zoneID")
	}
	if rsID == "" {
		return fmt.Errorf("DeleteDnsHealthCheck: empty rsID")
	}
	if recID == "" {
		return fmt.Errorf("DeleteDnsHealthCheck: empty recID")
	}
	path := "/dns-zone/" + url.PathEscape(zoneID) + "/record-set/" + url.PathEscape(rsID) +
		"/record/" + url.PathEscape(recID) + "/health-check"
	return c.doVoid(ctx, "DELETE", path, nil)
}

// dnsChildren extracts an embedded array (e.g. "record_sets", "records", "vpcs")
// from a parent DNS object, coercing each element to a map. A missing/empty/
// malformed array yields nil. Mirrors lbChildren for the read-by-scan source.
func dnsChildren(parent map[string]any, key string) []map[string]any {
	raw, ok := parent[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
