# gRPC Dynamic Reverse Proxy Architecture Proposal

Based on your requirements, here is a proposed architecture and plan for the gRPC reverse proxy.

## 1. Handling Dynamic Message Types & RPCs

To handle new message types and RPCs without recompiling the proxy, we need a way to provide protobuf definitions at runtime so the proxy knows how to decode the raw bytes.

**Options Evaluated:**
*   **Go Plugins (`plugin` package):** Not recommended. Go plugins are notoriously fragile, require the exact same Go compiler version and dependencies, and don't work well on all platforms.
*   **`.proto` files:** Not ideal. Parsing raw text `.proto` files at runtime requires including a full parser in the proxy, which is complex and error-prone.
*   **`.pb` files (FileDescriptorSet) [RECOMMENDED]:** This is the industry standard approach for dynamic gRPC tooling (like `grpcurl`). You compile your `.proto` files into a binary `FileDescriptorSet` (using `protoc --descriptor_set_out=...`). The proxy loads this binary file at startup or hot-reloads it.
*   **gRPC Server Reflection [ALTERNATIVE RECOMMENDED]:** If your backend server has reflection enabled, the proxy can dynamically query the backend at startup to retrieve the proto definitions automatically, completely eliminating the need to manage `.pb` files.

**Decision:** We will support loading `.pb` (FileDescriptorSet) files to extract message descriptors.

## 2. Inspecting Fields and Values

Since the proxy won't have the Go structs compiled into it, we will use the `dynamicpb` package (`google.golang.org/protobuf/types/dynamicpb`).

1.  The proxy intercepts the raw bytes of a gRPC message.
2.  Using the RPC method name (e.g., `/my.Service/MyMethod`), it looks up the expected input/output message descriptor from the loaded `.pb` file.
3.  We decode the raw bytes into a `dynamicpb.Message`.
4.  We can then traverse the fields of this dynamic message, inspect the values, log them, or even modify them before re-encoding and sending them along.

## 3. Configuration (YAML)

We will use a YAML configuration file to define the proxy's behavior.

**Example `config.yaml`:**
```yaml
server:
  listen_address: "0.0.0.0:8080"
  
backend:
  address: "localhost:9090"  # Single backend for now

descriptors:
  - "paths/to/compiled_protos.pb"

routes:
  - method: "/my.Service/MyMethod"
    inspect: true
  - method: "/my.Service/AnotherMethod"
    inspect: false # Pass-through without decoding
# Alternatively, wildcard support: "/my.Service/*"
```

## 4. Connection & Routing Strategy

*   **Listen IP & Backend:** Based on the YAML config, the proxy will listen on a single IP:Port and forward all configured traffic to a single backend IP:Port.
*   **gRPC Director:** We will use a custom gRPC `UnknownServiceHandler`. When a request comes in for an unknown service (which is everything, since we didn't register handlers), this function is invoked. It extracts the method name from the stream context and routes it to the backend.

## 5. Bidirectional Streaming Implementation

Handling bidirectional streams while inspecting messages is the most complex part. 

**Workflow:**
1.  **Client connects** to the proxy initiating a bidirectional stream (Stream A).
2.  **Proxy intercepts** the call via the `UnknownServiceHandler`.
3.  **Proxy connects** to the Backend, establishing an outbound bidirectional stream (Stream B).
4.  **Forwarding Loop (Goroutines):** The proxy spins up two goroutines to pump messages in both directions simultaneously:
    *   **Goroutine 1 (Client -> Backend):**
        *   `Recv()` raw message from Stream A.
        *   *If inspection is enabled:* Decode to `dynamicpb.Message`, inspect/log.
        *   `Send()` the raw message to Stream B.
    *   **Goroutine 2 (Backend -> Client):**
        *   `Recv()` raw message from Stream B.
        *   *If inspection is enabled:* Decode to `dynamicpb.Message`, inspect/log.
        *   `Send()` the raw message to Stream A.
5.  **Termination:** The proxy correctly handles `io.EOF` and trailers in both directions to tear down the streams gracefully.

## Next Steps

1.  Review this proposal. Does the `.pb` file approach align with your deployment model?
2.  If approved, we can start by setting up the project structure, handling the YAML configuration, and building the core bidirectional stream interceptor (pass-through first, then adding dynamic decoding/inspection).
