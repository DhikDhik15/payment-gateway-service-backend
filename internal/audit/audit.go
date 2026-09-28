// Package audit defines the bounded, secret-safe security audit contract.
//
// It deliberately contains no HTTP framework and no application logging
// framework. Business services construct explicit AuditEvent values; the
// repository persists them, and middleware supplies request/actor context.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	// MaxMetadataBytes is the hard upper bound for one audit metadata object.
	// It is intentionally independent from the HTTP body limit: audit records
	// must never become an unbounded request-data sink.
	MaxMetadataBytes = 64 * 1024

	maxRequestIDBytes  = 128
	maxIPBytes         = 45
	maxActionBytes     = 64
	maxTargetTypeBytes = 64
	maxActorTypeBytes  = 32
	maxMetadataDepth   = 8
	maxMetadataKeys    = 128
)

var (
	ErrInvalidAuditEvent        = errors.New("invalid audit event")
	ErrAuditMetadataTooLarge    = errors.New("audit metadata exceeds size limit")
	ErrAuditMetadataNotObject   = errors.New("audit metadata must be a JSON object")
	ErrUnsafeAuditMetadata      = errors.New("audit metadata contains a forbidden key")
	ErrAuditRecorderUnavailable = errors.New("audit recorder unavailable for mandatory event")
)

// ActorType identifies the security principal that caused an event.
type ActorType string

const (
	ActorTypeDashboardUser   ActorType = "DASHBOARD_USER"
	ActorTypeAdmin           ActorType = "ADMIN"
	ActorTypeAPIKey          ActorType = "API_KEY"
	ActorTypeSystem          ActorType = "SYSTEM"
	ActorTypeUnauthenticated ActorType = "UNAUTHENTICATED"
)

func (a ActorType) Valid() bool {
	switch a {
	case ActorTypeDashboardUser, ActorTypeAdmin, ActorTypeAPIKey, ActorTypeSystem, ActorTypeUnauthenticated:
		return true
	default:
		return false
	}
}

// Action is a stable machine-readable security event identifier.
type Action string

const (
	ActionMerchantCreated          Action = "MERCHANT_CREATED"
	ActionMerchantStatusChanged    Action = "MERCHANT_STATUS_CHANGED"
	ActionUserCreated              Action = "USER_CREATED"
	ActionUserRoleChanged          Action = "USER_ROLE_CHANGED"
	ActionUserStatusChanged        Action = "USER_STATUS_CHANGED"
	ActionPasswordChanged          Action = "PASSWORD_CHANGED"
	ActionInvitationCreated        Action = "INVITATION_CREATED"
	ActionInvitationAccepted       Action = "INVITATION_ACCEPTED"
	ActionAPIKeyCreated            Action = "API_KEY_CREATED"
	ActionAPIKeyRotated            Action = "API_KEY_ROTATED"
	ActionAPIKeyRevoked            Action = "API_KEY_REVOKED"
	ActionLegacyCredentialMigrated Action = "LEGACY_CREDENTIAL_MIGRATED"
	ActionLegacyCredentialDisabled Action = "LEGACY_CREDENTIAL_DISABLED"
	ActionWebhookConfigChanged     Action = "WEBHOOK_CONFIG_CHANGED"
	ActionAdminAuthFailed          Action = "ADMIN_AUTH_FAILED"
)

func (a Action) Valid() bool {
	switch a {
	case ActionMerchantCreated, ActionMerchantStatusChanged, ActionUserCreated,
		ActionUserRoleChanged, ActionUserStatusChanged, ActionPasswordChanged,
		ActionInvitationCreated, ActionInvitationAccepted, ActionAPIKeyCreated,
		ActionAPIKeyRotated, ActionAPIKeyRevoked, ActionLegacyCredentialMigrated,
		ActionLegacyCredentialDisabled, ActionWebhookConfigChanged, ActionAdminAuthFailed:
		return true
	default:
		return false
	}
}

// TargetType identifies the resource category affected by an event.
type TargetType string

const (
	TargetMerchant         TargetType = "MERCHANT"
	TargetUser             TargetType = "USER"
	TargetInvitation       TargetType = "INVITATION"
	TargetAPIKey           TargetType = "API_KEY"
	TargetLegacyCredential TargetType = "LEGACY_CREDENTIAL"
	TargetWebhookConfig    TargetType = "WEBHOOK_CONFIG"
	TargetAdminAuth        TargetType = "ADMIN_AUTH"
)

