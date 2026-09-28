package middleware

import (
	"encoding/json"
	"net/http"

	errors "github.com/atharva-ng/crunch/internal/errors"
)

type jsonResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	// Code is the machine-readable error discriminator (Error.ErrCode).
	// omitempty keeps legacy error envelopes byte-identical.
	Code string `json:"code,omitempty"`
}

func SendJSONResponse(w http.ResponseWriter, r *http.Request, code int, body interface{}) {
	w.Header().Set(HeaderContentType, contentTypeJSON)
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(jsonResponse{
		Success: true,
		Data:    body,
	})
}

func SendJSONError(w http.ResponseWriter, r *http.Request, Error *errors.Error) {
	w.Header().Set(HeaderContentType, contentTypeJSON)
	w.WriteHeader(Error.Code)
	json.NewEncoder(w).Encode(jsonResponse{
		Success: false,
		Error:   Error.Message,
		Code:    Error.ErrCode,
		Data:    Error.Data,
	})
}
