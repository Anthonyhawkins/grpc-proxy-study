# Proof of Concept: Dynamic Message-Aware gRPC Reverse Proxy

## Abstract

This document outlines a formal proof of concept (POC) for a dynamic, message-aware gRPC reverse proxy. The primary goal of this study is to demonstrate the feasibility of building a proxy in Go that can terminate gRPC streams, forward traffic between clients and backends, deeply inspect message payloads, and dynamically enforce Envelope-based Cryptographic Message Syntax (CMS) verification—all without requiring recompilation when new RPC methods or Protocol Buffer (protobuf) message types are introduced. 

## Background

Traditional gRPC systems rely heavily on statically compiled stubs generated from `.proto` definitions. While this provides strong type safety and high performance, it creates a tight coupling in infrastructure components like proxies. If a reverse proxy needs to inspect or log the contents of a gRPC message for routing, security, or observability, the standard approach requires importing the compiled Go structs into the proxy and rebuilding it every time the API changes. 

Furthermore, to fulfill enterprise security requirements, clients often must sign their payloads using a private key (CMS Signature) that the proxy verifies before forwarding the payload to backend, often injecting its own signature to establish a chain of custody. 

This POC proves that a highly dynamic, zero-recompilation approach is possible, leveraging the `google.golang.org/protobuf/types/dynamicpb` and `github.com/jhump/protoreflect` packages to dynamically reconstruct message schemas, enforce dynamic "Outer Message" envelope schemas configured via YAML, verify signatures, and inject new fields.

## Requirements

The proxy satisfies the following constraints:
1. **Universal Compatibility:** Function directly with any arbitrary gRPC RPC method (Unary, Client Streaming, Server Streaming, or Bidirectional Streaming).
2. **Agnostic Payload Handling:** Intercept and parse any arbitrary protobuf message type.
3. **Deep Inspection:** Unmarshal and inspect the fields and values of the message payload dynamically.
4. **Dynamic Envelopes:** Accept a flexible "Outer Message" Envelope schema configured by the operator via YAML rather than hardcoded in Go, protecting the integrity of the inner immutable payload bytes.
5. **Runtime Flexibility:** Support new RPCs and message definitions dynamically via `FileDescriptorSets` or `gRPC Server Reflection` without proxy restarts or binary alterations.

---

## 1. Dynamic YAML Routing and Envelope Construction

The proxy is driven by `config.yaml`. By utilizing a Route matching engine, the proxy can apply different security postures and dynamic field extraction rules depending on the RPC being invoked.

```yaml
routes:
  # Route 1: Legacy pass-through
  # The proxy will not attempt to decode the message; it streams raw bytes directly.
  - match: "/echo.EchoService/*"
    mode: "pass-thru"

  # Route 2: Secure Envelope Processing
  # The proxy will use dynamicpb to decode the payload, using the field names below 
  # to dynamically extract the payload, perform CMS verification, and inject its own signature.
  - match: "/echo.SecureService/*"
    mode: "inspect-verify-sign"
    envelope:
      payload_field: "payload"
      type_url_field: "type_url"
      client_sig_field: "client_signature"
      proxy_sig_field: "proxy_signature"
      metadata_field: "metadata"
```

Because the Envelope schema mappings are defined as arbitrary YAML strings (e.g. `payload_field: "payload"`), the proxy is entirely unopinionated about the exact `.proto` structure of your Envelope. If your backend team defines an Envelope where the signature field is called `cms_sig`, you simply update `config.yaml` to point to `client_sig_field: "cms_sig"` and the proxy intelligently adapts at runtime.

---

## 2. Architecture and Execution Flow

The core architecture relies on a "transparent" gRPC handler combined with a custom byte-level codec. 

### A. The Custom Codec (`bytesCodec`)
To prevent the gRPC server from attempting (and failing) to unmarshal incoming bytes into strongly-typed Go structs, the proxy defines a custom `encoding.Codec` named `bytesCodec` (`proxy/main.go:L55`). 

This codec instructs the gRPC server to treat all incoming payloads as raw `[]byte` slices, preserving the serialized protobuf data. The codec is forced on the server via `grpc.ForceServerCodec(bytesCodec{})`.

### B. Stream Termination and Transparent Routing
Because the proxy does not register any specific service surfaces (like `RegisterEchoServiceServer`), all incoming connections fall back to the `grpc.UnknownServiceHandler(transparentHandler)`. 

When a client connects to a bidirectional stream, `transparentHandler` behaves as follows:
1. **Intercept Method:** Extracts the requested RPC method name (e.g., `/echo.SecureService/SecureBidiEcho`).
2. **Match Route Config:** Checks the `config.yaml` to determine the security mode (e.g. `inspect-verify-sign`).
3. **Backend Dialing:** It dials the target backend using the same `bytesCodec` so that outbound messages remain as raw bytes payload if untouched.
4. **Asynchronous Pumping:** For bidirectional streams, it spins up two goroutines to pump bytes from Client -> Server and Server -> Client simultaneously (`proxy/main.go:L163`).

### C. The Envelope Processor (`processMsg`)
Inside the pumping loop, messages configured for inspection are routed to `processMsg`.

