package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestValidateMetadataRejectsUnsafeShapesAndKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want error
	}{
		{name: "array", raw: `["old_status"]`, want: ErrAuditMetadataNotObject},
		{name: "scalar", raw: `"ACTIVE"`, want: ErrAuditMetadataNotObject},
		{name: "null", raw: `null`, want: ErrAuditMetadataNotObject},
		{name: "trailing", raw: `{} {}`, want: ErrInvalidAuditEvent},
		{name: "password", raw: `{"password":"TEST_PASSWORD_SECRET_123"}`, want: ErrUnsafeAuditMetadata},
		{name: "nested token", raw: `{"context":{"refresh_token":"TEST_REFRESH_SECRET_789"}}`, want: ErrUnsafeAuditMetadata},
		{name: "authorization", raw: `{"Authorization":"Bearer nope"}`, want: ErrUnsafeAuditMetadata},
		{name: "cookie", raw: `{"cookie":"session=TEST_COOKIE_SECRET_ABC"}`, want: ErrUnsafeAuditMetadata},
		{name: "body", raw: `{"raw_request_body":"TEST_PASSWORD_SECRET_123"}`, want: ErrUnsafeAuditMetadata},
		{name: "secret value", raw: `{"note":"whsec_TEST_WEBHOOK_SECRET_DEF"}`, want: ErrUnsafeAuditMetadata},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMetadata(json.RawMessage(tt.raw))
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateMetadata() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestMarshalMetadataBoundsAndAcceptsSafeObject(t *testing.T) {
	t.Parallel()

	raw, err := MarshalMetadata(map[string]any{
		"old_status":      "ACTIVE",
		"new_status":      "SUSPENDED",
		"credential_type": "API_KEY",
	})
	if err != nil {
		t.Fatalf("MarshalMetadata() error = %v", err)
	}
	if !json.Valid(raw) || string(raw) == "" {
		t.Fatalf("MarshalMetadata() returned invalid JSON: %q", raw)
	}

	_, err = MarshalMetadata(map[string]any{"blob": strings.Repeat("x", MaxMetadataBytes)})
	if !errors.Is(err, ErrAuditMetadataTooLarge) {
		t.Fatalf("oversized metadata error = %v, want %v", err, ErrAuditMetadataTooLarge)
	}
}

func TestValidateEventRejectsUnknownAction(t *testing.T) {
	t.Parallel()
	err := ValidateEvent(Event{
		ActorType: ActorTypeSystem,
		Action:    Action("FUTURE_UNREVIEWED_ACTION"),
		Metadata:  json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrInvalidAuditEvent) {
		t.Fatalf("unknown action error = %v, want %v", err, ErrInvalidAuditEvent)
	}
}

func TestNewEventFromContextCapturesSafeRequestAndActor(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	merchantID := uuid.New()
	targetID := uuid.New()
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{
		RequestID: "req_test_123",
		IP:        "203.0.113.10",
	})
	ctx = WithActor(ctx, Actor{
		Type:       ActorTypeDashboardUser,
		UserID:     &userID,
		MerchantID: &merchantID,
	})
	event, err := NewEventFromContext(ctx, ActionUserRoleChanged, TargetUser, &targetID, nil, map[string]any{
		"old_role": "VIEWER",
		"new_role": "ADMIN",
	})
	if err != nil {
		t.Fatalf("NewEventFromContext() error = %v", err)
	}
	if event.MerchantID == nil || *event.MerchantID != merchantID {
		t.Fatalf("merchant ID = %v, want %s", event.MerchantID, merchantID)
	}
	if event.ActorUserID == nil || *event.ActorUserID != userID {
		t.Fatalf("actor user ID = %v, want %s", event.ActorUserID, userID)
	}
	if event.RequestID == nil || *event.RequestID != "req_test_123" {
		t.Fatalf("request ID = %v", event.RequestID)
	}
	if event.IP == nil || *event.IP != "203.0.113.10" {
		t.Fatalf("IP = %v", event.IP)
	}
	if err := ValidateEvent(event); err != nil {
		t.Fatalf("ValidateEvent() error = %v", err)
	}
}

func TestWithRequestMetadataSanitizesUntrustedCorrelationValues(t *testing.T) {
	t.Parallel()

	ctx := WithRequestMetadata(context.Background(), RequestMetadata{
		RequestID: strings.Repeat("x", MaxMetadataBytes) + "\n",
		IP:        "not-an-ip",
	})
	metadata := RequestMetadataFromContext(ctx)
	if len(metadata.RequestID) > 128 {
		t.Fatalf("request ID length = %d, want <= 128", len(metadata.RequestID))
	}
	if strings.ContainsAny(metadata.RequestID, "\r\n") {
		t.Fatalf("request ID contains control characters: %q", metadata.RequestID)
	}
	if metadata.IP != "" {
		t.Fatalf("invalid IP was retained: %q", metadata.IP)
	}
	secretCtx := WithRequestMetadata(context.Background(), RequestMetadata{
		RequestID: "req_TEST_REFRESH_SECRET_789",
	})
	if got := RequestMetadataFromContext(secretCtx).RequestID; got != "" {
		t.Fatalf("secret-like request ID was retained: %q", got)
	}
	if got := NormalizeRequestID("req_VALID-123"); got != "req_VALID-123" {
		t.Fatalf("safe request ID normalized to %q", got)
	}
}

func TestRequireRecorderFailsClosedForAttachedEvent(t *testing.T) {
	t.Parallel()
	ctx := WithEvent(context.Background(), Event{
		ActorType: ActorTypeSystem,
		Action:    ActionPasswordChanged,
		Metadata:  json.RawMessage(`{}`),
	})
	if err := RequireRecorder(ctx, nil); !errors.Is(err, ErrAuditRecorderUnavailable) {
		t.Fatalf("RequireRecorder() error = %v, want %v", err, ErrAuditRecorderUnavailable)
	}
}

func TestRecordInTxIfPresentWithoutEventIsNoop(t *testing.T) {
	t.Parallel()

	// A nil transaction is safe when no event is attached because the helper
	// must not touch it. This is the path used by legacy/unit-test doubles.
	if err := RecordInTxIfPresent(context.Background(), nil, nil); err != nil {
		t.Fatalf("RecordInTxIfPresent() error = %v", err)
	}
}
