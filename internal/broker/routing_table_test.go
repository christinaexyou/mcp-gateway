package broker

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

// resourceCapableMockServer implements upstream.ActiveMCPServer with a
// configurable Config()/SupportsResources(), needed for buildRoutingTable's
// resource-prefix skip conditions - the package's other mock
// (mockActiveServer in version_test.go) hardcodes both, so isn't reusable here.
type resourceCapableMockServer struct {
	cfg               config.MCPServer
	supportsResources bool
}

func (m *resourceCapableMockServer) Stop()           {}
func (m *resourceCapableMockServer) MCPName() string { return m.cfg.Name }
func (m *resourceCapableMockServer) GetStatus() upstream.ServerValidationStatus {
	return upstream.ServerValidationStatus{}
}
func (m *resourceCapableMockServer) GetManagedTools() []mcp.Tool           { return nil }
func (m *resourceCapableMockServer) GetServedManagedTool(string) *mcp.Tool { return nil }
func (m *resourceCapableMockServer) GetToolHints(string) (upstream.ToolHints, bool) {
	return upstream.ToolHints{}, false
}
func (m *resourceCapableMockServer) GetManagedPrompts() []mcp.Prompt           { return nil }
func (m *resourceCapableMockServer) GetServedManagedPrompt(string) *mcp.Prompt { return nil }
func (m *resourceCapableMockServer) Config() config.MCPServer                  { return m.cfg }
func (m *resourceCapableMockServer) SupportedVersions() []string               { return nil }
func (m *resourceCapableMockServer) SupportsVersion(string) bool               { return false }
func (m *resourceCapableMockServer) ToolsCacheMetadata() upstream.CacheMetadata {
	return upstream.CacheMetadata{}
}
func (m *resourceCapableMockServer) PromptsCacheMetadata() upstream.CacheMetadata {
	return upstream.CacheMetadata{}
}
func (m *resourceCapableMockServer) SupportsResources() bool { return m.supportsResources }
func (m *resourceCapableMockServer) ListResources(context.Context) (*mcp.ListResourcesResult, error) {
	return nil, ErrListResourcesNotImplemented
}

// TestBuildRoutingTable_ResourcePrefixSkipConditions confirms
// buildRoutingTable registers a resource-prefix route only for servers that
// pass every one of FetchResources' own skip conditions (resource-capable,
// non-empty prefix, prefix matches the allowlist) - resources/read routing
// and resources/list fetching must agree on which servers participate, or a
// server excluded from list could still (or could no longer) be reachable
// via read.
func TestBuildRoutingTable_ResourcePrefixSkipConditions(t *testing.T) {
	b := &mcpBrokerImpl{
		logger: slog.Default(),
		mcpServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"good":      &resourceCapableMockServer{cfg: config.MCPServer{Name: "good", Prefix: "good_"}, supportsResources: true},
			"noprefix":  &resourceCapableMockServer{cfg: config.MCPServer{Name: "noprefix", Prefix: ""}, supportsResources: true},
			"badprefix": &resourceCapableMockServer{cfg: config.MCPServer{Name: "badprefix", Prefix: "Bad-Prefix!"}, supportsResources: true},
			"unsupported": &resourceCapableMockServer{
				cfg:               config.MCPServer{Name: "unsupported", Prefix: "unsup_"},
				supportsResources: false,
			},
		},
	}

	table := b.buildRoutingTable()

	route, ok := table.LookupResourcePrefix("good_template.html")
	assert.True(t, ok, "resource-capable server with a valid prefix should be routable")
	assert.Equal(t, "good", route.Name)

	_, ok = table.LookupResourcePrefix("noprefix_template.html")
	assert.False(t, ok, "server with no prefix must not be resource-routable")

	_, ok = table.LookupResourcePrefix("Bad-Prefix!template.html")
	assert.False(t, ok, "server with a prefix failing the charset allowlist must not be resource-routable")

	_, ok = table.LookupResourcePrefix("unsup_template.html")
	assert.False(t, ok, "server that doesn't support resources must not be resource-routable")
}

// TestBuildRoutingTable_CopiesGuardrailsConfigIDs verifies the broker wiring: a
// registered server's per-server guardrails config IDs must survive onto
// the routing.ServerRoute the router looks up, since that's how the
// request-side check learns which configIDs to send to the guardrails
// server for this tool's server.
func TestBuildRoutingTable_CopiesGuardrailsConfigIDs(t *testing.T) {
	b := &mcpBrokerImpl{
		logger: slog.Default(),
		mcpServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"guarded": &resourceCapableMockServer{cfg: config.MCPServer{
				Name:                "guarded",
				Prefix:              "guarded_",
				UserSpecificList:    true,
				GuardrailsConfigIDs: []string{"config-a", "config-b"},
			}},
			"unguarded": &resourceCapableMockServer{cfg: config.MCPServer{
				Name:             "unguarded",
				Prefix:           "unguarded_",
				UserSpecificList: true,
			}},
		},
	}

	table := b.buildRoutingTable()

	route, ok := table.LookupPrefix("guarded_")
	assert.True(t, ok)
	assert.Equal(t, []string{"config-a", "config-b"}, route.GuardrailsConfigIDs)

	route, ok = table.LookupPrefix("unguarded_")
	assert.True(t, ok)
	assert.Empty(t, route.GuardrailsConfigIDs)
}
