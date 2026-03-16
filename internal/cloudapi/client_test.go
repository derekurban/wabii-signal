package cloudapi

import (
	"encoding/json"
	"testing"
)

func TestTokenUnmarshalSupportsCurrentAndLegacyKeyFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload string
		wantKey string
	}{
		{
			name:    "current token field",
			payload: `{"id":"tok-1","accessPolicyId":"ap-1","name":"writer","displayName":"Writer","token":"secret-token"}`,
			wantKey: "secret-token",
		},
		{
			name:    "legacy key field",
			payload: `{"id":"tok-1","accessPolicyId":"ap-1","name":"writer","displayName":"Writer","key":"legacy-secret"}`,
			wantKey: "legacy-secret",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var token Token
			if err := json.Unmarshal([]byte(tc.payload), &token); err != nil {
				t.Fatalf("unmarshal token: %v", err)
			}
			if token.Key != tc.wantKey {
				t.Fatalf("expected key %q, got %q", tc.wantKey, token.Key)
			}
		})
	}
}

func TestListAccessPoliciesWrappedShape(t *testing.T) {
	t.Parallel()

	var wrapped listResponse[AccessPolicy]
	payload := `{"items":[{"id":"ap-1","name":"policy-1","displayName":"Policy 1","scopes":["logs:write"],"realms":[{"type":"stack","identifier":"123"}]}]}`
	if err := json.Unmarshal([]byte(payload), &wrapped); err != nil {
		t.Fatalf("unmarshal wrapped list: %v", err)
	}
	if len(wrapped.Items) != 1 {
		t.Fatalf("expected 1 access policy, got %d", len(wrapped.Items))
	}
	if wrapped.Items[0].ID != "ap-1" {
		t.Fatalf("expected access policy id ap-1, got %q", wrapped.Items[0].ID)
	}
}
