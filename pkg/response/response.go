// Package response provides standardised JSON response helpers for Gin handlers.
//
// Success envelope:
//
//	{ "data": <payload> }
//
// Error envelope:
//
//	{ "error": { "code": "<CODE>", "message": "<human-readable message>" } }
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ----- envelope types -------------------------------------------------------

// successEnvelope wraps any successful response payload.
type successEnvelope struct {
	Data any `json:"data"`
}

// errorDetail holds a machine-readable code and a human-readable message.
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorEnvelope wraps an error response.
type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

// ----- success helpers -------------------------------------------------------

// OK writes HTTP 200 with a JSON success envelope.
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, successEnvelope{Data: data})
}

// Created writes HTTP 201 with a JSON success envelope.
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, successEnvelope{Data: data})
}

// ----- error helpers ---------------------------------------------------------

// BadRequest writes HTTP 400 with a JSON error envelope.
func BadRequest(c *gin.Context, code, message string) {
	writeError(c, http.StatusBadRequest, code, message)
}

// Unauthorized writes HTTP 401 with a JSON error envelope.
func Unauthorized(c *gin.Context, code, message string) {
	writeError(c, http.StatusUnauthorized, code, message)
}

// Forbidden writes HTTP 403 with a JSON error envelope.
func Forbidden(c *gin.Context, code, message string) {
	writeError(c, http.StatusForbidden, code, message)
}

// NotFound writes HTTP 404 with a JSON error envelope.
func NotFound(c *gin.Context, code, message string) {
	writeError(c, http.StatusNotFound, code, message)
}

// Conflict writes HTTP 409 with a JSON error envelope.
func Conflict(c *gin.Context, code, message string) {
	writeError(c, http.StatusConflict, code, message)
}

// InternalServerError writes HTTP 500 with a JSON error envelope.
// The raw internal error is intentionally NOT forwarded to the client.
func InternalServerError(c *gin.Context) {
	writeError(c, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "An unexpected error occurred")
}

// writeError is the shared implementation for all error helpers.
func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorEnvelope{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}
