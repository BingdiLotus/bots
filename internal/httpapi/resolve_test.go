package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestResolveDeliveryRequiresAdminAndValidState(t *testing.T) {
	_, s, mux := newTestApp(t)
	path := "/api/tenants/tenant/bots/bot/deliveries/delivery/resolve"

	response := appRequest(mux, http.MethodPost, path, `{"state":"confirmed","provider_message_id":"123.456"}`, "wrong", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized resolve status = %d", response.Code)
	}
	response = appRequest(mux, http.MethodPost, path, `{"state":"pending"}`, "admin-only", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid state status = %d", response.Code)
	}
	response = appRequest(mux, http.MethodPost, path, `{"state":"confirmed","provider_message_id":"123.456","note":"checked in Slack"}`, "admin-only", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"confirmed"`) {
		t.Fatalf("confirmed resolve = %d %s", response.Code, response.Body.String())
	}
	if len(s.resolved) != 1 || s.resolved[0] != "delivery|confirmed|123.456|checked in Slack" {
		t.Fatalf("resolve was not passed to the store: %+v", s.resolved)
	}
}
