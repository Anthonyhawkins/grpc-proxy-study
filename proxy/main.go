package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
)

// methodDescriptors is a global cache mapping full RPC method names
// (e.g., "/echo.EchoService/UnaryEcho") to their parsed protobuf descriptors.
// This allows the proxy to know the exact input/output struct schemas at runtime.
var methodDescriptors map[string]*desc.MethodDescriptor
var backendAddr string

// bytesCodec is a custom grpc.Codec implementation that intentionally bypasses
// the standard protobuf unmarshaling process.
// Why it works: Instead of trying to deserialize incoming bytes into a compiled
// Go struct (which the proxy doesn't have), this codec simply passes the raw
// []byte slice exactly as it was received over the wire. This is crucial for
// transparent proxying.
type bytesCodec struct{}

func (bytesCodec) Marshal(v interface{}) ([]byte, error) {
	b, ok := v.(*[]byte)
	if !ok {
		return nil, fmt.Errorf("expected *[]byte, got %T", v)
	}
	return *b, nil
}

func (bytesCodec) Unmarshal(data []byte, v interface{}) error {
	b, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("expected *[]byte, got %T", v)
	}
	// Make a copy of the data to avoid holding onto the underlying transport buffer
	*b = append([]byte(nil), data...)
	return nil
}

func (bytesCodec) Name() string {
	return "proto"
}

func main() {
	// Parse CLI flags to determine how the proxy should discover schemas
	flag.StringVar(&backendAddr, "backend", "localhost:9090", "backend address")
	listenAddr := flag.String("listen", ":8080", "listen address")
	method := flag.String("method", "pb", "pb or reflect")
	pbPath := flag.String("pb", "../api/echo/echo.pb", "path to pb file")
	flag.Parse()

	log.Printf("Starting proxy, descriptor method: %s", *method)

	// Phase 1: Schema Discovery. Before listening for traffic, load the schemas
	// so we know how to decode the raw bytes we intercept.
	if *method == "pb" {
		methodDescriptors = loadFromPB(*pbPath)
	} else if *method == "reflect" {
		methodDescriptors = loadFromReflection(backendAddr)
	} else {
		log.Fatalf("unknown method %s", *method)
	}

	// Phase 2: Server Initialization.
	server := grpc.NewServer(
		// Force the server to use our custom codec, so all incoming messages
		// arrive as raw []byte slices.
		grpc.ForceServerCodec(bytesCodec{}),
		// This is the core routing mechanism. Because we don't register any
		// specific services, every single incoming RPC call falls back to this
		// UnknownServiceHandler, which we've pointed to transparentHandler.
		grpc.UnknownServiceHandler(transparentHandler),
	)

	lis, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("failed listening: %v", err)
	}

	log.Printf("Proxy listening on %s", lis.Addr().String())
	if err := server.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

// transparentHandler acts as the "Director". It terminates the incoming client
// stream, creates an outbound stream to the backend, and pumps bytes between them.
func transparentHandler(srv interface{}, serverStream grpc.ServerStream) error {
	// 1. Identify what the client is asking for (e.g., /pkg.Svc/Method)
	fullMethodName, ok := grpc.MethodFromServerStream(serverStream)
	if !ok {
		return status.Errorf(codes.Internal, "lowLevelServerStream not exists in context")
	}

	log.Printf("[Proxy] Intercepted %s", fullMethodName)

	// 2. Extract incoming headers (metadata) and prepare them to be sent upstream
	md, _ := metadata.FromIncomingContext(serverStream.Context())
	outCtx := metadata.NewOutgoingContext(serverStream.Context(), md.Copy())

	// 3. Dial the backend. Notice we use ForceCodec(bytesCodec{}) here too!
	// This ensures the backend client sends our raw []byte slice without
	// trying to protobuf-encode it a second time.
	backendConn, err := grpc.Dial(backendAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.ForceCodec(bytesCodec{})))
	if err != nil {
		return err
	}
	defer backendConn.Close()

	// Link the outgoing stream's lifecycle to the incoming stream's context
	clientCtx, clientCancel := context.WithCancel(outCtx)
	defer clientCancel()

	// 4. Create a completely generic client stream. Setting ServerStreams and
	// ClientStreams to true ensures this handler works universally for Unary,
	// Client-streaming, Server-streaming, and Bidirectional-streaming workloads.
	clientStream, err := grpc.NewClientStream(clientCtx, &grpc.StreamDesc{
		ServerStreams: true,
		ClientStreams: true,
	}, backendConn, fullMethodName)
	if err != nil {
		return err
	}

	// 5. Asynchronous Forwarding Loop
	// We spin up two goroutines to pump messages in both directions simultaneously.
	// We use channels to catch errors (or EOFs) indicating the stream has closed.

	// Goroutine 1: Backend -> Client Pump (s2c)
	s2cErrChan := make(chan error, 1)
	go func() {
		for {
			var payload []byte
			// Receive raw bytes from the backend
			if err := clientStream.RecvMsg(&payload); err != nil {
				s2cErrChan <- err
				break
			}
			// Inspect the raw bytes (Response)
			inspectMsg(fullMethodName, false, payload)
			// Forward raw bytes to the client
			if err := serverStream.SendMsg(&payload); err != nil {
				s2cErrChan <- err
				break
			}
		}
	}()

	// Goroutine 2: Client -> Backend Pump (c2s)
	c2sErrChan := make(chan error, 1)
	go func() {
		for {
			var payload []byte
			// Receive raw bytes from the client
			if err := serverStream.RecvMsg(&payload); err != nil {
				c2sErrChan <- err
				break
			}
			// Inspect the raw bytes (Request)
			inspectMsg(fullMethodName, true, payload)
			// Forward raw bytes to the backend
			if err := clientStream.SendMsg(&payload); err != nil {
				c2sErrChan <- err
				break
			}
		}
	}()

	// 6. Wait for stream termination
	// We block here until one of the pumps hits an error or io.EOF
	select {
	case err := <-s2cErrChan:
		// If the server closed the stream, we just return nil to close the client side
		if err == io.EOF {
			return nil
		}
		return err
	case err := <-c2sErrChan:
		// If the client closed the stream (Half-Close), we must tell the backend
		// we are done sending requests using CloseSend(), then wait for the
		// backend to finish sending its responses before returning.
		if err == io.EOF {
			clientStream.CloseSend()
			err = <-s2cErrChan
			if err == io.EOF {
				return nil
			}
			return err
		}
		return err
	}
}

