package validators

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestIsValidIPsecPSK(t *testing.T) {
	good := []string{strings.Repeat("a", 16), strings.Repeat("Z9!#", 32), "correct-horse-battery-staple"}
	for _, s := range good {
		if !IsValidIPsecPSK(s) {
			t.Errorf("expected %d-char key to be valid", len(s))
		}
	}
	bad := []string{
		"", strings.Repeat("a", 15), strings.Repeat("a", 129),
		strings.Repeat("a", 15) + " ", strings.Repeat("a", 15) + "\"", strings.Repeat("a", 15) + "\\",
		strings.Repeat("a", 15) + "\n", strings.Repeat("a", 15) + "\x7f", strings.Repeat("a", 15) + "é",
	}
	for _, s := range bad {
		if IsValidIPsecPSK(s) {
			t.Errorf("expected %q to be invalid", s)
		}
	}
}

func TestIPsecPSK_neverEchoesTheValue(t *testing.T) {
	secret := "sec ret-with-space"
	resp := &validator.StringResponse{}
	IPsecPSK().ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringValue(secret)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	for _, d := range resp.Diagnostics {
		if strings.Contains(d.Summary()+d.Detail(), "sec") && strings.Contains(d.Detail(), "ret-with") {
			t.Errorf("diagnostic echoes the key: %s", d.Detail())
		}
	}
}

func TestIPv4CIDRPrefixBetween(t *testing.T) {
	v := IPv4CIDRPrefixBetween(8, 32)
	cases := map[string]bool{
		"192.168.50.0/24": true, "10.0.0.0/8": true, "203.0.113.7/32": true,
		"0.0.0.0/0": false, "10.0.0.0/7": false, "10.0.0.0": false, "nope": false, "10.0.0.0/24\n": false,
	}
	for in, ok := range cases {
		resp := &validator.StringResponse{}
		v.ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringValue(in)}, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("%q: valid=%v, diagnostics error=%v", in, ok, resp.Diagnostics.HasError())
		}
	}
}

func TestIPsecSelectorProblem(t *testing.T) {
	cases := []struct {
		list   []string
		remote bool
		bad    bool
	}{
		{[]string{"192.168.50.0/24"}, true, false},
		{[]string{"192.168.50.7/24"}, true, true},                   // host bits set
		{[]string{"192.168.0.0/16", "192.168.50.0/24"}, true, true}, // overlap inside the list
		{[]string{"10.0.0.0/24", "10.0.1.0/24"}, true, false},
		{[]string{"127.0.0.0/24"}, true, true},   // loopback
		{[]string{"169.254.1.0/24"}, true, true}, // link-local
		{[]string{"224.0.0.0/8"}, true, true},    // multicast
		{[]string{"250.0.0.0/8"}, true, true},    // class E
		{[]string{"127.0.0.0/24"}, false, false}, // reserved ranges apply to remote only
		{[]string{"10.60.0.0/16", "10.60.5.0/24"}, false, true},
	}
	for _, c := range cases {
		got := IPsecSelectorProblem(c.list, c.remote)
		if (got != "") != c.bad {
			t.Errorf("%v remote=%v: problem=%q, want bad=%v", c.list, c.remote, got, c.bad)
		}
	}
}

func TestIsPlainOptionalString(t *testing.T) {
	for in, want := range map[string]bool{"": false, " ": false, " a": false, "a ": false, "a\n": false, "a b": true, "198.51.100.7": true} {
		if IsPlainOptionalString(in) != want {
			t.Errorf("%q: want %v", in, want)
		}
	}
}
