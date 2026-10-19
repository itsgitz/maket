// Package jsonapi writes maket's JSON API responses.
package jsonapi

import (
	"encoding/json"
	"net/http"
)

// Write writes v as a JSON response with the given status.
func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Error writes the API error shape: {"error": {"code", "message"}}.
func Error(w http.ResponseWriter, status int, code, message string) {
	type body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	Write(w, status, map[string]body{"error": {Code: code, Message: message}})
}
