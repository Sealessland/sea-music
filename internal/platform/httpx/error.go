package httpx

import (
	"encoding/json"
	"net/http"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	Error     Error  `json:"error"`
	RequestID string `json:"request_id"`
}

func WriteError(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	WriteJSON(writer, status, ErrorResponse{
		Error:     Error{Code: code, Message: message},
		RequestID: RequestID(request.Context()),
	})
}

func WriteJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