func (t TargetType) Valid() bool {
	switch t {
	case TargetMerchant, TargetUser, TargetInvitation, TargetAPIKey, TargetLegacyCredential, TargetWebhookConfig, TargetAdminAuth:
		return true
	default:
		return false
	}
}

// Event is the validated logical audit record passed to persistence.
// Metadata is always a JSON object after ValidateEvent.
type Event struct {
	MerchantID  *uuid.UUID
	ActorUserID *uuid.UUID
	ActorType   ActorType
	Action      Action
	TargetType  *TargetType
	TargetID    *uuid.UUID
	RequestID   *string
	IP          *string
	Metadata    json.RawMessage
}

// RequestMetadata is captured by the existing request-ID middleware and
// follows Gin's configured ClientIP/trusted-proxy policy.
type RequestMetadata struct {
	RequestID string
	IP        string
}

// Actor is the authenticated principal attached to a request context.
type Actor struct {
	Type       ActorType
	UserID     *uuid.UUID
	MerchantID *uuid.UUID
}

type contextKey uint8

const (
	requestMetadataKey contextKey = iota
	actorKey
	eventKey
)

// WithRequestMetadata stores request correlation data in the standard context.
// It never stores headers, cookies, bodies, or credentials.
func WithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestMetadataKey, RequestMetadata{
		RequestID: sanitizeRequestID(metadata.RequestID),
		IP:        sanitizeIP(metadata.IP),
	})
}

// RequestMetadataFromContext returns a copy of request correlation data.
func RequestMetadataFromContext(ctx context.Context) RequestMetadata {
	if ctx == nil {
		return RequestMetadata{}
	}
	value, _ := ctx.Value(requestMetadataKey).(RequestMetadata)
	return value
}

// WithActor stores the authenticated actor in the request context.
func WithActor(ctx context.Context, actor Actor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actorKey, Actor{
		Type:       actor.Type,
		UserID:     cloneUUID(actor.UserID),
		MerchantID: cloneUUID(actor.MerchantID),
	})
}

// ActorFromContext returns a copy of the authenticated actor, if any.
func ActorFromContext(ctx context.Context) Actor {
	if ctx == nil {
		return Actor{}
	}
	value, _ := ctx.Value(actorKey).(Actor)
	value.UserID = cloneUUID(value.UserID)
	value.MerchantID = cloneUUID(value.MerchantID)
	return value
}

// WithEvent attaches an event to a transaction-owning repository call. The
// event is consumed by the repository inside that same pgx transaction.
func WithEvent(ctx context.Context, event Event) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, eventKey, event)
}

// EventFromContext returns the transaction-coupled event, if one was attached.
func EventFromContext(ctx context.Context) (Event, bool) {
	if ctx == nil {
		return Event{}, false
	}
	value, ok := ctx.Value(eventKey).(Event)
	return value, ok
}

// NewEventFromContext constructs an event using request correlation and actor
// context. Explicit merchant/target arguments always take precedence.
func NewEventFromContext(
	ctx context.Context,
	action Action,
	targetType TargetType,
	targetID *uuid.UUID,
	merchantID *uuid.UUID,
	metadata map[string]any,
) (Event, error) {
	request := RequestMetadataFromContext(ctx)
	actor := ActorFromContext(ctx)
	if actor.Type == "" {
		actor.Type = ActorTypeUnauthenticated
	}
	if merchantID == nil {
		merchantID = cloneUUID(actor.MerchantID)
	}

	raw, err := MarshalMetadata(metadata)
	if err != nil {
		return Event{}, err
	}
	event := Event{
		MerchantID:  cloneUUID(merchantID),
		ActorUserID: cloneUUID(actor.UserID),
		ActorType:   actor.Type,
		Action:      action,
		TargetID:    cloneUUID(targetID),
		Metadata:    raw,
	}
	if targetType != "" {
		t := targetType
		event.TargetType = &t
	}
	if request.RequestID != "" {
		id := request.RequestID
		event.RequestID = &id
	}
	if request.IP != "" {
		ip := request.IP
		event.IP = &ip
	}
	return event, ValidateEvent(event)
}

