package wabsignal

import "testing"

func TestNormalizeURLPreservesOTLPPath(t *testing.T) {
	t.Parallel()

	got, err := normalizeURL("https://otlp-gateway-prod-ca-east-0.grafana.net/otlp", "otlp endpoint", true)
	if err != nil {
		t.Fatalf("normalizeURL returned error: %v", err)
	}
	want := "https://otlp-gateway-prod-ca-east-0.grafana.net/otlp"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestNormalizeURLStripsGrafanaPathForReadAPI(t *testing.T) {
	t.Parallel()

	got, err := normalizeURL("https://example.grafana.net/api/ds/query", "grafana api url", false)
	if err != nil {
		t.Fatalf("normalizeURL returned error: %v", err)
	}
	want := "https://example.grafana.net"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
