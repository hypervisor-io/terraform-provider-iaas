package validators

import (
	"strings"
	"testing"
)

func TestInstanceHostnameProblem(t *testing.T) {
	label63 := strings.Repeat("a", 63)
	ok := []string{"web-01", "WEB", "a", "vps1.example.com", "xn--bcher-kva.example", "web--1", "a1", label63, label63 + "." + label63 + "." + label63 + "." + strings.Repeat("b", 61), "1.2.3.4.in-addr.arpa", ""}
	for _, h := range ok {
		if p := InstanceHostnameProblem(h); p != "" {
			t.Errorf("%q should pass, got %q", h, p)
		}
	}
	bad := []string{"web_01", "-web", "web-", "a..b", ".a", "a b", "web;1", "w'x", "bücher", strings.Repeat("a", 64), strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 62), "a\nb", "a.-b", "a-.b", "."}
	for _, h := range bad {
		if InstanceHostnameProblem(h) == "" {
			t.Errorf("%q should be refused", h)
		}
	}
	if p := InstanceHostnameProblem("web."); !strings.Contains(p, "trailing dot") {
		t.Errorf("trailing dot sentence missing: %q", p)
	}
	if p := InstanceHostnameProblem("web_1"); p != InstanceHostnameMessage {
		t.Errorf("underscore must carry the panel sentence, got %q", p)
	}
}
