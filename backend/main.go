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

func main() {
	lis, err := net.Listen("tcp", ":9090")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	echo.RegisterEchoServiceServer(s, &server{})
	
	// Enable server reflection to test the alternative approach
	reflection.Register(s)

	log.Printf("Backend listening on %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
