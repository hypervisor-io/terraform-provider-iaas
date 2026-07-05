package client

import (
	"strings"
	"testing"
)

// ----------------------------------------------------------------------------
// decodeList tests
// ----------------------------------------------------------------------------

func TestDecodeList_Paginator(t *testing.T) {
	body := []byte(`{"current_page":1,"data":[{"id":"a"},{"id":"b"}],"per_page":10,"total":2}`)

	items, err := decodeList(body)
	if err != nil {
		t.Fatalf("decodeList returned unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items; got %d", len(items))
	}
	if items[0]["id"] != "a" {
		t.Errorf("items[0][id] = %v; want %q", items[0]["id"], "a")
	}
	if items[1]["id"] != "b" {
		t.Errorf("items[1][id] = %v; want %q", items[1]["id"], "b")
	}
}

func TestDecodeList_TopLevelArray(t *testing.T) {
	body := []byte(`[{"id":"a"}]`)

	items, err := decodeList(body)
	if err != nil {
		t.Fatalf("decodeList returned unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item; got %d", len(items))
	}
	if items[0]["id"] != "a" {
		t.Errorf("items[0][id] = %v; want %q", items[0]["id"], "a")
	}
}

func TestDecodeList_SuccessFalse(t *testing.T) {
	body := []byte(`{"success":false,"message":"boom"}`)

	_, err := decodeList(body)
	if err == nil {
		t.Fatal("decodeList: expected error for success=false, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error message %q does not contain %q", err.Error(), "boom")
	}
}

func TestDecodeList_MalformedJSON(t *testing.T) {
	body := []byte(`{not valid json`)

	_, err := decodeList(body)
	if err == nil {
		t.Fatal("decodeList: expected error for malformed JSON, got nil")
	}
}

func TestDecodeList_DataNotArray(t *testing.T) {
	// "data" field is a string, not an array - must error, not panic.
	body := []byte(`{"data":"nope"}`)

	_, err := decodeList(body)
	if err == nil {
		t.Fatal("decodeList: expected error when 'data' is not an array, got nil")
	}
	if !strings.Contains(err.Error(), "not an array") {
		t.Errorf("error message %q does not mention 'not an array'", err.Error())
	}
}

func TestDecodeList_DataElementNotObject(t *testing.T) {
	// data[1] is a scalar - must error mentioning the index.
	body := []byte(`{"data":[{"id":"a"},"scalar"]}`)

	_, err := decodeList(body)
	if err == nil {
		t.Fatal("decodeList: expected error when data element is not an object, got nil")
	}
	if !strings.Contains(err.Error(), "data[1]") {
		t.Errorf("error message %q does not mention 'data[1]'", err.Error())
	}
}

// ----------------------------------------------------------------------------
// decodeItem tests
// ----------------------------------------------------------------------------

func TestDecodeItem_BareResourceObject(t *testing.T) {
	// SHOW: {"ssh_key":{"id":"k1","name":"x"}}
	body := []byte(`{"ssh_key":{"id":"k1","name":"x"}}`)

	obj, err := decodeItem(body, "ssh_key")
	if err != nil {
		t.Fatalf("decodeItem returned unexpected error: %v", err)
	}
	if obj["id"] != "k1" {
		t.Errorf("id = %v; want %q", obj["id"], "k1")
	}
	if obj["name"] != "x" {
		t.Errorf("name = %v; want %q", obj["name"], "x")
	}
}

func TestDecodeItem_SuccessWithResource(t *testing.T) {
	// SHOW/CREATE: {"success":true,"vpc":{"id":"v1"}}
	body := []byte(`{"success":true,"vpc":{"id":"v1"}}`)

	obj, err := decodeItem(body, "vpc")
	if err != nil {
		t.Fatalf("decodeItem returned unexpected error: %v", err)
	}
	if obj["id"] != "v1" {
		t.Errorf("id = %v; want %q", obj["id"], "v1")
	}
}

func TestDecodeItem_SuccessFalse(t *testing.T) {
	// C3: success=false must be an error even with HTTP 200
	body := []byte(`{"success":false,"message":"boom"}`)

	_, err := decodeItem(body, "vpc")
	if err == nil {
		t.Fatal("decodeItem: expected error for success=false, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error message %q does not contain %q", err.Error(), "boom")
	}
}

func TestDecodeItem_VPCCreateNoObject(t *testing.T) {
	// VPC create: {"success":true,"message":"queued"} - no sub-object for "vpc".
	// Must return the bare top-level map (not an error); caller detects missing id.
	body := []byte(`{"success":true,"message":"queued"}`)

	obj, err := decodeItem(body, "vpc")
	if err != nil {
		t.Fatalf("decodeItem returned unexpected error: %v", err)
	}
	// The returned map should be the top-level envelope.
	if obj["message"] != "queued" {
		t.Errorf("bare envelope: message = %v; want %q", obj["message"], "queued")
	}
	// Callers detect the missing id by checking obj["id"] == nil.
	if _, hasID := obj["id"]; hasID {
		t.Error("bare envelope must not contain 'id' key")
	}
}

func TestDecodeItem_MalformedJSON(t *testing.T) {
	body := []byte(`{bad json`)

	_, err := decodeItem(body, "ssh_key")
	if err == nil {
		t.Fatal("decodeItem: expected error for malformed JSON, got nil")
	}
}
