package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestValidateProviderOutboundURLRequiresHTTPSForPublicHosts(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "false")
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "")

	if _, err := validateProviderOutboundURL("http://8.8.8.8/v1"); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("public HTTP URL error = %v, want HTTPS rejection", err)
	}
	if _, err := validateProviderOutboundURL("https://8.8.8.8/v1"); err != nil {
		t.Fatalf("public HTTPS URL rejected: %v", err)
	}
}

func TestValidateProviderOutboundURLAllowsOnlyExplicitPrivateHTTP(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "false")
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")

	if _, err := validateProviderOutboundURL("http://127.0.0.1:8080/v1"); err != nil {
		t.Fatalf("explicit private Mock URL rejected: %v", err)
	}
	if _, err := validateProviderOutboundURL("http://127.0.0.2:8080/v1"); err == nil {
		t.Fatal("unlisted private HTTP URL was accepted")
	}
}

func TestProviderOutboundRedirectRejectsHTTPSDowngrade(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "false")
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "")
	request, err := http.NewRequest(http.MethodGet, "http://8.8.8.8/media.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := providerOutboundRedirectPolicy(request, []*http.Request{{}}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("HTTP redirect error = %v, want HTTPS rejection", err)
	}
}

func TestProviderOutboundRedirectRetainsFiveHopLimit(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://8.8.8.8/media.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := make([]*http.Request, maxProviderOutboundRedirects)
	if err := providerOutboundRedirectPolicy(request, via); err == nil || !strings.Contains(err.Error(), "重定向次数过多") {
		t.Fatalf("redirect limit error = %v, want redirect limit rejection", err)
	}
}
