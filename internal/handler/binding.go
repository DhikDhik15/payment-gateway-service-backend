package handler

import (
	"errors"
	"strings"

	"github.com/go-playground/validator/v10"
)

// parseBindingErrors converts validator.ValidationErrors (from ShouldBindJSON)
// into a map[string]string suitable for response.ValidationError's details field.
//
// Each key is the JSON field name (lowercase), each value is a human-readable
// description of why the field failed.
func parseBindingErrors(err error) map[string]string {
	details := make(map[string]string)

	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		// Not a validation error — return a generic entry.
		details["_"] = err.Error()
		return details
	}

	for _, fe := range ve {
		field := strings.ToLower(fe.Field())
		details[field] = validationMessage(fe)
	}
	return details
}

// validationMessage returns a human-readable message for a single field error.
func validationMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "This field is required"
	case "min":
		return "Value is too short (minimum " + fe.Param() + " characters)"
	case "max":
		return "Value is too long (maximum " + fe.Param() + " characters)"
	case "len":
		return "Value must be exactly " + fe.Param() + " characters"
	case "email":
		return "Must be a valid email address"
	case "uuid":
		return "Must be a valid UUID"
	case "oneof":
		return "Must be one of: " + strings.ReplaceAll(fe.Param(), " ", ", ")
	case "gt":
		return "Value must be greater than " + fe.Param()
	case "gte":
		return "Value must be greater than or equal to " + fe.Param()
	case "lt":
		return "Value must be less than " + fe.Param()
	case "lte":
		return "Value must be less than or equal to " + fe.Param()
	default:
		return "Invalid value"
	}
}
