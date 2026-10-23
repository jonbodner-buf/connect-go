// Copyright 2021-2026 The Connect Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package connecthttp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/internal/assert"
	pingv1 "connectrpc.com/connect/v2/internal/gen/connect/ping/v1"
	"connectrpc.com/connect/v2/internal/gen/connect/ping/v1/pingv1connect"
	"connectrpc.com/connect/v2/internal/memhttp/memhttptest"
)

const (
	rewriteRequestContentTypeHeader  = "Test-Rewrite-Request-Content-Type"
	rewriteResponseContentTypeHeader = "Test-Rewrite-Response-Content-Type"
)

type contentTypeVariant struct {
	name    string
	rewrite func(contentType string) string
	// rejectsJSON is true if the rewritten Content-Type is invalid for JSON
	// codecs. It's valid for every other codec.
	rejectsJSON bool
	// rejectsAll is true if the rewritten Content-Type is invalid for every
	// codec.
	rejectsAll bool
}

func newContentTypeVariants() []contentTypeVariant {
	return []contentTypeVariant{
		{
			name:    "uppercase",
			rewrite: strings.ToUpper,
		},
		{
			name:    "extra_parameter",
			rewrite: func(contentType string) string { return contentType + "; foo=bar" },
		},
		{
			name:    "charset_utf8_no_space",
			rewrite: func(contentType string) string { return contentType + ";Charset=UTF-8" },
		},
		{
			name:        "charset_latin1",
			rewrite:     func(contentType string) string { return contentType + "; charset=latin1" },
			rejectsJSON: true,
		},
		{
			name:        "charset_empty_quoted",
			rewrite:     func(contentType string) string { return contentType + `; charset=""` },
			rejectsJSON: true,
		},
		{
			name:       "charset_no_value",
			rewrite:    func(contentType string) string { return contentType + "; charset=" },
			rejectsAll: true,
		},
	}
}

func TestContentTypeParameters(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	srv := connect.NewServer()
	pingv1connect.RegisterPingServiceHandler(srv, pingServer{})
	connecthttp.Mount(mux, srv)
	contentTypeVariants := newContentTypeVariants()
	server := memhttptest.NewServer(t, rewriteContentTypeMiddleware(mux, contentTypeVariants))

	protocols := []struct {
		name    string
		options []connecthttp.Option
	}{
		{name: connect.ProtocolNameConnect},
		{name: connect.ProtocolNameGRPC, options: []connecthttp.Option{connecthttp.WithGRPC()}},
		{name: connect.ProtocolNameGRPCWeb, options: []connecthttp.Option{connecthttp.WithGRPCWeb()}},
	}
	codecs := []struct {
		name    string
		options []connecthttp.Option
	}{
		{name: connect.CodecNameProto},
		{name: connect.CodecNameJSON, options: []connecthttp.Option{connecthttp.WithProtoJSON()}},
	}
	for _, protocol := range protocols {
		for _, codec := range codecs {
			options := append(append([]connecthttp.Option{}, protocol.options...), codec.options...)
			client := pingv1connect.NewPingServiceClient(connect.NewClient(
				connecthttp.NewTransport(server.Client(), server.URL(), options...),
			))
			for _, variant := range contentTypeVariants {
				rejected := variant.rejectsAll || (variant.rejectsJSON && codec.name == connect.CodecNameJSON)
				prefix := protocol.name + "_" + codec.name + "_" + variant.name
				t.Run(prefix+"/server_unary", func(t *testing.T) {
					t.Parallel()
					ctx := rewriteContentTypeContext(t, rewriteRequestContentTypeHeader, variant.name)
					response, err := client.Ping(ctx, &pingv1.PingRequest{Number: 42})
					if rejected {
						assertUnsupportedMediaType(t, err)
						return
					}
					assert.Nil(t, err)
					assert.Equal(t, response.GetNumber(), 42)
				})
				t.Run(prefix+"/server_stream", func(t *testing.T) {
					t.Parallel()
					ctx := rewriteContentTypeContext(t, rewriteRequestContentTypeHeader, variant.name)
					numbers, err := countUp(ctx, client, 3)
					if rejected {
						assertUnsupportedMediaType(t, err)
						return
					}
					assert.Nil(t, err)
					assert.Equal(t, numbers, []int64{1, 2, 3})
				})
				t.Run(prefix+"/client_unary", func(t *testing.T) {
					t.Parallel()
					ctx := rewriteContentTypeContext(t, rewriteResponseContentTypeHeader, variant.name)
					response, err := client.Ping(ctx, &pingv1.PingRequest{Number: 42})
					if rejected {
						assertInvalidContentType(t, err, variant)
						return
					}
					assert.Nil(t, err)
					assert.Equal(t, response.GetNumber(), 42)
				})
				t.Run(prefix+"/client_stream", func(t *testing.T) {
					t.Parallel()
					ctx := rewriteContentTypeContext(t, rewriteResponseContentTypeHeader, variant.name)
					numbers, err := countUp(ctx, client, 3)
					if rejected {
						assertInvalidContentType(t, err, variant)
						return
					}
					assert.Nil(t, err)
					assert.Equal(t, numbers, []int64{1, 2, 3})
				})
			}
		}
	}
	// Connect unary errors are always JSON, whatever the request codec, so the
	// client only reads the error body if its Content-Type is valid for JSON.
	client := pingv1connect.NewPingServiceClient(connect.NewClient(
		connecthttp.NewTransport(server.Client(), server.URL()),
	))
	for _, variant := range contentTypeVariants {
		t.Run("connect_"+variant.name+"/client_unary_error", func(t *testing.T) {
			t.Parallel()
			ctx := rewriteContentTypeContext(t, rewriteResponseContentTypeHeader, variant.name)
			_, err := client.Fail(ctx, &pingv1.FailRequest{Code: int32(connect.CodeFailedPrecondition)})
			if variant.rejectsJSON || variant.rejectsAll {
				// The client falls back to the code for HTTP 400.
				assert.Equal(t, connect.CodeOf(err), connect.CodeInternal)
				return
			}
			assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
			assert.Equal(t, errorMessageOf(err), errorMessage)
		})
	}
}

