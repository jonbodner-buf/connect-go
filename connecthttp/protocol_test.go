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

package connecthttp

import (
	"io"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/internal/assert"
)

func TestCanonicalizeContentType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{name: "uppercase should be normalized", arg: "APPLICATION/json", want: "application/json"},
		{name: "utf-8 charset param should be stripped", arg: "application/json; charset=UTF-8", want: "application/json"},
		{name: "non-utf-8 charset param should be stripped", arg: "application/json; charset=Shift-JIS", want: "application/json"},
		{name: "unknown param should be dropped", arg: "multipart/form-data; boundary=fooBar", want: "multipart/form-data"},
		{name: "no parameters should be normalized", arg: "APPLICATION/json;  ", want: "application/json"},
		{name: "unknown and charset params should be dropped", arg: "application/json; foo=bar; charset=shift-jis", want: "application/json"},
		{name: "spaces around separators should be allowed", arg: `application/json ; charset = "utf-8" ; foo = bar`, want: "application/json"},
		{name: "grpc proto params should be dropped", arg: "application/grpc+proto; charset=utf-16; foo=bar", want: "application/grpc+proto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, canonicalizeContentType(tt.arg), tt.want)
		})
	}
}

func TestHasSupportedCharset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		arg  string
		want bool
	}{
		{name: "no parameters", arg: "application/json", want: true},
		{name: "utf-8 charset", arg: "application/json; charset=UTF-8", want: true},
		{name: "quoted utf-8 charset", arg: `application/json; charset="utf-8"`, want: true},
		{name: "spaces around separators", arg: `application/json ; charset = "utf-8" ; foo = bar`, want: true},
		{name: "unknown param only", arg: "application/json; foo=bar", want: true},
		{name: "malformed", arg: "application/json; charset", want: true},
		{name: "non-utf-8 json charset", arg: "application/json; charset=Shift-JIS", want: false},
		{name: "quoted non-utf-8 json charset", arg: `application/json; foo=bar; charset="Shift-JIS"`, want: false},
		{name: "non-utf-8 json suffix charset", arg: "application/connect+json; charset=iso-8859-1", want: false},
		{name: "non-utf-8 proto charset", arg: "application/proto; charset=iso-8859-1", want: true},
		{name: "non-utf-8 grpc proto charset", arg: "application/grpc+proto; charset=utf-16", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, hasSupportedCharset(tt.arg), tt.want)
		})
	}
}

func BenchmarkCanonicalizeContentType(b *testing.B) {
	b.Run("simple", func(b *testing.B) {
		for b.Loop() {
			_ = canonicalizeContentType("application/json")
		}
		b.ReportAllocs()
	})

	b.Run("with charset", func(b *testing.B) {
		for b.Loop() {
			_ = canonicalizeContentType("application/json; charset=utf-8")
		}
		b.ReportAllocs()
	})

	b.Run("with other param", func(b *testing.B) {
		for b.Loop() {
			_ = canonicalizeContentType("application/json; foo=utf-8")
		}
		b.ReportAllocs()
	})
}

