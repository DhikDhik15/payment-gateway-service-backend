package handler

// binding_test.go — Phase 8D.2: http.MaxBytesError from a body that trips the
// global limit mid-bind must map to a stable, non-technical message through
// the shared parseBindingErrors path (the 400 VALIDATION_ERROR envelope).

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestParseBindingErrors_MaxBytesError(t *testing.T) {
	details := parseBindingErrors(&http.MaxBytesError{Limit: 1048576})
	msg, ok := details["_"]
	if !ok {
		t.Fatalf("no _ entry for MaxBytesError: %#v", details)
	}
	if msg != "Request body exceeds the configured size limit" {
		t.Fatalf("message = %q, want the stable size-limit message", msg)
	}
}

func TestParseBindingErrors_ValidationErrorsStillMap(t *testing.T) {
	// Guards the reordering: the MaxBytesError branch must not shadow
	// ordinary validator errors.
	err := validator.ValidationErrors{}
	details := parseBindingErrors(err)
	if _, exists := details["_"]; exists {
		t.Fatalf("validator errors should not fall into the generic branch: %#v", details)
	}
}

func TestParseBindingErrors_UnknownErrorUsesGenericEntry(t *testing.T) {
	details := parseBindingErrors(errFake{})
	if !strings.Contains(details["_"], "boom") {
		t.Fatalf("generic entry = %q, want underlying error text", details["_"])
	}
}

type errFake struct{}

func (errFake) Error() string { return "boom" }
