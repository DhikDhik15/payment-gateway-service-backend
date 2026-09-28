package middleware_test

// auth_legacy_credential_test.go — Phase 8D.3 tests for the legacy plaintext
// credential gate inside Auth:
//
//   - LEGACY_API_CREDENTIALS_ENABLED=false rejects every bare legacy key with
//     401 LEGACY_CREDENTIALS_NOT_ENABLED BEFORE any credential lookup, while
//     compound Phase 5C credentials keep working;
//   - the merchant's LegacyCredentialState is the second gate: LEGACY and
//     MIGRATED authenticate, LEGACY_DISABLED and unknown values fail closed;
//   - a state rejection is byte-identical to an unknown key (no validity
//     oracle), including when the merchant is also non-ACTIVE.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/google/uuid"
)

// ─── flag closed (LEGACY_API_CREDENTIALS_ENABLED=false) ──────────────────────

// TestAuth_Legacy_FlagOff_RejectsBeforeLookup is the core fail-safe test: with
// the window closed, a bare legacy key never reaches the database, so the
// response cannot confirm whether the credential exists.
func TestAuth_Legacy_FlagOff_RejectsBeforeLookup(t *testing.T) {
	merchant := legacyMerchant("pk_knownlegacystatusvalue", model.LegacyCredentialStateLegacy)
	merchantSvc := &stubMerchantService{merchant: merchant}
	r := newAuthTestRouter(t, merchantSvc, noopAPIKeySvc(), false)

	w := authRequest(r, "pk_knownlegacystatusvalue")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	assertErrorCode(t, w, response.CodeLegacyCredentialsNotEnabled)
	if merchantSvc.lookups != 0 {
		t.Errorf("credential lookup must not run when the window is closed (lookups=%d)", merchantSvc.lookups)
	}
}

// TestAuth_Legacy_FlagOff_NoValidityOracle proves the closed-window response
// is identical for a credential that WOULD be valid and one that never existed
// — no status or body difference leaks existence.
func TestAuth_Legacy_FlagOff_NoValidityOracle(t *testing.T) {
	merchant := legacyMerchant("pk_realkeyvalue000000000000", model.LegacyCredentialStateLegacy)
	merchantSvc := &stubMerchantService{merchant: merchant}
	r := newAuthTestRouter(t, merchantSvc, noopAPIKeySvc(), false)

	wouldBeValid := authRequest(r, "pk_realkeyvalue000000000000")
	unknown := authRequest(r, "pk_neverexistedvalue00000000")

	if wouldBeValid.Code != unknown.Code {
		t.Fatalf("status oracle: valid-shaped=%d unknown=%d", wouldBeValid.Code, unknown.Code)
	}
	if comparableBody(t, wouldBeValid) != comparableBody(t, unknown) {
		t.Errorf("body oracle:\n valid-shaped: %s\n unknown:      %s",
			comparableBody(t, wouldBeValid), comparableBody(t, unknown))
	}
	assertErrorCode(t, wouldBeValid, response.CodeLegacyCredentialsNotEnabled)
	if merchantSvc.lookups != 0 {
		t.Errorf("lookups must stay 0 with the window closed (got %d)", merchantSvc.lookups)
	}
}

// TestAuth_Legacy_FlagOff_CompoundKeyStillWorks guards against over-blocking:
// the flag must never affect Phase 5C compound credentials.
func TestAuth_Legacy_FlagOff_CompoundKeyStillWorks(t *testing.T) {
	merchant := &model.Merchant{
		ID: uuid.New(), Name: "Compound", Code: "CMP",
		Status:                model.MerchantStatusActive,
		LegacyCredentialState: model.LegacyCredentialStateLegacyDisabled,
	}
	apiKeySvc := &stubAPIKeyService{merchant: merchant}
	r := newAuthTestRouter(t, &stubMerchantService{}, apiKeySvc, false)

	w := authRequest(r, "pk_abc123:sk_secretvalue")
	if w.Code != http.StatusOK {
		t.Fatalf("compound key must work regardless of the legacy flag, got %d\nbody: %s", w.Code, w.Body)
	}
	if apiKeySvc.callCount == 0 {
		t.Error("compound credential must be verified by the Phase 5C service")
	}
}

// ─── flag open, state gate ───────────────────────────────────────────────────

// TestAuth_Legacy_StateLegacyDisabled_UniformInvalidAPIKey is the anti-oracle
// test: a disabled legacy key must be indistinguishable from an unknown key.
func TestAuth_Legacy_StateLegacyDisabled_UniformInvalidAPIKey(t *testing.T) {
	disabled := legacyMerchant("pk_disabledkeyvalue000000000", model.LegacyCredentialStateLegacyDisabled)
	svcDisabled := &stubMerchantService{merchant: disabled}
	rDisabled := newAuthTestRouter(t, svcDisabled, noopAPIKeySvc(), true)
	wDisabled := authRequest(rDisabled, "pk_disabledkeyvalue000000000")

	svcUnknown := &stubMerchantService{} // no match → ErrMerchantNotFound
	rUnknown := newAuthTestRouter(t, svcUnknown, noopAPIKeySvc(), true)
	wUnknown := authRequest(rUnknown, "pk_totallyunknownkey0000000")

	if wDisabled.Code != http.StatusUnauthorized || wUnknown.Code != http.StatusUnauthorized {
		t.Fatalf("want 401/401, got %d/%d", wDisabled.Code, wUnknown.Code)
	}
	if comparableBody(t, wDisabled) != comparableBody(t, wUnknown) {
		t.Errorf("disabled key must be indistinguishable from an unknown key:\n disabled: %s\n unknown:  %s",
			comparableBody(t, wDisabled), comparableBody(t, wUnknown))
	}
	assertErrorCode(t, wDisabled, response.CodeInvalidAPIKey)
	// The lookup DID happen (the window is open) — only the outcome is uniform.
	if svcDisabled.lookups != 1 {
		t.Errorf("expected exactly 1 lookup, got %d", svcDisabled.lookups)
	}
}

