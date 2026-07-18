package appapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

func init() {
	gin.EnableJsonDecoderDisallowUnknownFields()
}

func bindJSON(context *gin.Context, target any) error {
	request := context.Request
	request.Body = http.MaxBytesReader(context.Writer, request.Body, 1<<20)
	if err := context.ShouldBindBodyWith(target, binding.JSON); err != nil {
		return errors.New("request body must be valid JSON")
	}
	body, ok := context.Get(gin.BodyBytesKey)
	if !ok {
		return errors.New("request body must be valid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body.([]byte)))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return errors.New("request body must be valid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}
