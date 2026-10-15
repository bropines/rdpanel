package httpx

import (
	"encoding/json"
	"net/http"
)

// ErrorBody is the panel API error envelope: {"error":{"code":"...","message":"..."}}.
// (The RustDesk client API under /api uses its own legacy shapes.)
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine-readable code and a human message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes v as JSON with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the standard error envelope.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// NotFound is a JSON 404 handler for API sub-routers.
func NotFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotFound, "not_found", "not found")
}

// MethodNotAllowed is a JSON 405 handler for API sub-routers.
func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}
