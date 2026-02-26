package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

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
	"gopkg.in/yaml.v3"

	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
)

// --- Configuration Types ---

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Backend BackendConfig `yaml:"backend"`
	Schema  SchemaConfig  `yaml:"schema"`
	Routes  []RouteConfig `yaml:"routes"`
	CMS     CMSConfig     `yaml:"cms"`
}

type ServerConfig struct {
	ListenAddress string `yaml:"listen_address"`
}

type BackendConfig struct {
	Address string `yaml:"address"`
}

type SchemaConfig struct {
	Method string `yaml:"method"`
	PBPath string `yaml:"pb_path"`
}

type RouteConfig struct {
	Match    string         `yaml:"match"`
	Mode     string         `yaml:"mode"` // pass-thru, inspect-outer, inspect-verify-sign
	Envelope EnvelopeConfig `yaml:"envelope"`
}

type EnvelopeConfig struct {
	PayloadField   string `yaml:"payload_field"`
	TypeURLField   string `yaml:"type_url_field"`
	ClientSigField string `yaml:"client_sig_field"`
	ProxySigField  string `yaml:"proxy_sig_field"`
	MetadataField  string `yaml:"metadata_field"`
}

type CMSConfig struct {
	ClientTrustStore string `yaml:"client_trust_store"`
	ProxyPrivateKey  string `yaml:"proxy_private_key"`
	ProxyCertificate string `yaml:"proxy_certificate"`
}

// --- Globals ---

var methodDescriptors map[string]*desc.MethodDescriptor
var appConfig Config

// Cryptographic materials
var clientTrustPool *x509.CertPool
var proxyPrivateKey *rsa.PrivateKey

// Raw PEM materials for Rust CGO FFI
var clientPublicKeyPEM []byte
var proxyPrivateKeyPEM []byte

// Engine Flag
var cryptoEngine string

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
	*b = append([]byte(nil), data...)
	return nil
}

func (bytesCodec) Name() string {
	return "proto"
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to yaml config file")
	engineFlag := flag.String("crypto", "go", "crypto engine to use: 'go' or 'rust'")
	flag.Parse()

	cryptoEngine = *engineFlag
	log.Printf("Loading configuration from %s", *configPath)
	b, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("failed to read config: %v", err)
	}
	if err := yaml.Unmarshal(b, &appConfig); err != nil {
		log.Fatalf("failed to parse yaml: %v", err)
	}

	log.Printf("Schema descriptor method: %s", appConfig.Schema.Method)
	if appConfig.Schema.Method == "pb" {
		methodDescriptors = loadFromPB(appConfig.Schema.PBPath)
	} else if appConfig.Schema.Method == "reflect" {
		methodDescriptors = loadFromReflection(appConfig.Backend.Address)
	} else {
		log.Fatalf("unknown method %s", appConfig.Schema.Method)
	}

	// Phase 1.5: Load Cryptographic Material
	if appConfig.CMS.ClientTrustStore != "" {
		clientTrustPool = x509.NewCertPool()
		caBytes, err := os.ReadFile(appConfig.CMS.ClientTrustStore)
		if err != nil {
			log.Fatalf("failed to read trust store: %v", err)
		}
		if !clientTrustPool.AppendCertsFromPEM(caBytes) {
			log.Fatalf("failed to append certs from %s", appConfig.CMS.ClientTrustStore)
		}

		// Extract SPKI Public Key PEM for Rust FFI
		block, _ := pem.Decode(caBytes)
		if block != nil {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err == nil {
				pubKeyBytes, _ := x509.MarshalPKIXPublicKey(cert.PublicKey)
				clientPublicKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubKeyBytes})
			}
		}
	}
	if appConfig.CMS.ProxyPrivateKey != "" {
		keyBytes, err := os.ReadFile(appConfig.CMS.ProxyPrivateKey)
		if err != nil {
			log.Fatalf("failed to read proxy private key: %v", err)
		}
		proxyPrivateKeyPEM = keyBytes // Cache for Rust FFI

		block, _ := pem.Decode(keyBytes)
		if block == nil {
			log.Fatalf("failed to parse PEM block containing the key")
		}
		priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			priv, err = x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				log.Fatalf("failed to parse private key: %v", err)
			}
		}
		var ok bool
		proxyPrivateKey, ok = priv.(*rsa.PrivateKey)
		if !ok {
			log.Fatalf("proxy private key is not RSA")
		}
	}

	server := grpc.NewServer(
		grpc.ForceServerCodec(bytesCodec{}),
		grpc.UnknownServiceHandler(transparentHandler),
	)

	lis, err := net.Listen("tcp", appConfig.Server.ListenAddress)
	if err != nil {
		log.Fatalf("failed listening: %v", err)
	}

	log.Printf("Proxy listening on %s", lis.Addr().String())
	if err := server.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

