package main

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/anthony/grpc-proxy/api/echo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	conn, err := grpc.Dial("localhost:8080", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	client := echo.NewEchoServiceClient(conn)

	log.Println("--- Testing UnaryEcho ---")
	res, err := client.UnaryEcho(context.Background(), &echo.EchoRequest{Message: "Hello Unary"})
	if err != nil {
		log.Fatalf("Unary error: %v", err)
	}
	log.Printf("UnaryResponse: %s", res.GetMessage())

	log.Println("--- Testing BidirectionalStreamingEcho ---")
	stream, err := client.BidirectionalStreamingEcho(context.Background())
	if err != nil {
		log.Fatalf("Bidi stream error: %v", err)
	}

	msgs := []string{"Ping 1", "Ping 2"}
	waitc := make(chan struct{})

	go func() {
		for {
			in, err := stream.Recv()
			if err == io.EOF {
				close(waitc)
				return
			}
			if err != nil {
				log.Fatalf("Failed to receive stream: %v", err)
			}
			log.Printf("Got BidiResponse: %s", in.GetMessage())
		}
	}()

	for _, msg := range msgs {
		log.Printf("Sending BidiRequest: %s", msg)
		if err := stream.Send(&echo.EchoRequest{Message: msg}); err != nil {
			log.Fatalf("Failed to send: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	stream.CloseSend()
	<-waitc
	log.Println("Client finished successfully.")
}
