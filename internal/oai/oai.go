// Package oai defines the OpenAI-compatible error envelope used by every
// proxy-generated error response so standard OpenAI SDKs can decode them.
package oai

import (
	"encoding/json"
	"net/http"
)

// Error is the OpenAI error object.
type Error struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    string `json:"code"`
}

type envelope struct {
	Error Error `json:"error"`
}

// WriteError writes status and an OpenAI-shaped JSON error body.
func WriteError(w http.ResponseWriter, status int, typ, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Error: Error{
		Message: message,
		Type:    typ,
		Param:   nil,
		Code:    code,
	}})
}
