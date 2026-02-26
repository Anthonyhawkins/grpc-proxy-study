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

	log.Println("=== Testing Legacy Service (Pass-Thru) ===")
	legacyClient := echo.NewEchoServiceClient(conn)

	// Legacy Unary
	lRes, err := legacyClient.UnaryEcho(context.Background(), &echo.EchoRequest{Message: "Legacy Unary"})
	if err != nil {
		log.Fatalf("Legacy Unary error: %v", err)
	}
	log.Printf("Legacy UnaryResponse: %s", lRes.GetMessage())

	log.Println("\n=== Testing Secure Service (Envelope + Signature) ===")
	secureClient := echo.NewSecureServiceClient(conn)

	// Secure Unary (Envelope)
	envReq := &echo.SecureEnvelope{
		Payload:         []byte(`{"user_id": "123", "action": "login"}`),
		TypeUrl:         "type.googleapis.com/target.LoginRequest",
		ClientSignature: []byte("client_signed_bytes"),
		Metadata:        map[string]string{"trace_id": "req-999"},
	}
	sRes, err := secureClient.SecureEcho(context.Background(), envReq)
	if err != nil {
		log.Fatalf("Secure Unary error: %v", err)
	}
	log.Printf("Secure UnaryResponse: %s", string(sRes.GetPayload()))

	// Secure Bidi (Envelope)
	log.Println("\n=== Testing Secure Bidi Stream ===")
	stream, err := secureClient.SecureBidiEcho(context.Background())
	if err != nil {
		log.Fatalf("Secure Bidi stream error: %v", err)
	}

	msgs := []string{`{"cmd":"start"}`, `{"cmd":"stop"}`}
	waitc := make(chan struct{})

	go func() {
		for {
			in, err := stream.Recv()
			if err == io.EOF {
				close(waitc)
				return
			}
			if err != nil {
				log.Fatalf("Failed to receive secure stream: %v", err)
			}
			log.Printf("Got Secure BidiResponse Payload: %s, ProxySig: %s", string(in.GetPayload()), string(in.GetProxySignature()))
		}
	}()

	for _, msg := range msgs {
		log.Printf("Sending Secure BidiRequest Envelope with payload: %s", msg)
		if err := stream.Send(&echo.SecureEnvelope{
			Payload:         []byte(msg),
			TypeUrl:         "type.googleapis.com/target.Command",
			ClientSignature: []byte("client_sig_" + msg),
		}); err != nil {
			log.Fatalf("Failed to send: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	stream.CloseSend()
	<-waitc
	log.Println("Client finished successfully.")
}