1. **Schema Retrieval:** It looks up the pre-loaded `MethodDescriptor` based on the intercepted RPC path to find exactly what `.proto` schema the client submitted.
2. **Dynamic Unmarshaling:** Constructs an empty dynamic message using `dynamic.NewMessage(msgDesc)` and unmarshals the raw `[]byte` wire payload into it.
3. **Field Extraction:** Using the dynamic schema, the proxy dynamically targets the fields requested by YAML (`route.Envelope.PayloadField`, `route.Envelope.ClientSigField`). 
4. **CMS Verification:** The proxy verifies the `client_signature` bytes against the immutable `payload` bytes using its configured Trust Store. By keeping the signature separated from the payload inside the Envelope, re-serialization vulnerabilities that invalidate signatures are mitigated.
5. **Inner Inspection:** The proxy reads the `type_url` field, dynamically looks up the inner message schema, and reconstructs the inner payload for inspection or logging.
6. **CMS Signing:** The proxy signs the `payload` using its private key and *injects* the bytes directly into the `dynamicpb.Message` field requested by `route.Envelope.ProxySigField`.
7. **Forwarding:** The updated `dynamicpb.Message` is marshaled back to `[]byte` and sent across the wire.

---

## 3. Defining New RPCs Without Recompilation

The defining feature of this proxy is its ability to learn about new data types instantly. The POC implements two distinct methods:

### Method 1: FileDescriptorSets (`.pb` files)
This is the "push" model. The proxy is provided a pre-compiled binary representation of the `.proto` files.

1. A developer adds a new RPC or Message to a `.proto` file.
2. The developer compiles the descriptors to a `.pb` file using `protoc --descriptor_set_out=api/echo.pb`.
3. The proxy parses the `.pb` file at startup (or via a hot-reload watcher mechanism) using `desc.CreateFileDescriptorsFromSet`. **No Go compilation of the proxy occurs.**

### Method 2: gRPC Server Reflection
This is the "pull" model. 

1. A developer adds a new RPC to a `.proto` file, builds the backend server, and deploys it.
2. The proxy utilizes the `grpcreflect` client to dial the backend server at startup and ask the backend for its schema directly.
3. The backends responds with all definitions. The proxy dynamically parses incoming matching packets exactly as if it was using a `.pb` file. **The proxy binary remains wholly unchanged.**

---

## 4. Hybrid Go/Rust CGO Architecture (Performance Offloading)

While Go's `dynamicpb` handles dynamic Envelopes well, cryptographic math (such as 2048-bit RSA PKCS#1v15 signing and verification over SHA-256) is highly CPU-intensive. To optimize this, the proxy supports offloading these heavy operations to a compiled **Rust C-ABI Dynamic Library (`cdylib`)** via **CGO (Go's Foreign Function Interface)**.

By keeping the proxy's core networking, HTTP/2 streams, and dynamic routing in Go, and dropping down into high-performance Rust (`rsa` + `sha2` crates) purely for the signature logic using `C.CBytes` and `C.GoBytes`, the system achieves the "best of both worlds".

### Benchmark Results (10,000 Concurrent Requests)

The proxy includes an integrated synthetic benchmark tool (`make bench-all`) to measure the performance overhead of both dynamic protobuf parsing and CGO cryptographic offloading. It also tests the difference between strict Ordered streaming and concurrent Unordered streaming.

| Mode | Engine | Processing | Total Time | Average Latency | Overhead vs Baseline |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Pass-Thru** | No Decoding | Ordered | 3.31 sec | **~331 µs / req** | *Baseline* |
| **Inspect Outer** | Go `dynamicpb` (No Crypto) | Ordered | 3.53 sec | **~353 µs / req** | **+22 µs / req** |
| **Secure Envelope** | **Pure Go Crypto** | Ordered | 21.50 sec | **~2.15 ms / req** | **+1.82 ms / req** |
| **Secure Envelope** | **Rust CGO FFI Crypto** | Ordered | 19.67 sec | **~1.96 ms / req** | **+1.63 ms / req** |
| **Secure Envelope** | **Rust CGO FFI Crypto** | **Unordered (Concurrent)** | 7.52 sec | **~752 µs / req** | **+421 µs / req** |

### Benchmark Takeaways

1. **Dynamic Protobuf Parsing is Fast:** The overhead of catching a raw bitstream, converting it to a dynamic message (`dynamicpb`), mapping Envelope fields via YAML, inspecting the inner payload, and serializing it back to bytes adds only **~22 microseconds** of latency per request. 
2. **Rust is ~10% Faster at Crypto than Go:** Offloading the two RSA-2048 operations (verify and sign) to the compiled Rust library shaved ~200 microseconds off each request compared to Go's standard `crypto/rsa` library, representing nearly a 10% total latency reduction across the entire execution flow.
3. **CGO Overhead is Minimal:** The cost of passing C-pointers (`*const u8`) back and forth across the Go/Rust C ABI boundary is insignificant compared to the heavy mathematical work being performed.
4. **Unordered Concurrency Yields Massive Throughput:** By configuring a route as `unordered: true` matching a stream where strict message ordering is not required, the proxy fans out the heavy cryptographic processing to a worker pool (syncing back to the stream via a channel). Doing this **nearly tripled throughput** (from 19.6s down to 7.5s for 10k messages) and slashed average effective latency per request directly.