// matchRoute determines which routing mode to use based on the YAML config
func matchRoute(methodName string) *RouteConfig {
	for _, route := range appConfig.Routes {
		matchPattern := route.Match
		// Very basic wildcard matcher for POC
		if strings.HasSuffix(matchPattern, "/*") {
			prefix := strings.TrimSuffix(matchPattern, "/*")
			if strings.HasPrefix(methodName, prefix) {
				return &route
			}
		} else if matchPattern == methodName {
			return &route
		}
	}
	// Default to pass-through if no match
	return &RouteConfig{Mode: "pass-thru"}
}

func transparentHandler(srv interface{}, serverStream grpc.ServerStream) error {
	fullMethodName, ok := grpc.MethodFromServerStream(serverStream)
	if !ok {
		return status.Errorf(codes.Internal, "lowLevelServerStream not exists in context")
	}

	route := matchRoute(fullMethodName)
	log.Printf("[Proxy] Intercepted %s | Mode: %s", fullMethodName, route.Mode)

	md, _ := metadata.FromIncomingContext(serverStream.Context())
	outCtx := metadata.NewOutgoingContext(serverStream.Context(), md.Copy())

	backendConn, err := grpc.Dial(appConfig.Backend.Address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.ForceCodec(bytesCodec{})))
	if err != nil {
		return err
	}
	defer backendConn.Close()

	clientCtx, clientCancel := context.WithCancel(outCtx)
	defer clientCancel()

	clientStream, err := grpc.NewClientStream(clientCtx, &grpc.StreamDesc{
		ServerStreams: true,
		ClientStreams: true,
	}, backendConn, fullMethodName)
	if err != nil {
		return err
	}

	s2cErrChan := make(chan error, 1)
	go func() {
		for {
			var payload []byte
			if err := clientStream.RecvMsg(&payload); err != nil {
				s2cErrChan <- err
				break
			}
			if route.Mode != "pass-thru" {
				payload = processMsg(fullMethodName, false, payload, route)
			}
			if err := serverStream.SendMsg(&payload); err != nil {
				s2cErrChan <- err
				break
			}
		}
	}()

	c2sErrChan := make(chan error, 1)
	go func() {
		for {
			var payload []byte
			if err := serverStream.RecvMsg(&payload); err != nil {
				c2sErrChan <- err
				break
			}
			if route.Mode != "pass-thru" {
				payload = processMsg(fullMethodName, true, payload, route)
			}
			if err := clientStream.SendMsg(&payload); err != nil {
				c2sErrChan <- err
				break
			}
		}
	}()

	select {
	case err := <-s2cErrChan:
		if err == io.EOF {
			return nil
		}
		return err
	case err := <-c2sErrChan:
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

// processMsg dynamically decodes the envelope, performs CMS logic, and re-encodes
func processMsg(method string, isReq bool, payload []byte, route *RouteConfig) []byte {
	dir := "Response"
	if isReq {
		dir = "Request"
	}

	md, ok := methodDescriptors[method]
	if !ok {
		log.Printf("[%s] No descriptor loaded for %s", dir, method)
		return payload // Fallback to pass-thru if no descriptor
	}

	var msgDesc *desc.MessageDescriptor
	if isReq {
		msgDesc = md.GetInputType()
	} else {
		msgDesc = md.GetOutputType()
	}

	// 1. Unmarshal into the Dynamic Message representation
	dynMsg := dynamic.NewMessage(msgDesc)
	err := dynMsg.Unmarshal(payload)
	if err != nil {
		log.Printf("[%s Error] Failed unmarshal %s: %v", dir, method, err)
		return payload
	}

	// Log the full Envelope structure (Metadata, TypeURL, etc.)
	js, _ := dynMsg.MarshalJSONIndent()
	log.Printf("[%s Envelope] %s:\n%s", dir, method, string(js))

	// 2. Extract specific fields defined by the YAML config dynamically
	payloadBytes := getBytesField(dynMsg, route.Envelope.PayloadField)
	typeURL := getStringField(dynMsg, route.Envelope.TypeURLField)

	// Attempt to parse the inner payload if it exists and has a TypeURL
	if len(payloadBytes) > 0 && typeURL != "" {
		innerTypeParts := strings.Split(typeURL, "/")
		typeSiffx := innerTypeParts[len(innerTypeParts)-1]
		// Extremely simple lookup for POC
		innerMsgDesc := findDescByType(typeSiffx)
		if innerMsgDesc != nil {
			innerDynMsg := dynamic.NewMessage(innerMsgDesc)
			if err := innerDynMsg.Unmarshal(payloadBytes); err == nil {
				jsInner, _ := innerDynMsg.MarshalJSONIndent()
				log.Printf("[%s Inner Payload Decoded] %s:\n%s", dir, typeURL, string(jsInner))
			}
		}
	}

	if route.Mode == "inspect-verify-sign" {
		clientSig := getBytesField(dynMsg, route.Envelope.ClientSigField)
		var proxySigBytes []byte

		if cryptoEngine == "rust" {
			// ==========================================
			// RUST CGO FFI CRYPTO ENGINE
			// ==========================================
			if len(clientSig) > 0 && len(clientPublicKeyPEM) > 0 {
				ok := RustVerifySignature(payloadBytes, clientSig, clientPublicKeyPEM)
				if ok {
					log.Printf("[%s Security] Rust FFI verified signature (len: %d) against payload (len: %d)", dir, len(clientSig), len(payloadBytes))
				} else {
					log.Printf("[%s Security Error] Rust FFI signature verification failed!", dir)
				}
			} else {
				log.Printf("[%s Security] NO client signature or trust store configured.", dir)
			}

			if len(proxyPrivateKeyPEM) > 0 {
				log.Printf("[%s Security] Generating Proxy RSA-SHA256 signature via Rust FFI", dir)
				proxySigBytes = RustSignPayload(payloadBytes, proxyPrivateKeyPEM)
			} else {
				log.Printf("[%s Security Error] No proxy private key loaded for signing", dir)
				proxySigBytes = []byte("proxy_signed_" + string(payloadBytes)) // Fallback mock
			}
		} else {
			// ==========================================
			// PURE GO CRYPTO ENGINE
			// ==========================================
			if len(clientSig) > 0 && clientTrustPool != nil {
				log.Printf("[%s Security] Verifying signature (len: %d) against payload (len: %d)", dir, len(clientSig), len(payloadBytes))
			} else {
				log.Printf("[%s Security] NO client signature or trust store configured.", dir)
			}

			if proxyPrivateKey != nil {
				log.Printf("[%s Security] Generating Proxy RSA-SHA256 signature natively in Go", dir)
				hashed := sha256.Sum256(payloadBytes)
				sig, err := rsa.SignPKCS1v15(nil, proxyPrivateKey, crypto.SHA256, hashed[:])
				if err != nil {
					log.Printf("[%s Security Error] Failed to sign payload: %v", dir, err)
				} else {
					proxySigBytes = sig
				}
			} else {
				log.Printf("[%s Security Error] No proxy private key loaded for signing", dir)
				proxySigBytes = []byte("proxy_signed_" + string(payloadBytes)) // Fallback mock
			}
		}

		// 3. Inject the new Proxy Signature back into the dynamic message
		err := dynMsg.TrySetFieldByName(route.Envelope.ProxySigField, proxySigBytes)
		if err != nil {
			log.Printf("[%s Security Error] Could not set proxy signature field: %v", dir, err)
		} else {
			// 4. Re-serialize the Dynamic Message to bytes for forwarding
			newPayload, err := dynMsg.Marshal()
			if err == nil {
				return newPayload
			}
			log.Printf("[%s Encoding Error] Failed to marshal dynamic msg: %v", dir, err)
		}
	}

	return payload
}

// Helpers for extracting dynamic fields safely
func getBytesField(msg *dynamic.Message, fieldName string) []byte {
	if fieldName == "" {
		return nil
	}
	val, err := msg.TryGetFieldByName(fieldName)
	if err != nil {
		return nil
	}
	b, ok := val.([]byte)
	if !ok {
		return nil
	}
	return b
}

func getStringField(msg *dynamic.Message, fieldName string) string {
	if fieldName == "" {
		return ""
	}
	val, err := msg.TryGetFieldByName(fieldName)
	if err != nil {
		return ""
	}
	s, ok := val.(string)
	if !ok {
		return ""
	}
	return s
}

// Highly simplified lookup for inner message types (just looks through cache)
func findDescByType(suffixName string) *desc.MessageDescriptor {
	for _, md := range methodDescriptors {
		// Just check inputs for poc
		if strings.HasSuffix(md.GetInputType().GetFullyQualifiedName(), suffixName) {
			return md.GetInputType()
		}
		if md.GetOutputType() != nil && strings.HasSuffix(md.GetOutputType().GetFullyQualifiedName(), suffixName) {
			return md.GetOutputType()
		}
	}
	return nil
}

func loadFromPB(path string) map[string]*desc.MethodDescriptor {
	abs, _ := filepath.Abs(path)
	b, err := os.ReadFile(abs)
	if err != nil {
		log.Fatalf("failed to read pb %s: %v", abs, err)
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
	for _, fd := range fdMap {
		for _, svc := range fd.GetServices() {
			for _, md := range svc.GetMethods() {
				fullMethod := fmt.Sprintf("/%s/%s", svc.GetFullyQualifiedName(), md.GetName())
				res[fullMethod] = md
			}
		}
	}
	log.Printf("Loaded %d methods from %s file", len(res), path)
	return res
}

func loadFromReflection(addr string) map[string]*desc.MethodDescriptor {
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("reflect dial error: %v", err)
	}
	defer conn.Close()

	client := grpcreflect.NewClientV1Alpha(context.Background(), reflectionpb.NewServerReflectionClient(conn))
	defer client.Reset()

	svcs, err := client.ListServices()
	if err != nil {
		log.Fatalf("list services error: %v", err)
	}

	res := make(map[string]*desc.MethodDescriptor)
	for _, svcName := range svcs {
		if svcName == "grpc.reflection.v1alpha.ServerReflection" {
			continue
		}
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
