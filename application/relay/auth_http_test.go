package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleRegisterSignupDisabled(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(`{
		"email":"demo@example.com",
		"password":"password123",
		"name":"Demo User"
	}`))
	rr := httptest.NewRecorder()

	handleRegister(rr, req, nil, &Config{SignupEnabled: false})

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}

	var resp AuthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected success=false when signup is disabled")
	}
}
