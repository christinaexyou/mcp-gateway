package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
)

func TestReconcileGuardrails(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)

	validConfigYAML := `
url: https://nemo-guardrails.internal:8080
configIDs:
  - tool-safety-v1
model: meta/llama-3.1-8b-instruct
`

	tests := []struct {
		name        string
		annotations map[string]string
		secrets     []corev1.Secret
		wantErr     bool
		errContains string
		wantConfig  *config.GuardrailsConfig
	}{
		{
			name:        "no guardrails-ref annotation clears config",
			annotations: nil,
			wantConfig:  nil,
		},
		{
			name:        "secret not found",
			annotations: map[string]string{labelGuardrailsReference: "missing"},
			wantErr:     true,
			errContains: "not found",
		},
		{
			name:        "missing label",
			annotations: map[string]string{labelGuardrailsReference: "no-label"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{Name: "no-label", Namespace: "test-ns"},
				Type:       guardrails.SecretTypeNeMo,
				Data:       map[string][]byte{"config.yaml": []byte(validConfigYAML)},
			}},
			wantErr:     true,
			errContains: "missing required label",
		},
		{
			name:        "invalid config data",
			annotations: map[string]string{labelGuardrailsReference: "bad-config"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "bad-config", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Type: guardrails.SecretTypeNeMo,
				Data: map[string][]byte{"config.yaml": []byte("model: only-model")},
			}},
			wantErr:     true,
			errContains: "is invalid",
		},
		{
			name:        "valid secret writes resolved config",
			annotations: map[string]string{labelGuardrailsReference: "good-config"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "good-config", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Type: guardrails.SecretTypeNeMo,
				Data: map[string][]byte{"config.yaml": []byte(validConfigYAML)},
			}},
			wantConfig: &config.GuardrailsConfig{
				URL:       "https://nemo-guardrails.internal:8080",
				ConfigIDs: []string{"tool-safety-v1"},
				Model:     "meta/llama-3.1-8b-instruct",
				FailMode:  guardrails.FailModeDeny,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]runtime.Object, len(tt.secrets))
			for i := range tt.secrets {
				objs[i] = &tt.secrets[i]
			}
			fc := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()

			r := &MCPGatewayExtensionReconciler{
				DirectAPIReader: fc,
			}

			mcpExt := &mcpv1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test-ns", Annotations: tt.annotations},
			}

			got, err := r.resolveGuardrails(context.Background(), mcpExt)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" {
					msg := err.Error()
					var valErr *validationError
					if errors.As(err, &valErr) {
						msg = valErr.message
					}
					if !strings.Contains(msg, tt.errContains) {
						t.Fatalf("error %q does not contain %q", msg, tt.errContains)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := tt.wantConfig
			if (got == nil) != (want == nil) {
				t.Fatalf("resolveGuardrails = %+v, want %+v", got, want)
			}
			if got != nil {
				if got.URL != want.URL || got.Model != want.Model || got.FailMode != want.FailMode ||
					strings.Join(got.ConfigIDs, ",") != strings.Join(want.ConfigIDs, ",") {
					t.Fatalf("resolveGuardrails = %+v, want %+v", got, want)
				}
			}
		})
	}
}

func TestResolveMaxBodyBytes(t *testing.T) {
	t.Run("defaults to 1 MiB when spec is unset", func(t *testing.T) {
		got := resolveMaxBodyBytes(&mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test-ns"},
		})
		if got != config.DefaultMaxBodyBytes {
			t.Fatalf("resolveMaxBodyBytes = %d, want %d", got, config.DefaultMaxBodyBytes)
		}
	})

	t.Run("uses spec maxBodyBytes", func(t *testing.T) {
		n := int32(4096)
		got := resolveMaxBodyBytes(&mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test-ns"},
			Spec:       mcpv1.MCPGatewayExtensionSpec{MaxBodyBytes: &n},
		})
		if got != 4096 {
			t.Fatalf("resolveMaxBodyBytes = %d, want 4096", got)
		}
	})
}

func TestReconcileExtensionConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)

	emptyFake := fake.NewClientBuilder().WithScheme(scheme).Build()

	t.Run("one write with default maxBodyBytes and nil guardrails", func(t *testing.T) {
		writer := &capturingConfigWriter{}
		r := &MCPGatewayExtensionReconciler{
			DirectAPIReader:     emptyFake,
			ConfigWriterDeleter: writer,
		}
		err := r.reconcileExtensionConfig(context.Background(), &mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test-ns"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if writer.writes != 1 {
			t.Fatalf("writes = %d, want 1", writer.writes)
		}
		if writer.last.GatewayCACertPEM == nil || *writer.last.GatewayCACertPEM != "" {
			t.Fatalf("GatewayCACertPEM = %v, want empty string", writer.last.GatewayCACertPEM)
		}
		if writer.last.GlobalGuardrails == nil || writer.last.GlobalGuardrails.Config != nil {
			t.Fatalf("GlobalGuardrails = %+v, want clear", writer.last.GlobalGuardrails)
		}
		if writer.last.MaxBodyBytes == nil || *writer.last.MaxBodyBytes != config.DefaultMaxBodyBytes {
			t.Fatalf("MaxBodyBytes = %v, want %d", writer.last.MaxBodyBytes, config.DefaultMaxBodyBytes)
		}
	})

	t.Run("one write with spec maxBodyBytes and resolved guardrails", func(t *testing.T) {
		n := int32(4096)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "good-config", Namespace: "test-ns",
				Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
			},
			Type: guardrails.SecretTypeNeMo,
			Data: map[string][]byte{"config.yaml": []byte(`
url: https://nemo-guardrails.internal:8080
configIDs:
  - tool-safety-v1
model: meta/llama-3.1-8b-instruct
`)},
		}
		fc := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(secret).Build()
		writer := &capturingConfigWriter{}
		r := &MCPGatewayExtensionReconciler{
			DirectAPIReader:     fc,
			ConfigWriterDeleter: writer,
		}
		err := r.reconcileExtensionConfig(context.Background(), &mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test", Namespace: "test-ns",
				Annotations: map[string]string{labelGuardrailsReference: "good-config"},
			},
			Spec: mcpv1.MCPGatewayExtensionSpec{MaxBodyBytes: &n},
		})
		if err != nil {
			t.Fatal(err)
		}
		if writer.writes != 1 {
			t.Fatalf("writes = %d, want 1", writer.writes)
		}
		if writer.last.MaxBodyBytes == nil || *writer.last.MaxBodyBytes != 4096 {
			t.Fatalf("MaxBodyBytes = %v, want 4096", writer.last.MaxBodyBytes)
		}
		got := writer.last.GlobalGuardrails
		if got == nil || got.Config == nil || got.Config.URL != "https://nemo-guardrails.internal:8080" ||
			strings.Join(got.Config.ConfigIDs, ",") != "tool-safety-v1" {
			t.Fatalf("GlobalGuardrails = %+v", got)
		}
	})

	t.Run("missing rails secret clears GlobalGuardrails", func(t *testing.T) {
		validPEM := generateTestCACertPEM(t)
		caSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "valid-ca", Namespace: "test-ns",
				Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
			},
			Data: map[string][]byte{"ca.crt": validPEM},
		}
		fc := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(caSecret).Build()
		writer := &capturingConfigWriter{}
		r := &MCPGatewayExtensionReconciler{
			DirectAPIReader:     fc,
			ConfigWriterDeleter: writer,
		}
		err := r.reconcileExtensionConfig(context.Background(), &mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test", Namespace: "test-ns",
				Annotations: map[string]string{labelGuardrailsReference: "missing"},
			},
			Spec: mcpv1.MCPGatewayExtensionSpec{
				CACertBundleRef: &mcpv1.CACertBundleReference{Name: "valid-ca"},
			},
		})
		if err == nil {
			t.Fatal("expected rails error")
		}
		if writer.writes != 1 {
			t.Fatalf("writes = %d, want 1", writer.writes)
		}
		if writer.last.GatewayCACertPEM == nil || *writer.last.GatewayCACertPEM != string(validPEM) {
			t.Fatalf("CA PEM not written on rails error")
		}
		if writer.last.GlobalGuardrails == nil || writer.last.GlobalGuardrails.Config != nil {
			t.Fatalf("GlobalGuardrails = %+v, want clear", writer.last.GlobalGuardrails)
		}
		if writer.last.MaxBodyBytes == nil || *writer.last.MaxBodyBytes != config.DefaultMaxBodyBytes {
			t.Fatalf("MaxBodyBytes = %v, want default", writer.last.MaxBodyBytes)
		}
	})

	t.Run("invalid rails secret keeps last GlobalGuardrails", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "bad-config", Namespace: "test-ns",
				Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
			},
			Type: guardrails.SecretTypeNeMo,
			Data: map[string][]byte{"config.yaml": []byte("model: only-model")},
		}
		fc := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(secret).Build()
		writer := &capturingConfigWriter{}
		r := &MCPGatewayExtensionReconciler{
			DirectAPIReader:     fc,
			ConfigWriterDeleter: writer,
		}
		err := r.reconcileExtensionConfig(context.Background(), &mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test", Namespace: "test-ns",
				Annotations: map[string]string{labelGuardrailsReference: "bad-config"},
			},
		})
		if err == nil {
			t.Fatal("expected rails error")
		}
		if writer.writes != 1 {
			t.Fatalf("writes = %d, want 1", writer.writes)
		}
		if writer.last.GlobalGuardrails != nil {
			t.Fatalf("GlobalGuardrails = %+v, want omitted", writer.last.GlobalGuardrails)
		}
		if writer.last.MaxBodyBytes == nil || *writer.last.MaxBodyBytes != config.DefaultMaxBodyBytes {
			t.Fatalf("MaxBodyBytes = %v, want default", writer.last.MaxBodyBytes)
		}
	})

	t.Run("CA validation error still writes rails and maxBodyBytes", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "good-config", Namespace: "test-ns",
				Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
			},
			Type: guardrails.SecretTypeNeMo,
			Data: map[string][]byte{"config.yaml": []byte(`
url: https://nemo-guardrails.internal:8080
configIDs:
  - tool-safety-v1
model: meta/llama-3.1-8b-instruct
`)},
		}
		fc := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(secret).Build()
		writer := &capturingConfigWriter{}
		r := &MCPGatewayExtensionReconciler{
			DirectAPIReader:     fc,
			ConfigWriterDeleter: writer,
		}
		err := r.reconcileExtensionConfig(context.Background(), &mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test", Namespace: "test-ns",
				Annotations: map[string]string{labelGuardrailsReference: "good-config"},
			},
			Spec: mcpv1.MCPGatewayExtensionSpec{
				CACertBundleRef: &mcpv1.CACertBundleReference{Name: "missing-ca"},
			},
		})
		if err == nil {
			t.Fatal("expected CA error")
		}
		if writer.writes != 1 {
			t.Fatalf("writes = %d, want 1", writer.writes)
		}
		if writer.last.GatewayCACertPEM != nil {
			t.Fatalf("GatewayCACertPEM = %v, want omitted", writer.last.GatewayCACertPEM)
		}
		got := writer.last.GlobalGuardrails
		if got == nil || got.Config == nil || got.Config.URL != "https://nemo-guardrails.internal:8080" {
			t.Fatalf("GlobalGuardrails = %+v, want written", got)
		}
		if writer.last.MaxBodyBytes == nil || *writer.last.MaxBodyBytes != config.DefaultMaxBodyBytes {
			t.Fatalf("MaxBodyBytes = %v, want default", writer.last.MaxBodyBytes)
		}
	})
}
