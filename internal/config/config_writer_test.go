package config

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func newTestSecretReaderWriter(t *testing.T) *SecretReaderWriter {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()
	logger := slog.New(slog.DiscardHandler)
	return &SecretReaderWriter{
		Client: fakeClient,
		Scheme: scheme,
		Logger: logger,
	}
}

func TestUpsertMCPServer(t *testing.T) {
	testCases := []struct {
		name           string
		serversToAdd   []MCPServer
		expectedCount  int
		expectedServer MCPServer // checks first server expectedCount == 1
	}{
		{
			name: "creates secret if not exists",
			serversToAdd: []MCPServer{
				{Name: "test-server", URL: "http://test.local:8080/mcp", Prefix: "test_", State: string(mcpv1.ServerStateEnabled)},
			},
			expectedCount:  1,
			expectedServer: MCPServer{Name: "test-server", URL: "http://test.local:8080/mcp", Prefix: "test_"},
		},
		{
			name: "updates existing server",
			serversToAdd: []MCPServer{
				{Name: "test-server", URL: "http://old.local:8080/mcp", Prefix: "old_", State: string(mcpv1.ServerStateEnabled)},
				{Name: "test-server", URL: "http://new.local:8080/mcp", Prefix: "new_", State: string(mcpv1.ServerStateEnabled)},
			},
			expectedCount:  1,
			expectedServer: MCPServer{Name: "test-server", URL: "http://new.local:8080/mcp", Prefix: "new_"},
		},
		{
			name: "appends new server",
			serversToAdd: []MCPServer{
				{Name: "server1", URL: "http://s1.local/mcp", State: string(mcpv1.ServerStateEnabled)},
				{Name: "server2", URL: "http://s2.local/mcp", State: string(mcpv1.ServerStateEnabled)},
			},
			expectedCount: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srw := newTestSecretReaderWriter(t)
			ctx := context.Background()
			namespaceName := types.NamespacedName{Namespace: "test-ns", Name: "mcp-gateway-config"}

			for i, server := range tc.serversToAdd {
				if err := srw.UpsertMCPServer(ctx, server, namespaceName); err != nil {
					t.Fatalf("UpsertMCPServer[%d] failed: %v", i, err)
				}
			}

			secret := &corev1.Secret{}
			if err := srw.Client.Get(ctx, namespaceName, secret); err != nil {
				t.Fatalf("failed to get secret: %v", err)
			}

			configData := secret.StringData[configFileName]
			if configData == "" {
				configData = string(secret.Data[configFileName])
			}
			var config BrokerConfig
			if err := yaml.Unmarshal([]byte(configData), &config); err != nil {
				t.Fatalf("failed to unmarshal config: %v", err)
			}

			if len(config.Servers) != tc.expectedCount {
				t.Fatalf("expected %d server(s), got %d", tc.expectedCount, len(config.Servers))
			}

			if tc.expectedCount == 1 && tc.expectedServer.Name != "" {
				if config.Servers[0].Name != tc.expectedServer.Name {
					t.Errorf("expected name %q, got %q", tc.expectedServer.Name, config.Servers[0].Name)
				}
				if config.Servers[0].URL != tc.expectedServer.URL {
					t.Errorf("expected URL %q, got %q", tc.expectedServer.URL, config.Servers[0].URL)
				}
				if config.Servers[0].Prefix != tc.expectedServer.Prefix {
					t.Errorf("expected Prefix %q, got %q", tc.expectedServer.Prefix, config.Servers[0].Prefix)
				}
			}
		})
	}
}

func TestRemoveMCPServer_RemovesFromConfig(t *testing.T) {
	srw := newTestSecretReaderWriter(t)
	ctx := context.Background()
	namespaceName := types.NamespacedName{Namespace: "test-ns", Name: "mcp-gateway-config"}

	// insert two servers
	server1 := MCPServer{Name: "server1", URL: "http://s1.local/mcp", State: string(mcpv1.ServerStateEnabled)}
	server2 := MCPServer{Name: "server2", URL: "http://s2.local/mcp", State: string(mcpv1.ServerStateEnabled)}
	if err := srw.UpsertMCPServer(ctx, server1, namespaceName); err != nil {
		t.Fatalf("UpsertMCPServer server1 failed: %v", err)
	}
	if err := srw.UpsertMCPServer(ctx, server2, namespaceName); err != nil {
		t.Fatalf("UpsertMCPServer server2 failed: %v", err)
	}

	// remove server1
	if err := srw.RemoveMCPServer(ctx, "server1"); err != nil {
		t.Fatalf("RemoveMCPServer failed: %v", err)
	}

	// verify only server2 remains
	secret := &corev1.Secret{}
	if err := srw.Client.Get(ctx, namespaceName, secret); err != nil {
		t.Fatalf("failed to get secret: %v", err)
	}

	configData := secret.StringData[configFileName]
	if configData == "" {
		configData = string(secret.Data[configFileName])
	}
	var config BrokerConfig
	if err := yaml.Unmarshal([]byte(configData), &config); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}

	if len(config.Servers) != 1 {
		t.Fatalf("expected 1 server after removal, got %d", len(config.Servers))
	}
	if config.Servers[0].Name != "server2" {
		t.Fatalf("expected server2 to remain, got '%s'", config.Servers[0].Name)
	}
}