// TestAuth_Legacy_StateCheckBeforeStatusCheck_NoStatusOracle proves ordering:
// a LEGACY_DISABLED merchant that is ALSO SUSPENDED must report INVALID_API_KEY
// (state rejection), never MERCHANT_INACTIVE, so the disabled state cannot be
// probed through the lifecycle code.
func TestAuth_Legacy_StateCheckBeforeStatusCheck_NoStatusOracle(t *testing.T) {
	m := legacyMerchant("pk_suspendedanddisabled000000", model.LegacyCredentialStateLegacyDisabled)
	m.Status = model.MerchantStatusSuspended
	r := newAuthTestRouter(t, &stubMerchantService{merchant: m}, noopAPIKeySvc(), true)

	w := authRequest(r, "pk_suspendedanddisabled000000")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	assertErrorCode(t, w, response.CodeInvalidAPIKey)

	// Contrast: a state-eligible but SUSPENDED merchant still reports the
	// lifecycle error (Phase 7 behaviour preserved while the window is open).
	m2 := legacyMerchant("pk_suspendedlegacykey0000000", model.LegacyCredentialStateLegacy)
	m2.Status = model.MerchantStatusSuspended
	r2 := newAuthTestRouter(t, &stubMerchantService{merchant: m2}, noopAPIKeySvc(), true)
	w2 := authRequest(r2, "pk_suspendedlegacykey0000000")
	assertErrorCode(t, w2, response.CodeMerchantInactive)
}

// TestAuth_Legacy_StateMigrAtedStillAuthenticates documents the deliberate
// window: MIGRATED means the Phase 5C key exists, but the legacy key keeps
// working until it is explicitly disabled.
func TestAuth_Legacy_StateMigratedStillAuthenticates(t *testing.T) {
	merchant := legacyMerchant("pk_migratedbutstilllive0000", model.LegacyCredentialStateMigrated)
	r := newAuthTestRouter(t, &stubMerchantService{merchant: merchant}, noopAPIKeySvc(), true)

	w := authRequest(r, "pk_migratedbutstilllive0000")
	if w.Code != http.StatusOK {
		t.Fatalf("MIGRATED must still allow legacy auth until disable, got %d\nbody: %s", w.Code, w.Body)
	}
	if getMerchantID(t, w) != merchant.ID.String() {
		t.Error("wrong merchant resolved")
	}
}

// TestAuth_Legacy_UnknownStateFailsClosed covers the empty/unexpected state
// (e.g. a row written outside the state machine): it must never authenticate.
func TestAuth_Legacy_UnknownStateFailsClosed(t *testing.T) {
	for _, state := range []model.LegacyCredentialState{"", "SOMETHING_ELSE", "legacy", "MIGRATED "} {
		t.Run("state="+string(state), func(t *testing.T) {
			merchant := legacyMerchant("pk_unknownstatekey000000000", state)
			r := newAuthTestRouter(t, &stubMerchantService{merchant: merchant}, noopAPIKeySvc(), true)

			w := authRequest(r, "pk_unknownstatekey000000000")
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("state %q: expected 401 (fail closed), got %d", state, w.Code)
			}
			assertErrorCode(t, w, response.CodeInvalidAPIKey)
		})
	}
}

// ─── leakage ─────────────────────────────────────────────────────────────────

// TestAuth_Legacy_ErrorBodiesNeverEchoCredentials sweeps every rejection path
// and asserts no presented credential ever appears in a response body.
func TestAuth_Legacy_ErrorBodiesNeverEchoCredentials(t *testing.T) {
	secrets := []string{
		"pk_echoedsecret00000000000000",
		"pk_echoedcompound000000000000:sk_echoedsecret000000000000000000",
	}

	cases := []struct {
		name         string
		legacyOn     bool
		merchant     *model.Merchant
		apiKeySvc    service.MerchantAPIKeyService
		lookupCounts bool
	}{
		{
			name:     "flag closed",
			legacyOn: false,
			merchant: legacyMerchant(secrets[0], model.LegacyCredentialStateLegacy),
		},
		{
			name:     "state disabled",
			legacyOn: true,
			merchant: legacyMerchant(secrets[0], model.LegacyCredentialStateLegacyDisabled),
		},
		{
			name:     "unknown key",
			legacyOn: true,
			merchant: nil,
		},
		{
			name:     "compound rejected by 5c service",
			legacyOn: true,
			merchant: nil,
			apiKeySvc: &stubAPIKeyService{
				authErr: service.ErrAPIKeyNotFound,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keySvc := tc.apiKeySvc
			if keySvc == nil {
				keySvc = noopAPIKeySvc()
			}
			r := newAuthTestRouter(t, &stubMerchantService{merchant: tc.merchant}, keySvc, tc.legacyOn)

			w := authRequest(r, secrets[0])
			if w.Code == http.StatusOK {
				t.Fatal("rejection case unexpectedly succeeded")
			}
			body := w.Body.String()
			for _, s := range secrets {
				for _, part := range strings.Split(s, ":") {
					if part != "" && strings.Contains(body, part) {
						t.Errorf("response leaks credential material %q: %s", part, body)
					}
				}
			}
		})
	}
}
