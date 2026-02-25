# Proof of Concept: Dynamic Message-Aware gRPC Reverse Proxy

## Abstract

This document outlines a formal proof of concept (POC) for a dynamic, message-aware gRPC reverse proxy. The primary goal of this study is to demonstrate the feasibility of building a proxy in Go that can terminate gRPC streams, forward traffic between clients and backends, and deeply inspect message payloads—all without requiring recompilation when new RPC methods or Protocol Buffer (protobuf) message types are introduced. 

## Background

Traditional gRPC systems rely heavily on statically compiled stubs generated from `.proto` definitions. While this provides strong type safety and high performance, it creates a tight coupling in infrastructure components like proxies. If a reverse proxy needs to inspect or log the contents of a gRPC message for routing, security, or observability, the standard approach requires importing the compiled Go structs into the proxy and rebuilding it every time the API changes. 

This POC proves that a dynamic approach is possible, leveraging the `google.golang.org/protobuf/types/dynamicpb` and `github.com/jhump/protoreflect` packages to dynamically reconstruct message schemas at runtime and read raw byte streams transparently.

## Requirements

The proxy must satisfy the following constraints:
1. **Language:** Written in Go.
2. **Universal Compatibility:** Function with any arbitrary gRPC RPC method (Unary, Client Streaming, Server Streaming, or Bidirectional Streaming).
3. **Agnostic Payload Handling:** Function with any arbitrary protobuf message type.
4. **Deep Inspection:** Capable of unmarshaling and inspecting the fields and values of the message payload.
5. **Runtime Flexibility:** Support new RPCs and message definitions without a proxy code change.

## Architecture and Execution Flow

The core architecture relies on a "transparent" gRPC handler combined with a custom byte-level codec. 

### 1. The Custom Codec (`bytesCodec`)
To prevent the gRPC server from attempting (and failing) to unmarshal incoming bytes into strongly-typed Go structs, the proxy defines a custom `encoding.Codec` named `bytesCodec` (`proxy/main.go:L29`). 

```go
func (bytesCodec) Unmarshal(data []byte, v interface{}) error {
	b, ok := v.(*[]byte)
    // ...
	*b = append([]byte(nil), data...)
	return nil
}
```
This codec instructs the gRPC server to treat all incoming payloads as raw `[]byte` slices, preserving the serialized protobuf data. The codec is forced on the server via `grpc.ForceServerCodec(bytesCodec{})` (`proxy/main.go:L68`).

### 2. The Unknown Service Handler
Because the proxy does not register any specific service surfaces (like `RegisterEchoServiceServer`), all incoming connections fall back to the `grpc.UnknownServiceHandler()`. We supply a custom function, `transparentHandler` (`proxy/main.go:L84`), to act as the traffic director.

### 3. Stream Termination and Forwarding
When a client connects, the `transparentHandler` behaves as follows:
1. **Intercept Method:** It extracts the requested RPC method name (e.g., `/echo.EchoService/BidirectionalStreamingEcho`) from the stream context.
2. **Context Propagation:** It copies incoming metadata (headers) to a new outgoing context (`proxy/main.go:L91`).
3. **Backend Dialing:** It dials the configured backend server using the `bytesCodec` so that outbound messages remain as raw bytes.
4. **Asynchronous Pumping:** For bidirectional streams, it spins up two goroutines:
   - **Client-to-Server (`c2s`):** Receives raw bytes from the client stream, conditionally inspects them, and sends them to the backend stream (`proxy/main.go:L130`).
   - **Server-to-Client (`s2c`):** Receives raw bytes from the backend stream, conditionally inspects them, and sends them to the client stream (`proxy/main.go:L114`).

### 4. Dynamic Message Inspection
Inside the pumping goroutines, the `inspectMsg` function (`proxy/main.go:L166`) is called. This function performs the deep inspection:

1. **Schema Retrieval:** It looks up the pre-loaded `MethodDescriptor` based on the intercepted RPC path.
2. **Dynamic Unmarshaling:** Based on whether the message is a request or response, it gets the specific `MessageDescriptor` (e.g., `InputType` or `OutputType`), constructs an empty dynamic message using `dynamic.NewMessage(msgDesc)`, and unmarshals the raw `[]byte` payload into it.
3. **Data Extraction:** Once decoded into a `dynamicpb.Message`, the object can be converted to JSON (`dynMsg.MarshalJSONIndent()`), logged, or algorithmically inspected.

## Defining New RPCs Without Recompilation

The defining feature of this proxy is its ability to understand new data types without recompiling the Go binary. The POC implements two distinct methods for schema discovery:

### Method 1: FileDescriptorSets (`.pb` files)
This is the recommended "push" model. The proxy is provided a pre-compiled binary representation of the `.proto` files.

**How to introduce a new RPC:**
1. A developer adds a new RPC or Message to a `.proto` file (e.g., `api/users/users.proto`).
2. The developer or a CI/CD pipeline compiles the descriptors to a `.pb` file using `protoc`:
   ```bash
   protoc --descriptor_set_out=api/users/users.pb --include_imports api/users/users.proto
   ```
3. The `.pb` file is placed in a directory accessible by the proxy.
4. The proxy parses the `.pb` file at startup (or via a hot-reload watcher mechanism) using `desc.CreateFileDescriptorsFromSet` (`proxy/main.go:L202`).
5. From that point forward, if an RPC matching the new definition passes through, the proxy has the schema required to decode the payload. **No Go compilation of the proxy occurs.**

### Method 2: gRPC Server Reflection
This is an alternative "pull" model. If the target backend server enables the gRPC Server Reflection API (as done in `backend/main.go:L48`), it self-reports its schema.

**How to introduce a new RPC:**
1. A developer adds a new RPC to a `.proto` file, builds the backend server, and deploys it.
2. When the proxy starts (or when triggered to refresh), it opens a connection to the backend and queries the reflection API using the `grpcreflect` client (`proxy/main.go:L227`).
3. The backend responds with its full schema (Services, Methods, and Types). The proxy caches these descriptors.
4. When traffic arrives, the proxy uses the cached reflection descriptors to unmarshal the raw bytes exactly as it would using a `.pb` file. **The proxy binary remains wholly unchanged.**
