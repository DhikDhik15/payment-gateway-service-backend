package model

// Development-only account recovery (no SMTP delivery).
//
// The recovery endpoint identifies the intended existing account explicitly
// by email and sets a new password after an independent development-only
// authorization check (X-Dev-Recovery-Key header). The key itself is never
// part of the JSON body, never logged, and never returned.
//
// Password policy mirrors the rest of the application: 8–128 characters.
type DevRecoveryRequest struct {
	Email           string `json:"email"            binding:"required,max=254"`
	NewPassword     string `json:"new_password"     binding:"required,min=8,max=128"`
	ConfirmPassword string `json:"confirm_password" binding:"required,min=8,max=128"`
}

// DevRecoveryResponse confirms the password update without establishing a
// session and without returning hashes or secrets. The caller must sign in
// with the new password afterwards.
type DevRecoveryResponse struct {
	Message string `json:"message"`
	Email   string `json:"email"`
}

// DevRecoveryStatusResponse is the safe capability signal for the dashboard:
// whether development recovery is enabled. It never carries secrets.
type DevRecoveryStatusResponse struct {
	Enabled bool `json:"enabled"`
}
