#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>

bool verify_signature(const uint8_t *payload_ptr,
                      uintptr_t payload_len,
                      const uint8_t *sig_ptr,
                      uintptr_t sig_len,
                      const uint8_t *pub_key_ptr,
                      uintptr_t pub_key_len);

bool sign_payload(const uint8_t *payload_ptr,
                  uintptr_t payload_len,
                  const uint8_t *priv_key_ptr,
                  uintptr_t priv_key_len,
                  uint8_t **out_sig_ptr,
                  uintptr_t *out_sig_len,
                  uintptr_t *out_sig_cap);

void free_signature(uint8_t *sig_ptr, uintptr_t sig_len, uintptr_t sig_cap);
