package client

import (
	"encoding/json"
	"fmt"
)

// decodeItem unwraps a single-object response body.
//
// C3: If the top-level JSON object contains a "success" key whose value is
// false, decodeItem returns an error whose message is the API's "message"
// string (or a generic fallback when absent), regardless of HTTP status.
//
// Otherwise the lookup proceeds:
//   - If the top-level object has a key matching key, the corresponding
//     sub-object is returned (handles bare {"ssh_key":{…}} and
//     {"success":true,"vpc":{…}}).
//   - If the key is absent, the whole top-level map is returned so that
//     callers can detect the missing id (VPC create: {"success":true,"message":"queued"}).
func decodeItem(body []byte, key string) (map[string]any, error) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	// C3 - success:false at HTTP 200 is still an error.
	if err := checkSuccessFlag(top); err != nil {
		return nil, err
	}

	// Unwrap sub-object when the envelope key is present.
	if raw, ok := top[key]; ok {
		sub, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected object under %q; got %T", key, raw)
		}
		return sub, nil
	}

	// Key absent (e.g. VPC create {"success":true,"message":"queued"}) -
	// return the bare envelope so callers can inspect it.
	return top, nil
}

// decodeList unwraps a list response body.
//
// C3: success:false → error.
//
// Two envelope shapes are handled:
//   - Laravel paginator: top-level object with a "data" array → return the array.
//   - Top-level JSON array → return it directly.
func decodeList(body []byte) ([]map[string]any, error) {
	// First try to unmarshal as a raw JSON value so we can distinguish
	// array vs object without double-parsing.
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	switch raw[0] {
	case '[':
		// Top-level JSON array.
		var items []map[string]any
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("decoding list array: %w", err)
		}
		return items, nil

	default:
		// Object - check success flag first, then look for paginator.
		var top map[string]any
		if err := json.Unmarshal(raw, &top); err != nil {
			return nil, fmt.Errorf("decoding response object: %w", err)
		}

		// C3.
		if err := checkSuccessFlag(top); err != nil {
			return nil, err
		}

		// Laravel paginator shape: {"data":[…], …}
		dataRaw, ok := top["data"]
		if !ok {
			return nil, fmt.Errorf("unexpected object response: no 'data' array and no top-level array (decodeList called on a single-resource endpoint?)")
		}
		dataSlice, ok := dataRaw.([]any)
		if !ok {
			return nil, fmt.Errorf("paginator 'data' field is not an array (got %T)", dataRaw)
		}
		items := make([]map[string]any, 0, len(dataSlice))
		for i, v := range dataSlice {
			obj, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("paginator data[%d] is not an object (got %T)", i, v)
			}
			items = append(items, obj)
		}
		return items, nil
	}
}

// checkSuccessFlag inspects the "success" field of a decoded top-level object.
// If the field is present and is boolean false, an error is returned whose
// message is the value of the "message" field (fallback: "API returned success=false").
// If "success" is absent or true, nil is returned.
func checkSuccessFlag(top map[string]any) error {
	successRaw, hasSuccess := top["success"]
	if !hasSuccess {
		return nil
	}
	successBool, isBool := successRaw.(bool)
	if !isBool || successBool {
		return nil // absent or true - not an error
	}

	// success == false
	if msg, ok := top["message"].(string); ok && msg != "" {
		return fmt.Errorf("%s", msg)
	}
	return fmt.Errorf("API returned success=false")
}
