package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/anthony/grpc-proxy/api/echo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	mode := flag.String("mode", "legacy", "legacy or secure")
	count := flag.Int("count", 1000, "number of requests to fire")
	flag.Parse()

	conn, err := grpc.Dial("localhost:8080", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	if *mode == "legacy" {
		client := echo.NewEchoServiceClient(conn)
		log.Printf("Starting benchmark of %d requests on Legacy Service (Pass-Thru)", *count)

		start := time.Now()
		for i := 0; i < *count; i++ {
			_, err := client.UnaryEcho(context.Background(), &echo.EchoRequest{Message: "Bench"})
			if err != nil {
				log.Fatalf("Req err: %v", err)
			}
		}
		dur := time.Since(start)
		log.Printf("[RESULT] Legacy Pass-Thru Mode: %d reqs in %v (Avg: %v/req)", *count, dur, dur/time.Duration(*count))
	} else if *mode == "inspect" {
		client := echo.NewSecureServiceClient(conn)
		log.Printf("Starting benchmark of %d requests on Secure Service (Inspect Outer Only)", *count)

		req := &echo.SecureEnvelope{
			Payload:  []byte("Bench Payload Bytes"),
			TypeUrl:  "type.googleapis.com/target.Benchmark",
			Metadata: map[string]string{"bench": "true"},
		}

		start := time.Now()
		for i := 0; i < *count; i++ {
			_, err := client.InspectOuter(context.Background(), req)
			if err != nil {
				log.Fatalf("Req err: %v", err)
			}
		}
		dur := time.Since(start)
		log.Printf("[RESULT] Inspect Outer Mode: %d reqs in %v (Avg: %v/req)", *count, dur, dur/time.Duration(*count))
	} else if *mode == "secure-unordered" {
		client := echo.NewSecureServiceClient(conn)
		log.Printf("Starting benchmark of %d requests on Secure Service (UNORDERED CONCURRENT STREAM)", *count)

		req := &echo.SecureEnvelope{
			Payload:         []byte("Bench Payload Bytes"),
			TypeUrl:         "type.googleapis.com/target.Benchmark",
			ClientSignature: []byte("mock_client_signature_bytes_for_verification"),
			Metadata:        map[string]string{"bench": "true"},
		}

		stream, err := client.UnorderedBidiEcho(context.Background())
		if err != nil {
			log.Fatalf("Stream start err: %v", err)
		}

		start := time.Now()

		// Sender Goroutine
		go func() {
			for i := 0; i < *count; i++ {
				if err := stream.Send(req); err != nil {
					log.Fatalf("Send error: %v", err)
				}
			}
			stream.CloseSend()
		}()

		// Receiver Routine (Main Thread)
		received := 0
		for {
			_, err := stream.Recv()
			if err != nil {
				if err.Error() == "EOF" {
					break
				}
				log.Fatalf("Recv error: %v", err)
			}
			received++
			if received == *count {
				break
			}
		}

		dur := time.Since(start)
		log.Printf("[RESULT] Unordered Secure Mode: %d reqs in %v (Avg: %v/req)", received, dur, dur/time.Duration(received))

	} else {
		client := echo.NewSecureServiceClient(conn)
		log.Printf("Starting benchmark of %d requests on Secure Service (Envelope with Crypto)", *count)

		req := &echo.SecureEnvelope{
			Payload:         []byte("Bench Payload Bytes"),
			TypeUrl:         "type.googleapis.com/target.Benchmark",
			ClientSignature: []byte("mock_client_signature_bytes_for_verification"),
			Metadata:        map[string]string{"bench": "true"},
		}

		start := time.Now()
		for i := 0; i < *count; i++ {
			_, err := client.SecureEcho(context.Background(), req)
			if err != nil {
				log.Fatalf("Req err: %v", err)
			}
		}
		dur := time.Since(start)
		log.Printf("[RESULT] Secure Envelope Mode: %d reqs in %v (Avg: %v/req)", *count, dur, dur/time.Duration(*count))
	}
}
