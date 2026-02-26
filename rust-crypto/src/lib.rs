use rsa::pkcs1::DecodeRsaPrivateKey;
use rsa::pkcs8::{DecodePrivateKey, DecodePublicKey};
use rsa::{Pkcs1v15Sign, RsaPrivateKey, RsaPublicKey};
use sha2::{Digest, Sha256};
use std::slice;
use std::str;

#[no_mangle]
pub extern "C" fn verify_signature(
    payload_ptr: *const u8,
    payload_len: usize,
    sig_ptr: *const u8,
    sig_len: usize,
    pub_key_ptr: *const u8,
    pub_key_len: usize,
) -> bool {
    if payload_ptr.is_null() || sig_ptr.is_null() || pub_key_ptr.is_null() {
        return false;
    }

    let payload = unsafe { slice::from_raw_parts(payload_ptr, payload_len) };
    let sig = unsafe { slice::from_raw_parts(sig_ptr, sig_len) };
    let pub_key_bytes = unsafe { slice::from_raw_parts(pub_key_ptr, pub_key_len) };

    let pub_key_str = match str::from_utf8(pub_key_bytes) {
        Ok(s) => s,
        Err(_) => return false,
    };

    let public_key = match RsaPublicKey::from_public_key_pem(pub_key_str) {
        Ok(k) => k,
        Err(_) => return false,
    };

    let mut hasher = Sha256::new();
    hasher.update(payload);
    let hashed = hasher.finalize();

    let scheme = Pkcs1v15Sign::new::<Sha256>();
    public_key.verify(scheme, &hashed, sig).is_ok()
}

#[no_mangle]
pub extern "C" fn sign_payload(
    payload_ptr: *const u8,
    payload_len: usize,
    priv_key_ptr: *const u8,
    priv_key_len: usize,
    out_sig_ptr: *mut *mut u8,
    out_sig_len: *mut usize,
    out_sig_cap: *mut usize,
) -> bool {
    if payload_ptr.is_null() || priv_key_ptr.is_null() || out_sig_ptr.is_null() || out_sig_len.is_null() || out_sig_cap.is_null() {
        return false;
    }

    let payload = unsafe { slice::from_raw_parts(payload_ptr, payload_len) };
    let priv_key_bytes = unsafe { slice::from_raw_parts(priv_key_ptr, priv_key_len) };

    let priv_key_str = match str::from_utf8(priv_key_bytes) {
        Ok(s) => s,
        Err(_) => return false,
    };

    // Try PKCS8 first, then PKCS1
    let private_key = match RsaPrivateKey::from_pkcs8_pem(priv_key_str) {
        Ok(k) => k,
        Err(_) => match RsaPrivateKey::from_pkcs1_pem(priv_key_str) {
            Ok(k) => k,
            Err(_) => return false,
        },
    };

    let mut hasher = Sha256::new();
    hasher.update(payload);
    let hashed = hasher.finalize();

    let scheme = Pkcs1v15Sign::new::<Sha256>();
    let mut sig_vec = match private_key.sign(scheme, &hashed) {
        Ok(s) => s,
        Err(_) => return false,
    };

    sig_vec.shrink_to_fit();
    let ptr = sig_vec.as_mut_ptr();
    let len = sig_vec.len();
    let cap = sig_vec.capacity();

    unsafe {
        *out_sig_ptr = ptr;
        *out_sig_len = len;
        *out_sig_cap = cap;
    }

    std::mem::forget(sig_vec);
    true
}

#[no_mangle]
pub extern "C" fn free_signature(sig_ptr: *mut u8, sig_len: usize, sig_cap: usize) {
    if !sig_ptr.is_null() {
        unsafe {
            let _ = Vec::from_raw_parts(sig_ptr, sig_len, sig_cap);
        }
    }
}
