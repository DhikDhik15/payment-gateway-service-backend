// Package middleware provides Gin middleware for the payment gateway.
package middleware

import (
	"fmt"
	"math/rand/v2"

	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

const (
	// HeaderRequestID is the canonical HTTP header for request tracing.
	HeaderRequestID = "X-Request-ID"

	// requestIDPrefix makes generated IDs visually distinct from client-supplied ones.
	requestIDPrefix = "req_"

	// idCharset is the alphabet used when generating a request ID.
	idCharset = "abcdefghijklmnopqrstuvwxyz0123456789"

	// idSuffixLen is the number of random characters appended to the prefix.
	idSuffixLen = 16
)

// RequestID is a Gin middleware that:
//  1. Reads X-Request-ID from the incoming request header.
//  2. Generates a new ID (req_<16 random chars>) when none is provided.
//  3. Stores the ID in the Gin context under response.ContextKey so that
//     response helpers can embed it in every JSON envelope.
//  4. Echoes the final ID back to the caller via the X-Request-ID response header.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if id == "" {
			id = generateID()
		}

		// Make it available to downstream handlers and response helpers.
		c.Set(response.ContextKey, id)

		// Return it in the response header so clients can correlate requests.
		c.Header(HeaderRequestID, id)

		c.Next()
	}
}

// generateID returns a new request ID of the form "req_<16 random chars>".
// Uses math/rand/v2 (Go 1.22+) which is seeded automatically — no setup needed.
func generateID() string {
	b := make([]byte, idSuffixLen)
	for i := range b {
		b[i] = idCharset[rand.IntN(len(idCharset))]
	}
	return fmt.Sprintf("%s%s", requestIDPrefix, string(b))
}
