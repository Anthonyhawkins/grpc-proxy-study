# Enhanced gRPC Proxy Proposal: Dynamic Envelopes & CMS Signatures

## 1. Goal: Flexibility Without Recompilation

The proxy must support advanced "Envelope" features (metadata inspection, payload verification, and CMS signing) *without* hardcoding the Envelope schema into the proxy's Go code. 

Furthermore, the proxy must gracefully support standard, non-envelope RPCs simultaneously.

**The Solution: Dynamic Field Mapping via Config**
Because the proxy already successfully loads `.pb` files or uses gRPC Server Reflection to discover the schema of *any* message at runtime, it already knows the structure of your Outer Message. 

Instead of hardcoding a type named `Envelope`, we will allow you to define *which fields* inside your discovered messages correspond to the Envelope concepts (the payload bytes, the type url, the signature, etc.) using the YAML configuration. 

---

## 2. Defining the Outer Message (Developer's Choice)

You can define your Outer Message however you like in your `.proto` files. For example, you might create two different envelopes for different backend services:

```proto
// Example A: A standard envelope for secure APIs
message SecureEnvelope {
    map<string, string> headers = 1;      // Metadata
    string action_type = 2;               // Type URL
    bytes action_payload = 3;             // The Inner Message
    bytes client_cms_sig = 4;             // Client Signature
    bytes proxy_cms_sig = 5;              // Proxy Signature
}

// Example B: A completely different message that just happens to have embedded bytes
message LegacySubmitRequest {
    string request_id = 1;
    bytes blob_data = 2;                  // The Inner Message
    string blob_type = 3;                 // Type URL
}
```

Both of these are compiled into your `api.pb` file (or exposed via Reflection) exactly like standard messages. The proxy parses them dynamically.

---

## 3. Dynamic YAML Configuration

We will introduce a `routes` array to the YAML config. This allows the proxy to apply different behaviors to different RPCs, mapping fields dynamically by their names.

```yaml
server:
  listen_address: "0.0.0.0:8080"
  
backend:
  address: "localhost:9090"

schema:
  method: "pb"
  pb_path: "api/echo/echo.pb"

# Route-specific configurations
routes:
  # Route 1: The standard, original pass-through behavior
  # Any RPC starting with /echo.EchoService assumes no envelope.
  - match: "/echo.EchoService/*"
    mode: "pass-thru"

  # Route 2: A secure endpoint using an Envelope
  - match: "/secure.API/SubmitTarget"
    mode: "inspect-verify-sign"
    envelope:
      # The proxy uses reflection to find these field names inside the request/response message
      payload_field: "action_payload"
      type_url_field: "action_type"
      client_sig_field: "client_cms_sig"
      proxy_sig_field: "proxy_cms_sig"

  # Route 3: A legacy endpoint that only requires inspection of the inner byte blob
  - match: "/legacy.v1.Service/*"
    mode: "inspect-outer"
    envelope:
      payload_field: "blob_data"
      type_url_field: "blob_type"

cms:
  client_trust_store: "certs/ca.crt"
  proxy_private_key: "certs/proxy.key"
  proxy_certificate: "certs/proxy.crt"
```

---

## 4. Execution Flow in the Proxy

When a client makes a call to `/secure.API/SubmitTarget`:

1.  **Intercept & Match:** The proxy intercepts the call and matches it against the `/secure.API/SubmitTarget` route in the YAML.
2.  **Schema Lookup:** The proxy looks up the `MethodDescriptor` from the loaded `.pb` (or Reflection data) to find the input message schema (e.g., `SecureEnvelope`).
3.  **Dynamic Unmarshal:** The proxy unmarshals the raw `[]byte` wire payload into a `dynamicpb.Message`.
4.  **Field Extraction:** Because the YAML configured `payload_field: "action_payload"` and `type_url_field: "action_type"`, the proxy dynamically extracts those specific fields from the `dynamicpb.Message`.
5.  **CMS Verification:** The proxy extracts the bytes from `client_cms_sig`, verifies it against the `action_payload` bytes using the `client_trust_store`.
6.  **Inner Inspection:** The proxy reads the string from `action_type` (e.g., `"type.googleapis.com/target.InnerData"`), looks up that inner schema from the `.pb` file, and dynamically decodes the `action_payload` bytes for logging/inspection.
7.  **CMS Signing:** The proxy signs the `action_payload` bytes, dynamically injects the signature into the `proxy_cms_sig` field of the `dynamicpb.Message`.
8.  **Forwarding:** The proxy re-serializes the `dynamicpb.Message` back into `[]byte` and forwards it to the backend.

### Original Functionality Retained

If a request hits `/echo.EchoService/UnaryEcho`, it matches the first route. The `mode` is `pass-thru`. The proxy skips the `dynamicpb` decoding entirely and just pumps the raw `[]byte` slices directly from the client stream to the backend stream, functioning exactly as it did initially.

No Go recompilation is ever required.
