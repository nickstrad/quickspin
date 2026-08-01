package api

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse is wire format, not a Go error: the server writes it and every
// future client (SDKs, the worker client) decodes it, which is why it lives in
// a package owned by neither. See docs/plans/open/05-control-plane-api.mdx.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Codes are the stable half of the envelope: SDKs branch on them, so they may
// never change once published, while Message stays free to improve.
const (
	CodeInvalidRequest = "invalid_request"
	CodeNotFound       = "not_found"
	CodeConflict       = "conflict"
	CodeUnprocessable  = "unprocessable"
	CodeTooLarge       = "too_large"
	CodeTimeout        = "timeout"
	CodeInternal       = "internal"
)

// WriteJSON writes status and, unless the status forbids a body, v as JSON.
//
// It returns the encode error rather than logging it: this package is shared
// wire format with no identity of its own, and the handler is the one place
// that knows which request failed. The header is already flushed by then, so
// the caller can only log — the client sees a truncated body either way.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	if status == http.StatusNoContent || v == nil {
		w.WriteHeader(status)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

// WriteError writes the error envelope. An empty code is filled in from status
// so the two can never disagree; pass a specific code (say "sandbox_not_found")
// when the handler knows more than the status does.
func WriteError(w http.ResponseWriter, status int, code, message string) error {
	if code == "" {
		code = CodeForStatus(status)
	}
	return WriteJSON(w, status, ErrorResponse{Error: ErrorBody{Code: code, Message: message}})
}

func CodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return CodeInvalidRequest
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeConflict
	case http.StatusUnprocessableEntity:
		return CodeUnprocessable
	case http.StatusRequestEntityTooLarge:
		return CodeTooLarge
	case http.StatusGatewayTimeout:
		return CodeTimeout
	default:
		return CodeInternal
	}
}