func TestEnsureConfigExists_CreatesSecretIfNotExists(t *testing.T) {
	srw := newTestSecretReaderWriter(t)
	ctx := context.Background()
	namespaceName := types.NamespacedName{Namespace: "test-ns", Name: "mcp-gateway-config"}

	if err := srw.EnsureConfigExists(ctx, namespaceName); err != nil {
		t.Fatalf("EnsureConfigExists failed: %v", err)
	}

	// verify secret was created
	secret := &corev1.Secret{}
	if err := srw.Client.Get(ctx, namespaceName, secret); err != nil {
		t.Fatalf("failed to get created secret: %v", err)
	}

	// verify it has the correct labels
	if secret.Labels["mcp.kuadrant.io/aggregated"] != "true" {
		t.Fatal("secret missing aggregated label")
	}
	if secret.Labels["mcp.kuadrant.io/secret"] != "true" {
		t.Fatal("secret missing managed secret label")
	}
}

func TestDeleteConfig(t *testing.T) {
	testCases := []struct {
		name         string
		createFirst  bool
		secretName   string
		expectExists bool
	}{
		{
			name:         "deletes existing secret",
			createFirst:  true,
			secretName:   "mcp-gateway-config",
			expectExists: false,
		},
		{
			name:         "no error if secret does not exist",
			createFirst:  false,
			secretName:   "nonexistent",
			expectExists: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srw := newTestSecretReaderWriter(t)
			ctx := context.Background()
			namespaceName := types.NamespacedName{Namespace: "test-ns", Name: tc.secretName}

			if tc.createFirst {
				if err := srw.EnsureConfigExists(ctx, namespaceName); err != nil {
					t.Fatalf("EnsureConfigExists failed: %v", err)
				}
			}

			if err := srw.DeleteConfig(ctx, namespaceName); err != nil {
				t.Fatalf("DeleteConfig failed: %v", err)
			}

			secret := &corev1.Secret{}
			err := srw.Client.Get(ctx, namespaceName, secret)
			exists := err == nil

			if exists != tc.expectExists {
				t.Fatalf("expected exists=%v, got exists=%v", tc.expectExists, exists)
			}
		})
	}
}

type countingClient struct {
	client.Client
	creates int
	updates int
}

func (c *countingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.creates++
	return c.Client.Create(ctx, obj, opts...)
}

func (c *countingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updates++
	return c.Client.Update(ctx, obj, opts...)
}

