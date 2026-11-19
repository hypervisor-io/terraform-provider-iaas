package validators

import (
	"regexp"
	"strings"
)

// InstanceHostnameMessage is the one sentence the panel shows for a refused instance hostname (instance_input.hostname_invalid).
const InstanceHostnameMessage = "The hostname may contain only letters, digits and hyphens, with dots between parts. " +
	"Each part can be up to 63 characters and cannot start or end with a hyphen. The whole name can be up to 253 characters."

var instanceHostnameLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// InstanceHostnameProblem mirrors the panel's App\Rules\InstanceHostname (3.3.8.8): an RFC 1123 host name, dot separated labels
// of 1-63 letters, digits and hyphens with no hyphen at either end of a label, 253 characters (bytes) in all; upper case and a
// single label are accepted; an underscore, a space, any other character and non-ASCII (use the xn-- form) are not.
//
// It returns "" when the panel would accept the value, else the panel's own sentence. The panel also accepts ONE trailing dot
// and stores the name without it; the provider cannot: the stored value would differ from the configured one on every plan, so
// a trailing dot is refused here with its own sentence. An empty string is left to the panel.
func InstanceHostnameProblem(hostname string) string {
	if hostname == "" {
		return ""
	}
	if strings.HasSuffix(hostname, ".") {
		return "A trailing dot is not accepted by this provider: the platform stores the name without it, so the configured and stored values would never match. Remove the dot. " + InstanceHostnameMessage
	}
	if len(hostname) > 253 {
		return InstanceHostnameMessage
	}
	for _, label := range strings.Split(hostname, ".") {
		if !instanceHostnameLabel.MatchString(label) {
			return InstanceHostnameMessage
		}
	}
	return ""
}