// inspectMsg performs the dynamic reconstruction of the protobuf message
func inspectMsg(method string, isReq bool, payload []byte) {
	// 1. Look up the schema for this specific RPC method
	md, ok := methodDescriptors[method]
	if !ok {
		log.Printf("[Inspect Warning] No descriptor loaded for %s", method)
		return
	}

	// 2. Decide if we are decoding the Input schema (Request) or Output schema (Response)
	var msgDesc *desc.MessageDescriptor
	if isReq {
		msgDesc = md.GetInputType()
	} else {
		msgDesc = md.GetOutputType()
	}

	// 3. Create a blank dynamic message based on the discovered schema
	dynMsg := dynamic.NewMessage(msgDesc)

	// 4. Unmarshal the raw bytes into the dynamic message.
	// Since dynMsg knows the schema, it can correctly map the byte tags to field names.
	err := dynMsg.Unmarshal(payload)
	if err != nil {
		log.Printf("[Inspect Error] Failed to unmarshal %s: %v", method, err)
		return
	}

	// 5. Convert the populated dynamic message into human-readable JSON
	js, _ := dynMsg.MarshalJSONIndent()
	dir := "Request"
	if !isReq {
		dir = "Response"
	}
	log.Printf("[Inspect Data | %s] %s:\n%s", dir, method, string(js))
}

// loadFromPB reads a compiled FileDescriptorSet (.pb) file from disk
// This is the recommended "push" model for dynamic gRPC tooling
func loadFromPB(path string) map[string]*desc.MethodDescriptor {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("failed to read pb: %v", err)
	}

	fds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(b, fds); err != nil {
		log.Fatalf("failed unmarshal fds: %v", err)
	}

	fdMap, err := desc.CreateFileDescriptorsFromSet(fds)
	if err != nil {
		log.Fatalf("failed to parse fds: %v", err)
	}

	res := make(map[string]*desc.MethodDescriptor)
	// Iterate through all files, services, and methods in the descriptor set
	for _, fd := range fdMap {
		for _, svc := range fd.GetServices() {
			for _, md := range svc.GetMethods() {
				// Construct the fully qualified method name (e.g. /pkg.Service/Method)
				fullMethod := fmt.Sprintf("/%s/%s", svc.GetFullyQualifiedName(), md.GetName())
				res[fullMethod] = md
			}
		}
	}
	log.Printf("Loaded %d methods from %s file", len(res), path)
	return res
}

// loadFromReflection queries a live backend server for its schema using the
// gRPC Server Reflection Protocol. This is the "pull" model.
func loadFromReflection(addr string) map[string]*desc.MethodDescriptor {
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("reflect dial error: %v", err)
	}
	defer conn.Close()

	// NewClientV1Alpha creates a client that speaks the reflection protocol
	client := grpcreflect.NewClientV1Alpha(context.Background(), reflectionpb.NewServerReflectionClient(conn))
	defer client.Reset()

	svcs, err := client.ListServices()
	if err != nil {
		log.Fatalf("list services error: %v", err)
	}

	res := make(map[string]*desc.MethodDescriptor)
	for _, svcName := range svcs {
		// Ignore the reflection service itself
		if svcName == "grpc.reflection.v1alpha.ServerReflection" {
			continue
		}
		// Ask the remote server for the full schema of the service
		sd, err := client.ResolveService(svcName)
		if err != nil {
			log.Printf("ResolveService err for %s: %v", svcName, err)
			continue
		}
		for _, md := range sd.GetMethods() {
			fullMethod := fmt.Sprintf("/%s/%s", svcName, md.GetName())
			res[fullMethod] = md
		}
	}
	log.Printf("Loaded %d methods from reflection API", len(res))
	return res
}