func TestWriteExtensionConfig(t *testing.T) {
	ctx := context.Background()
	namespaceName := types.NamespacedName{Namespace: "test-ns", Name: "mcp-gateway-config"}
	rails := &GuardrailsConfig{
		URL:       "https://nemo-guardrails.internal:8080",
		ConfigIDs: []string{"tool-safety-v1"},
		Model:     "meta/llama-3.1-8b-instruct",
		FailMode:  "deny",
	}
	fullPatch := ExtensionOwnedConfig{
		GatewayCACertPEM: ptr("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"),
		GlobalGuardrails: &GuardrailsUpdate{Config: rails},
		MaxBodyBytes:     ptr(DefaultMaxBodyBytes),
	}

	t.Run("missing secret is one Create", func(t *testing.T) {
		srw := newTestSecretReaderWriter(t)
		counter := &countingClient{Client: srw.Client}
		srw.Client = counter

		if err := srw.WriteExtensionConfig(ctx, fullPatch, namespaceName); err != nil {
			t.Fatalf("WriteExtensionConfig: %v", err)
		}
		if counter.creates != 1 || counter.updates != 0 {
			t.Fatalf("creates=%d updates=%d, want 1 create and 0 updates", counter.creates, counter.updates)
		}

		cfg := readTestBrokerConfig(t, srw, namespaceName)
		if cfg.GatewayCACertPEM != *fullPatch.GatewayCACertPEM {
			t.Fatalf("GatewayCACertPEM = %q", cfg.GatewayCACertPEM)
		}
		if cfg.GlobalGuardrails == nil || cfg.GlobalGuardrails.URL != rails.URL ||
			cfg.GlobalGuardrails.Model != rails.Model || cfg.GlobalGuardrails.FailMode != rails.FailMode ||
			!slices.Equal(cfg.GlobalGuardrails.ConfigIDs, rails.ConfigIDs) {
			t.Fatalf("GlobalGuardrails = %+v, want %+v", cfg.GlobalGuardrails, rails)
		}
		if cfg.MaxBodyBytes != DefaultMaxBodyBytes {
			t.Fatalf("MaxBodyBytes = %d, want %d", cfg.MaxBodyBytes, DefaultMaxBodyBytes)
		}
		if cfg.Servers == nil {
			t.Fatal("Servers is nil, want empty slice")
		}
		raw := secretYAML(t, srw, namespaceName)
		if !strings.Contains(raw, "servers: []") || strings.Contains(raw, "servers: null") {
			t.Fatalf("Create YAML = %q, want servers: []", raw)
		}
	})

	t.Run("identical write is a no-op", func(t *testing.T) {
		srw := newTestSecretReaderWriter(t)
		if err := srw.WriteExtensionConfig(ctx, fullPatch, namespaceName); err != nil {
			t.Fatalf("seed: %v", err)
		}
		counter := &countingClient{Client: srw.Client}
		srw.Client = counter
		if err := srw.WriteExtensionConfig(ctx, fullPatch, namespaceName); err != nil {
			t.Fatalf("WriteExtensionConfig: %v", err)
		}
		if counter.creates != 0 || counter.updates != 0 {
			t.Fatalf("creates=%d updates=%d, want no writes", counter.creates, counter.updates)
		}
	})

	t.Run("omitted guardrails preserves existing and keeps servers", func(t *testing.T) {
		srw := newTestSecretReaderWriter(t)
		if err := srw.UpsertMCPServer(ctx, MCPServer{
			Name: "keep-me", URL: "http://keep.local/mcp", State: string(mcpv1.ServerStateEnabled),
		}, namespaceName); err != nil {
			t.Fatalf("UpsertMCPServer: %v", err)
		}
		if err := srw.WriteExtensionConfig(ctx, fullPatch, namespaceName); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := srw.WriteExtensionConfig(ctx, ExtensionOwnedConfig{
			MaxBodyBytes: ptr(int64(4096)),
		}, namespaceName); err != nil {
			t.Fatalf("WriteExtensionConfig: %v", err)
		}
		cfg := readTestBrokerConfig(t, srw, namespaceName)
		if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "keep-me" {
			t.Fatalf("servers = %+v, want keep-me preserved", cfg.Servers)
		}
		if cfg.GlobalGuardrails == nil || !slices.Equal(cfg.GlobalGuardrails.ConfigIDs, rails.ConfigIDs) {
			t.Fatalf("GlobalGuardrails = %+v, want preserved", cfg.GlobalGuardrails)
		}
		if cfg.GatewayCACertPEM != *fullPatch.GatewayCACertPEM {
			t.Fatalf("GatewayCACertPEM cleared")
		}
		if cfg.MaxBodyBytes != 4096 {
			t.Fatalf("MaxBodyBytes = %d, want 4096", cfg.MaxBodyBytes)
		}
	})

	t.Run("nil guardrails config clears that field", func(t *testing.T) {
		srw := newTestSecretReaderWriter(t)
		if err := srw.WriteExtensionConfig(ctx, fullPatch, namespaceName); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := srw.WriteExtensionConfig(ctx, ExtensionOwnedConfig{
			GlobalGuardrails: &GuardrailsUpdate{Config: nil},
		}, namespaceName); err != nil {
			t.Fatalf("WriteExtensionConfig: %v", err)
		}
		cfg := readTestBrokerConfig(t, srw, namespaceName)
		if cfg.GlobalGuardrails != nil {
			t.Fatalf("GlobalGuardrails = %+v, want nil", cfg.GlobalGuardrails)
		}
		if cfg.GatewayCACertPEM != *fullPatch.GatewayCACertPEM || cfg.MaxBodyBytes != DefaultMaxBodyBytes {
			t.Fatalf("other fields changed: %+v", cfg)
		}
	})
}

func ptr[T any](v T) *T { return &v }

func readTestBrokerConfig(t *testing.T, srw *SecretReaderWriter, namespaceName types.NamespacedName) BrokerConfig {
	t.Helper()
	secret := &corev1.Secret{}
	if err := srw.Client.Get(context.Background(), namespaceName, secret); err != nil {
		t.Fatalf("failed to get secret: %v", err)
	}
	configData := secret.StringData[configFileName]
	if configData == "" {
		configData = string(secret.Data[configFileName])
	}
	var cfg BrokerConfig
	if err := yaml.Unmarshal([]byte(configData), &cfg); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}
	return cfg
}

func secretYAML(t *testing.T, srw *SecretReaderWriter, namespaceName types.NamespacedName) string {
	t.Helper()
	secret := &corev1.Secret{}
	if err := srw.Client.Get(context.Background(), namespaceName, secret); err != nil {
		t.Fatalf("failed to get secret: %v", err)
	}
	if s := secret.StringData[configFileName]; s != "" {
		return s
	}
	return string(secret.Data[configFileName])
}