func TestNegotiateCompression(t *testing.T) {
	t.Parallel()
	// pools builds the server's compressors. Order is the order they're
	// advertised in, but the client's Accept-Encoding order decides the
	// response encoding.
	pools := func(registered ...string) readOnlyCompressionPools {
		compressors := make([]connect.Compressor, 0, len(registered))
		for _, name := range registered {
			compressors = append(compressors, stubCompressor(name))
		}
		return newReadOnlyCompressionPools(compressors)
	}

	tests := []struct {
		name         string
		pools        readOnlyCompressionPools
		sent         string
		accept       string
		wantRequest  string
		wantResponse string
		wantErrCode  connect.Code
	}{{
		name:         "server supports gzip only",
		pools:        pools("gzip"),
		accept:       "gzip,br,zstd",
		wantRequest:  "identity",
		wantResponse: "gzip",
	}, {
		name:         "client order wins",
		pools:        pools("gzip", "zstd", "br"),
		accept:       "br,gzip,zstd",
		wantRequest:  "identity",
		wantResponse: "br",
	}, {
		name:         "client order wins over server order",
		pools:        pools("br", "gzip"),
		accept:       "gzip,br",
		wantRequest:  "identity",
		wantResponse: "gzip",
	}, {
		name:         "skips unsupported client preferences",
		pools:        pools("gzip"),
		accept:       "zstd,br,gzip",
		wantRequest:  "identity",
		wantResponse: "gzip",
	}, {
		// Compression is symmetric: a compressed request is answered in the
		// same encoding, whatever the client accepts.
		name:         "request encoding echoed",
		pools:        pools("br", "gzip"),
		sent:         "gzip",
		accept:       "br,gzip",
		wantRequest:  "gzip",
		wantResponse: "gzip",
	}, {
		name:         "request encoding echoed without accept",
		pools:        pools("br", "gzip"),
		sent:         "gzip",
		wantRequest:  "gzip",
		wantResponse: "gzip",
	}, {
		name:         "explicit identity request encoding",
		pools:        pools("br", "gzip"),
		sent:         "identity",
		accept:       "gzip,br",
		wantRequest:  "identity",
		wantResponse: "gzip",
	}, {
		name:         "no mutually supported encoding",
		pools:        pools("gzip"),
		accept:       "br,zstd",
		wantRequest:  "identity",
		wantResponse: "identity",
	}, {
		name:         "identity accepted",
		pools:        pools("gzip"),
		accept:       "identity",
		wantRequest:  "identity",
		wantResponse: "identity",
	}, {
		name:         "space separated accept",
		pools:        pools("br", "gzip"),
		accept:       "gzip, br",
		wantRequest:  "identity",
		wantResponse: "gzip",
	}, {
		name:         "no compressors registered",
		pools:        pools(),
		accept:       "gzip,br",
		wantRequest:  "identity",
		wantResponse: "identity",
	}, {
		name:        "unknown request encoding",
		pools:       pools("gzip"),
		sent:        "br",
		accept:      "gzip",
		wantErrCode: connect.CodeUnimplemented,
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request, response, err := negotiateCompression(test.pools, test.sent, test.accept)
			if test.wantErrCode != 0 {
				assert.NotNil(t, err)
				assert.Equal(t, connect.CodeOf(err), test.wantErrCode)
				return
			}
			assert.Nil(t, err)
			assert.Equal(t, request, test.wantRequest)
			assert.Equal(t, response, test.wantResponse)
		})
	}
}

func BenchmarkNegotiateCompression(b *testing.B) {
	pools := func(registered ...string) readOnlyCompressionPools {
		compressors := make([]connect.Compressor, 0, len(registered))
		for _, name := range registered {
			compressors = append(compressors, stubCompressor(name))
		}
		return newReadOnlyCompressionPools(compressors)
	}

	b.Run("default", func(b *testing.B) {
		available := pools(connect.CompressionNameGzip)
		for b.Loop() {
			_, _, _ = negotiateCompression(available, "", "gzip")
		}
		b.ReportAllocs()
	})

	b.Run("multiple compressors", func(b *testing.B) {
		available := pools(connect.CompressionNameGzip, "zstd", "br")
		for b.Loop() {
			_, _, _ = negotiateCompression(available, "", "gzip,br,zstd")
		}
		b.ReportAllocs()
	})

	b.Run("no mutually supported encoding", func(b *testing.B) {
		available := pools(connect.CompressionNameGzip)
		for b.Loop() {
			_, _, _ = negotiateCompression(available, "", "br,zstd")
		}
		b.ReportAllocs()
	})
}

// stubCompressor is a [connect.Compressor] that only carries a name. The
// negotiation tests exercise name selection, never the payload path.
type stubCompressor string

func (c stubCompressor) Name() string { return string(c) }

func (stubCompressor) Compress(dst io.Writer) (io.WriteCloser, error) {
	return identityWriteCloser{dst}, nil
}

func (stubCompressor) Decompress(src io.Reader) (io.ReadCloser, error) {
	return io.NopCloser(src), nil
}
