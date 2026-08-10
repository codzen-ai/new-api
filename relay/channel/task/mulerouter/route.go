package mulerouter

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// ContextKeyNativeRoute marks a request that arrived on the vendor-compatible
// /vendors/... routes, so responses are rendered in MuleRouter's own shape
// instead of the OpenAI video shape.
const ContextKeyNativeRoute = "mulerouter_native_route"

// PathRequest is a parsed /vendors/{vendor}/v1/{model}/{action}[/{task_id}] path.
type PathRequest struct {
	Vendor string
	Model  string
	Action string
	TaskID string
}

// ModelName is the internal model name this path maps to. It is the identity
// used for pricing, model permissions and route lookup.
func (p PathRequest) ModelName() string {
	return p.Vendor + "/" + p.Model + "/" + p.Action
}

// ParsePath splits the request path of a vendor-compatible route. `rest` is the
// wildcard tail after /vendors/{vendor}/v1. Two segments are a submit, three a
// task query; the action segment is variable in length across vendors
// (generation, text-to-video, video-diffusion), which is why the route is a
// wildcard rather than a fixed pattern.
func ParsePath(vendor, rest string) (PathRequest, bool) {
	vendor = strings.TrimSpace(vendor)
	segments := make([]string, 0, 3)
	for _, segment := range strings.Split(strings.Trim(rest, "/"), "/") {
		if segment = strings.TrimSpace(segment); segment != "" {
			segments = append(segments, segment)
		}
	}
	if vendor == "" || len(segments) < 2 || len(segments) > 3 {
		return PathRequest{}, false
	}

	parsed := PathRequest{Vendor: vendor, Model: segments[0], Action: segments[1]}
	if len(segments) == 3 {
		parsed.TaskID = segments[2]
	}
	return parsed, true
}

// IsVendorPath reports whether a request path belongs to the vendor-compatible
// routes.
func IsVendorPath(path string) bool {
	return strings.HasPrefix(path, dto.MuleRouterPathPrefix+"/")
}