// Recorder is implemented by the centralized audit service. Transactional
// repositories depend on this narrow interface rather than the service layer.
type Recorder interface {
	Record(ctx context.Context, event Event) error
	RecordInTx(ctx context.Context, tx pgx.Tx, event Event) error
}

// MarshalMetadata validates and bounds an explicitly constructed metadata map.
func MarshalMetadata(metadata map[string]any) (json.RawMessage, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal metadata: %v", ErrInvalidAuditEvent, err)
	}
	if err := ValidateMetadata(raw); err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// MergeMetadata returns a validated event with safe contextual values merged
// into its existing metadata object. It is used by a transaction owner when
// the authoritative old state is known only after locking the target row.
func MergeMetadata(event Event, values map[string]any) (Event, error) {
	metadata := make(map[string]any)
	if len(event.Metadata) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(event.Metadata)))
		decoder.UseNumber()
		if err := decoder.Decode(&metadata); err != nil {
			return Event{}, fmt.Errorf("%w: decode existing metadata: %v", ErrInvalidAuditEvent, err)
		}
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	for key, value := range values {
		metadata[key] = value
	}
	raw, err := MarshalMetadata(metadata)
	if err != nil {
		return Event{}, err
	}
	event.Metadata = raw
	return event, ValidateEvent(event)
}

// ValidateMetadata enforces the object, size, depth, key-count, and secret-key
// policy for JSONB audit metadata.
func ValidateMetadata(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > MaxMetadataBytes {
		return ErrAuditMetadataTooLarge
	}
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return ErrAuditMetadataNotObject
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: invalid JSON metadata: %v", ErrInvalidAuditEvent, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("%w: trailing JSON metadata", ErrInvalidAuditEvent)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON metadata: %v", ErrInvalidAuditEvent, err)
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return ErrAuditMetadataNotObject
	}
	return walkMetadata(object, 0, 0)
}

