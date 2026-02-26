package main

import (
	"context"
	"io"
	"log"
	"net"

	"github.com/anthony/grpc-proxy/api/echo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type server struct {
	echo.UnimplementedEchoServiceServer
	echo.UnimplementedSecureServiceServer
}

func (s *server) UnaryEcho(ctx context.Context, req *echo.EchoRequest) (*echo.EchoResponse, error) {
	log.Printf("Backend received UnaryEcho: %s", req.GetMessage())
	return &echo.EchoResponse{Message: "Backend says: " + req.GetMessage()}, nil
}

func (s *server) BidirectionalStreamingEcho(stream echo.EchoService_BidirectionalStreamingEchoServer) error {
	log.Printf("Backend Bidi stream opened")
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		log.Printf("Backend received BidiEcho: %s", req.GetMessage())
		if err := stream.Send(&echo.EchoResponse{Message: "Backend streams: " + req.GetMessage()}); err != nil {
			return err
		}
	}
}

func (s *server) SecureEcho(ctx context.Context, req *echo.SecureEnvelope) (*echo.SecureEnvelope, error) {
	log.Printf("Backend received SecureEcho Envelope, Payload: %s", string(req.GetPayload()))
	return &echo.SecureEnvelope{
		Payload:         []byte("Backend Processed: " + string(req.GetPayload())),
		TypeUrl:         req.GetTypeUrl(),
		ProxySignature:  req.GetProxySignature(), // Note: Backend usually verifies this!
		ClientSignature: req.GetClientSignature(),
	}, nil
}

func (s *server) InspectOuter(ctx context.Context, req *echo.SecureEnvelope) (*echo.SecureEnvelope, error) {
	// Simple echo for the inspect-outer benchmark
	return &echo.SecureEnvelope{
		Payload: []byte("Backend Processed (Inspect): " + string(req.GetPayload())),
		TypeUrl: req.GetTypeUrl(),
	}, nil
}

func (s *server) SecureBidiEcho(stream echo.SecureService_SecureBidiEchoServer) error {
	log.Printf("Backend Secure Bidi stream opened")
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		log.Printf("Backend received Secure Bidi Envelope, Payload: %s, ProxySig: %s", string(req.GetPayload()), string(req.GetProxySignature()))
		if err := stream.Send(&echo.SecureEnvelope{
			Payload:        []byte("Backend Streaming Processed: " + string(req.GetPayload())),
			TypeUrl:        req.GetTypeUrl(),
			ProxySignature: req.GetProxySignature(),
		}); err != nil {
			return err
		}
	}
}

func main() {
	lis, err := net.Listen("tcp", ":9090")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	echo.RegisterEchoServiceServer(s, &server{})
	echo.RegisterSecureServiceServer(s, &server{})

	// Enable server reflection to test the alternative approach
	reflection.Register(s)

	log.Printf("Backend listening on %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