func rewriteContentTypeContext(t *testing.T, header, variantName string) context.Context {
	t.Helper()
	ctx, callInfo := connect.NewClientContext(t.Context())
	callInfo.RequestHeader().Set(header, variantName)
	return ctx
}

func countUp(ctx context.Context, client pingv1connect.PingServiceClient, number int64) ([]int64, error) {
	stream, err := client.CountUp(ctx, &pingv1.CountUpRequest{Number: number})
	if err != nil {
		return nil, err
	}
	var numbers []int64
	for {
		response, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return numbers, stream.Close()
		}
		if err != nil {
			_ = stream.Close()
			return numbers, err
		}
		numbers = append(numbers, response.GetNumber())
	}
}

func assertUnsupportedMediaType(t *testing.T, err error) {
	t.Helper()
	if assert.NotNil(t, err) {
		assert.Equal(t, connect.CodeOf(err), connect.CodeUnknown)
		assert.True(t, strings.Contains(errorMessageOf(err), "415"))
	}
}

// assertInvalidContentType checks the client rejected the response. A
// malformed Content-Type may not look like the protocol, so it may be unknown.
func assertInvalidContentType(t *testing.T, err error, variant contentTypeVariant) {
	t.Helper()
	if assert.NotNil(t, err) {
		if !variant.rejectsAll {
			assert.Equal(t, connect.CodeOf(err), connect.CodeInternal)
		}
		assert.True(t, strings.Contains(errorMessageOf(err), "invalid content-type"))
	}
}

func errorMessageOf(err error) string {
	if connectErr, ok := errors.AsType[*connect.Error](err); ok {
		return connectErr.Message()
	}
	return ""
}

func findContentTypeVariant(variants []contentTypeVariant, name string) *contentTypeVariant {
	for i := range variants {
		if variants[i].name == name {
			return &variants[i]
		}
	}
	return nil
}

// rewriteContentTypeMiddleware rewrites the request or response Content-Type
// using the variant named in the request's test headers.
func rewriteContentTypeMiddleware(next http.Handler, variants []contentTypeVariant) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if variant := findContentTypeVariant(variants, request.Header.Get(rewriteRequestContentTypeHeader)); variant != nil {
			request.Header.Set("Content-Type", variant.rewrite(request.Header.Get("Content-Type")))
		}
		if variant := findContentTypeVariant(variants, request.Header.Get(rewriteResponseContentTypeHeader)); variant != nil {
			responseWriter = &contentTypeRewriter{ResponseWriter: responseWriter, rewrite: variant.rewrite}
		}
		next.ServeHTTP(responseWriter, request)
	})
}

type contentTypeRewriter struct {
	http.ResponseWriter

	rewrite     func(string) string
	wroteHeader bool
}

func (w *contentTypeRewriter) WriteHeader(statusCode int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if contentType := w.Header().Get("Content-Type"); contentType != "" {
			w.Header().Set("Content-Type", w.rewrite(contentType))
		}
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *contentTypeRewriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *contentTypeRewriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *contentTypeRewriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
