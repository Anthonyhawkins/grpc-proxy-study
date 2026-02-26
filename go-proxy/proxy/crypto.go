package main

/*
#cgo CFLAGS: -I../../rust-crypto
#cgo darwin LDFLAGS: -L../../rust-crypto/target/release -lrustcrypto
#cgo linux LDFLAGS: -L../../rust-crypto/target/release -lrustcrypto
#include "cryptolib.h"
#include <stdlib.h>
*/
import "C"
import (
	"log"
	"unsafe"
)

// RustVerifySignature calls the Rust FFI verify_signature function
func RustVerifySignature(payload, sig, pubKeyPEM []byte) bool {
	if len(payload) == 0 || len(sig) == 0 || len(pubKeyPEM) == 0 {
		return false
	}

	cPayload := C.CBytes(payload)
	cSig := C.CBytes(sig)
	cPubKey := C.CBytes(pubKeyPEM)

	defer C.free(cPayload)
	defer C.free(cSig)
	defer C.free(cPubKey)

	res := C.verify_signature(
		(*C.uint8_t)(cPayload), C.uintptr_t(len(payload)),
		(*C.uint8_t)(cSig), C.uintptr_t(len(sig)),
		(*C.uint8_t)(cPubKey), C.uintptr_t(len(pubKeyPEM)),
	)

	return bool(res)
}

// RustSignPayload calls the Rust FFI sign_payload function
func RustSignPayload(payload, privKeyPEM []byte) []byte {
	if len(payload) == 0 || len(privKeyPEM) == 0 {
		return nil
	}

	cPayload := C.CBytes(payload)
	cPrivKey := C.CBytes(privKeyPEM)

	defer C.free(cPayload)
	defer C.free(cPrivKey)

	var cOutSig *C.uint8_t
	var cOutLen C.uintptr_t
	var cOutCap C.uintptr_t

	success := C.sign_payload(
		(*C.uint8_t)(cPayload), C.uintptr_t(len(payload)),
		(*C.uint8_t)(cPrivKey), C.uintptr_t(len(privKeyPEM)),
		&cOutSig, &cOutLen, &cOutCap,
	)

	if !bool(success) || cOutSig == nil {
		log.Printf("[Rust FFI Error] Failed to sign payload")
		return nil
	}

	// Copy the signature bytes from Rust memory into Go memory
	sigLen := int(cOutLen)
	sigBytes := C.GoBytes(unsafe.Pointer(cOutSig), C.int(sigLen))

	// Ask Rust to free its allocated Vec<u8>
	C.free_signature(cOutSig, cOutLen, cOutCap)

	return sigBytes
}