func walkMetadata(value map[string]any, depth, keyCount int) error {
	if depth > maxMetadataDepth {
		return fmt.Errorf("%w: metadata nesting is too deep", ErrInvalidAuditEvent)
	}
	keyCount += len(value)
	if keyCount > maxMetadataKeys {
		return fmt.Errorf("%w: metadata has too many keys", ErrInvalidAuditEvent)
	}
	for key, child := range value {
		if unsafeMetadataKey(key) {
			return fmt.Errorf("%w: %q", ErrUnsafeAuditMetadata, key)
		}
		if err := validateMetadataValue(child, depth+1); err != nil {
			return err
		}
		switch typed := child.(type) {
		case map[string]any:
			if err := walkMetadata(typed, depth+1, keyCount); err != nil {
				return err
			}
		case []any:
			if depth+1 > maxMetadataDepth {
				return fmt.Errorf("%w: metadata nesting is too deep", ErrInvalidAuditEvent)
			}
			for _, item := range typed {
				if nested, ok := item.(map[string]any); ok {
					if err := walkMetadata(nested, depth+2, keyCount); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func validateMetadataValue(value any, depth int) error {
	if depth > maxMetadataDepth {
		return fmt.Errorf("%w: metadata nesting is too deep", ErrInvalidAuditEvent)
	}
	switch typed := value.(type) {
	case map[string]any:
		return walkMetadata(typed, depth, 0)
	case []any:
		if depth+1 > maxMetadataDepth {
			return fmt.Errorf("%w: metadata nesting is too deep", ErrInvalidAuditEvent)
		}
		for _, item := range typed {
			if err := validateMetadataValue(item, depth+1); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > 4096 || containsControl(typed) {
			return fmt.Errorf("%w: unsafe metadata string value", ErrInvalidAuditEvent)
		}
		lower := strings.ToLower(strings.TrimSpace(typed))
		if strings.HasPrefix(lower, "whsec_") || strings.HasPrefix(lower, "sk_") ||
			strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "basic ") ||
			strings.Contains(lower, "password=") || strings.Contains(lower, "token=") ||
			strings.Contains(lower, "secret=") {
			return ErrUnsafeAuditMetadata
		}
	}
	return nil
}

func unsafeMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(normalized)
	if normalized == "" {
		return true
	}
	switch normalized {
	case "password", "passwordhash", "token", "accesstoken", "refreshtoken", "invitationtoken", "apikey", "apisecret", "secret", "authorization", "cookie", "body", "requestbody", "responsebody", "headers", "headerauthorization":
		return true
	}
	// Token/password/secret/authorization/cookie substrings are rejected even
	// when embedded in a compound key. Public IDs such as invitation_id and
	// credential_id do not contain these terms and remain safe.
	for _, forbidden := range []string{"password", "token", "secret", "authorization", "cookie", "body", "header", "signature", "plaintext"} {
		if strings.Contains(normalized, forbidden) {
			return true
		}
	}
	return false
}

// ValidateEvent validates all scalar and metadata fields before persistence.
func ValidateEvent(event Event) error {
	if len(event.ActorType) > maxActorTypeBytes || !event.ActorType.Valid() {
		return fmt.Errorf("%w: invalid actor type", ErrInvalidAuditEvent)
	}
	if !event.Action.Valid() || !validIdentifier(string(event.Action), maxActionBytes) {
		return fmt.Errorf("%w: invalid action", ErrInvalidAuditEvent)
	}
	if event.TargetType != nil {
		if !event.TargetType.Valid() || !validIdentifier(string(*event.TargetType), maxTargetTypeBytes) {
			return fmt.Errorf("%w: invalid target type", ErrInvalidAuditEvent)
		}
	}
	if event.RequestID != nil {
		if len(*event.RequestID) > maxRequestIDBytes || containsControl(*event.RequestID) {
			return fmt.Errorf("%w: invalid request ID", ErrInvalidAuditEvent)
		}
	}
	if event.IP != nil {
		if len(*event.IP) > maxIPBytes || net.ParseIP(*event.IP) == nil {
			return fmt.Errorf("%w: invalid IP address", ErrInvalidAuditEvent)
		}
	}
	return ValidateMetadata(event.Metadata)
}

func validIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	for _, r := range value {
		if !(r == '_' || r == '-' || r == '.' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// NormalizeRequestID applies the bounded, printable correlation policy used by
// audit request metadata. It is exported for the existing Gin request-ID
// middleware so logs and responses cannot echo credential-shaped client input.
func NormalizeRequestID(value string) string {
	return sanitizeRequestID(value)
}

func sanitizeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"password", "secret", "token", "authorization", "bearer", "cookie", "refresh", "access"} {
		if strings.Contains(lower, forbidden) {
			return ""
		}
	}
	if len(value) > maxRequestIDBytes {
		value = value[:maxRequestIDBytes]
	}
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			continue
		}
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("_-.::", r)) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sanitizeIP(value string) string {
	value = strings.TrimSpace(value)
	if net.ParseIP(value) == nil {
		return ""
	}
	return value
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

// UUIDPtr returns a pointer to a UUID value for event construction.
func UUIDPtr(value uuid.UUID) *uuid.UUID { return &value }

// StringPtr returns a pointer to a string value for event construction.
func StringPtr(value string) *string { return &value }

// RequireRecorder verifies that a context-attached mandatory event cannot be
// silently dropped because a repository was constructed without its recorder.
func RequireRecorder(ctx context.Context, recorder Recorder) error {
	if _, ok := EventFromContext(ctx); ok && recorder == nil {
		return ErrAuditRecorderUnavailable
	}
	return nil
}

// RecordEventInTx validates and records an explicitly supplied event inside an
// existing transaction. It is useful when a repository learns the canonical
// target ID only after an INSERT ... ON CONFLICT RETURNING.
func RecordEventInTx(ctx context.Context, tx pgx.Tx, recorder Recorder, event Event) error {
	if tx == nil {
		return ErrInvalidAuditEvent
	}
	if recorder == nil {
		return ErrAuditRecorderUnavailable
	}
	if err := ValidateEvent(event); err != nil {
		return err
	}
	return recorder.RecordInTx(ctx, tx, event)
}

// RecordInTxIfPresent records a context-attached event inside an existing
// transaction. An absent event is a no-op for unit-test doubles and
// system-only paths; an attached event with a nil recorder fails closed.
func RecordInTxIfPresent(ctx context.Context, tx pgx.Tx, recorder Recorder) error {
	event, ok := EventFromContext(ctx)
	if !ok {
		return nil
	}
	return RecordEventInTx(ctx, tx, recorder, event)
}
