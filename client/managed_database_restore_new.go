package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ManagedDatabaseRestoreToNewResult identifies accepted or uncertain work.
// Status is absent on the server's 503 dispatch-unconfirmed response.
type ManagedDatabaseRestoreToNewResult struct {
	ManagedDatabaseID string `json:"managed_database_id"`
	TaskID            string `json:"task_id"`
	Status            string `json:"status,omitempty"`
}

// RestoreToNewError preserves IDs even when the request returns a refusal.
// Unwrap retains APIError status/reason/field diagnostics for all consumers.
type RestoreToNewError struct {
	Result ManagedDatabaseRestoreToNewResult
	Err    error
}

func (e *RestoreToNewError) Error() string {
	if e.Result.ManagedDatabaseID != "" || e.Result.TaskID != "" {
		return fmt.Sprintf("%v; managed_database_id=%s task_id=%s; do not retry this restore request; follow the returned database and task", e.Err, e.Result.ManagedDatabaseID, e.Result.TaskID)
	}
	return fmt.Sprintf("%v; restore dispatch may be uncertain; inspect databases and tasks before retrying", e.Err)
}
func (e *RestoreToNewError) Unwrap() error { return e.Err }

// RestoreManagedDatabaseToNew submits once. A 503 can retain a newly created
// database; neither an automatic replay nor a client-side rollback is safe.
func (c *Client) RestoreManagedDatabaseToNew(ctx context.Context, id string, body map[string]any) (ManagedDatabaseRestoreToNewResult, error) {
	var result ManagedDatabaseRestoreToNewResult
	if strings.TrimSpace(id) == "" {
		return result, fmt.Errorf("restore to new database: empty id")
	}
	backup, _ := body["backup_id"].(string)
	target, _ := body["target_time"].(string)
	if (strings.TrimSpace(backup) == "") == (strings.TrimSpace(target) == "") {
		return result, fmt.Errorf("restore to new database requires exactly one of backup_id or target_time")
	}
	resp, raw, err := c.doWithAttempts(ctx, "POST", "/database/"+url.PathEscape(id)+"/restore-to-new", body, nil, 1)
	if err != nil {
		return result, &RestoreToNewError{Err: err}
	}
	var envelope struct {
		Success *bool                             `json:"success"`
		Data    ManagedDatabaseRestoreToNewResult `json:"data"`
	}
	decodeErr := json.Unmarshal(raw, &envelope)
	result = envelope.Data
	if err := responseError(resp, raw); err != nil {
		if resp.StatusCode >= 500 || result.ManagedDatabaseID != "" || result.TaskID != "" {
			return result, &RestoreToNewError{Result: result, Err: err}
		}
		return result, err
	}
	if decodeErr != nil || resp.StatusCode != http.StatusAccepted || envelope.Success == nil || !*envelope.Success || result.ManagedDatabaseID == "" || result.TaskID == "" || result.Status != "restoring" {
		return result, &RestoreToNewError{Result: result, Err: fmt.Errorf("invalid restore-to-new acknowledgement (HTTP %d)", resp.StatusCode)}
	}
	return result, nil
}
