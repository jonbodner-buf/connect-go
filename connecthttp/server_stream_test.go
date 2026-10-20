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
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	pingv1 "connectrpc.com/connect/v2/internal/gen/connect/ping/v1"
	"connectrpc.com/connect/v2/internal/gen/connect/ping/v1/pingv1connect"
	"connectrpc.com/connect/v2/internal/memhttp/memhttptest"
)

func TestHeadersSentImmediately(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	srv := connect.NewServer()
	pingv1connect.RegisterPingServiceHandler(srv, slowCountUpPingServer{})
	Mount(mux, srv)

	server := memhttptest.NewServer(t, mux)
	client := pingv1connect.NewPingServiceClient(connect.NewClient(NewTransport(server.Client(), server.URL())))
	validate := func(ctx context.Context) {
		stream, err := client.CountUp(ctx, &pingv1.CountUpRequest{
			Number: 10,
		})
		// there's a 2-second delay in the stub implementation
		// if the context has a timeout < 2s, this will error
		if err != nil {
			t.Fatal(err)
		}
		var expected int64 = 0
		for resp, err := stream.Receive(); err == nil; resp, err = stream.Receive() {
			if resp.Number != expected {
				t.Errorf("expected %d, got %d", expected, resp.Number)
			}
			expected++
		}
		// close the stream
		stream.Close()
	}
	t.Run("has_timeout", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
		t.Cleanup(cancel)
		validate(ctx)
	})
	t.Run("no_timeout", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		validate(ctx)
	})
}

type slowCountUpPingServer struct {
	pingv1connect.UnimplementedPingServiceHandler
}

func (slowCountUpPingServer) CountUp(ctx context.Context, req *pingv1.CountUpRequest, stream pingv1connect.PingServiceCountUpServerStream) error {
	err := stream.SendHeaders()
	if err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	for i := range req.Number {
		err := stream.Send(&pingv1.CountUpResponse{
			Number: i,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
