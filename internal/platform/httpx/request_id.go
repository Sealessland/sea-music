package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// WithRequestID propagates a trimmed incoming X-Request-ID of at most 128 characters, or generates one when absent or invalid, then exposes it on the response and in the request context before calling next.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := strings.TrimSpace(request.Header.Get(RequestIDHeader))
		if requestID == "" || len(requestID) > 128 {
			requestID = newRequestID()
		}
		writer.Header().Set(RequestIDHeader, requestID)
		ctx := context.WithValue(request.Context(), requestIDKey{}, requestID)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// RequestID returns the request ID stored in ctx by WithRequestID, or an empty string if none is present.
func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

// newRequestID returns a 32-character lowercase hexadecimal ID from 16 cryptographically random bytes and panics if secure randomness is unavailable.
func newRequestID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(data[:])
}
