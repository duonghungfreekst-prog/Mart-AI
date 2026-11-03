//! MuseRouter Core Security & Cryptography Module (`muse_router_core`)
//! Tuân thủ Antigravity Polyglot v2.4:
//! - Prefix log bắt buộc: `[CORE]`
//! - Constant-time comparison chống timing attack
//! - Mã hóa AES-256-CTR + HMAC-SHA256 (Authenticated Encryption) cho Cookie/Token trước khi lưu đĩa
//! - Export `free_rust_string` và `free_rust_bytes` chống FFI Memory Leak

use std::ffi::{CStr, CString};
use std::os::raw::c_char;
use std::time::{SystemTime, UNIX_EPOCH};

const LOG_PREFIX: &str = "[CORE]";
const AES_BLOCK_SIZE: usize = 16;
const SHA256_BLOCK_SIZE: usize = 64;

/// FIPS 197 AES S-Box chuẩn
const AES_SBOX: [u8; 256] = [
    0x63, 0x7c, 0x77, 0x7b, 0xf2, 0x6b, 0x6f, 0xc5, 0x30, 0x01, 0x67, 0x2b, 0xfe, 0xd7, 0xab, 0x76,
    0xca, 0x82, 0xc9, 0x7d, 0xfa, 0x59, 0x47, 0xf0, 0xad, 0xd4, 0xa2, 0xaf, 0x9c, 0xa4, 0x72, 0xc0,
    0xb7, 0xfd, 0x93, 0x26, 0x36, 0x3f, 0xf7, 0xcc, 0x34, 0xa5, 0xe5, 0xf1, 0x71, 0xd8, 0x31, 0x15,
    0x04, 0xc7, 0x23, 0xc3, 0x18, 0x96, 0x05, 0x9a, 0x07, 0x12, 0x80, 0xe2, 0xeb, 0x27, 0xb2, 0x75,
    0x09, 0x83, 0x2c, 0x1a, 0x1b, 0x6e, 0x5a, 0xa0, 0x52, 0x3b, 0xd6, 0xb3, 0x29, 0xe3, 0x2f, 0x84,
    0x53, 0xd1, 0x00, 0xed, 0x20, 0xfc, 0xb1, 0x5b, 0x6a, 0xcb, 0xbe, 0x39, 0x4a, 0x4c, 0x58, 0xcf,
    0xd0, 0xef, 0xaa, 0xfb, 0x43, 0x4d, 0x33, 0x85, 0x45, 0xf9, 0x02, 0x7f, 0x50, 0x3c, 0x9f, 0xa8,
    0x51, 0xa3, 0x40, 0x8f, 0x92, 0x9d, 0x38, 0xf5, 0xbc, 0xb6, 0xda, 0x21, 0x10, 0xff, 0xf3, 0xd2,
    0xcd, 0x0c, 0x13, 0xec, 0x5f, 0x97, 0x44, 0x17, 0xc4, 0xa7, 0x7e, 0x3d, 0x64, 0x5d, 0x19, 0x73,
    0x60, 0x81, 0x4f, 0xdc, 0x22, 0x2a, 0x90, 0x88, 0x46, 0xee, 0xb8, 0x14, 0xde, 0x5e, 0x0b, 0xdb,
    0xe0, 0x32, 0x3a, 0x0a, 0x49, 0x06, 0x24, 0x5c, 0xc2, 0xd3, 0xac, 0x62, 0x91, 0x95, 0xe4, 0x79,
    0xe7, 0xc8, 0x37, 0x6d, 0x8d, 0xd5, 0x4e, 0xa9, 0x6c, 0x56, 0xf4, 0xea, 0x65, 0x7a, 0xae, 0x08,
    0xba, 0x78, 0x25, 0x2e, 0x1c, 0xa6, 0xb4, 0xc6, 0xe8, 0xdd, 0x74, 0x1f, 0x4b, 0xbd, 0x8b, 0x8a,
    0x70, 0x3e, 0xb5, 0x66, 0x48, 0x03, 0xf6, 0x0e, 0x61, 0x35, 0x57, 0xb9, 0x86, 0xc1, 0x1d, 0x9e,
    0xe1, 0xf8, 0x98, 0x11, 0x69, 0xd9, 0x8e, 0x94, 0x9b, 0x1e, 0x87, 0xe9, 0xce, 0x55, 0x28, 0xdf,
    0x8c, 0xa1, 0x89, 0x0d, 0xbf, 0xe6, 0x42, 0x68, 0x41, 0x99, 0x2d, 0x0f, 0xb0, 0x54, 0xbb, 0x16,
];

const RCON: [u8; 11] = [0x00, 0x01, 0x02, 0x04, 0x08, 0x10, 0x20, 0x40, 0x80, 0x1b, 0x36];

/// FIPS 180-4 SHA-256 Round Constants
const SHA256_K: [u32; 64] = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
];

fn sanitize_log(msg: &str) -> String {
    msg.chars()
        .filter(|&c| c != '\r' && c != '\n' && c != '\0')
        .collect()
}

fn log_info(msg: &str) {
    eprintln!("{} {}", LOG_PREFIX, sanitize_log(msg));
}

/// So sánh Constant-Time chống Timing Attack (PHAN 3)
pub fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    let mut diff: u8 = 0;
    for (&x, &y) in a.iter().zip(b.iter()) {
        diff |= x ^ y;
    }
    diff == 0
}

/// Hàm băm SHA-256 chuẩn FIPS 180-4 (Zero-copy slice `&[u8]`)
pub fn sha256(data: &[u8]) -> [u8; 32] {
    let mut h: [u32; 8] = [
        0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
        0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
    ];

    let bit_len = (data.len() as u64) * 8;
    let mut padded = Vec::with_capacity(((data.len() + 9 + 63) / 64) * 64);
    padded.extend_from_slice(data);
    padded.push(0x80);
    while (padded.len() % 64) != 56 {
        padded.push(0x00);
    }
    padded.extend_from_slice(&bit_len.to_be_bytes());

    for chunk in padded.chunks_exact(64) {
        let mut w = [0u32; 64];
        for i in 0..16 {
            w[i] = u32::from_be_bytes([
                chunk[i * 4],
                chunk[i * 4 + 1],
                chunk[i * 4 + 2],
                chunk[i * 4 + 3],
            ]);
        }
        for i in 16..64 {
            let s0 = w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3);
            let s1 = w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16]
                .wrapping_add(s0)
                .wrapping_add(w[i - 7])
                .wrapping_add(s1);
        }

        let mut a = h[0];
        let mut b = h[1];
        let mut c = h[2];
        let mut d = h[3];
        let mut e = h[4];
        let mut f = h[5];
        let mut g = h[6];
        let mut hh = h[7];

        for i in 0..64 {
            let s1 = e.rotate_right(6) ^ e.rotate_right(11) ^ e.rotate_right(25);
            let ch = (e & f) ^ ((!e) & g);
            let temp1 = hh
                .wrapping_add(s1)
                .wrapping_add(ch)
                .wrapping_add(SHA256_K[i])
                .wrapping_add(w[i]);
            let s0 = a.rotate_right(2) ^ a.rotate_right(13) ^ a.rotate_right(22);
            let maj = (a & b) ^ (a & c) ^ (b & c);
            let temp2 = s0.wrapping_add(maj);

            hh = g;
            g = f;
            f = e;
            e = d.wrapping_add(temp1);
            d = c;
            c = b;
            b = a;
            a = temp1.wrapping_add(temp2);
        }

        h[0] = h[0].wrapping_add(a);
        h[1] = h[1].wrapping_add(b);
        h[2] = h[2].wrapping_add(c);
        h[3] = h[3].wrapping_add(d);
        h[4] = h[4].wrapping_add(e);
        h[5] = h[5].wrapping_add(f);
        h[6] = h[6].wrapping_add(g);
        h[7] = h[7].wrapping_add(hh);
    }

    let mut out = [0u8; 32];
    for (i, &val) in h.iter().enumerate() {
        let bytes = val.to_be_bytes();
        out[i * 4..i * 4 + 4].copy_from_slice(&bytes);
    }
    out
}

/// HMAC-SHA256 chuẩn RFC 2104
pub fn hmac_sha256(key: &[u8], message: &[u8]) -> [u8; 32] {
    let mut k_block = [0u8; SHA256_BLOCK_SIZE];
    if key.len() > SHA256_BLOCK_SIZE {
        let hashed = sha256(key);
        k_block[..32].copy_from_slice(&hashed);
    } else {
        k_block[..key.len()].copy_from_slice(key);
    }

    let mut ipad = [0x36u8; SHA256_BLOCK_SIZE];
    let mut opad = [0x5cu8; SHA256_BLOCK_SIZE];
    for i in 0..SHA256_BLOCK_SIZE {
        ipad[i] ^= k_block[i];
        opad[i] ^= k_block[i];
    }

    let mut inner_input = Vec::with_capacity(SHA256_BLOCK_SIZE + message.len());
    inner_input.extend_from_slice(&ipad);
    inner_input.extend_from_slice(message);
    let inner_hash = sha256(&inner_input);

    let mut outer_input = Vec::with_capacity(SHA256_BLOCK_SIZE + 32);
    outer_input.extend_from_slice(&opad);
    outer_input.extend_from_slice(&inner_hash);
    sha256(&outer_input)
}

fn gf_mul2(x: u8) -> u8 {
    if (x & 0x80) != 0 {
        (x << 1) ^ 0x1b
    } else {
        x << 1
    }
}

/// Mở rộng khóa AES-256 (14 vòng -> 240 bytes round keys)
fn aes256_expand_key(key: &[u8; 32]) -> [u8; 240] {
    let mut w = [0u8; 240];
    w[..32].copy_from_slice(key);

    let mut i = 8; // 8 words = 32 bytes
    while i < 60 {
        let mut temp = [
            w[(i - 1) * 4],
            w[(i - 1) * 4 + 1],
            w[(i - 1) * 4 + 2],
            w[(i - 1) * 4 + 3],
        ];
        if i % 8 == 0 {
            temp = [
                AES_SBOX[temp[1] as usize] ^ RCON[i / 8],
                AES_SBOX[temp[2] as usize],
                AES_SBOX[temp[3] as usize],
                AES_SBOX[temp[0] as usize],
            ];
        } else if i % 8 == 4 {
            temp = [
                AES_SBOX[temp[0] as usize],
                AES_SBOX[temp[1] as usize],
                AES_SBOX[temp[2] as usize],
                AES_SBOX[temp[3] as usize],
            ];
        }
        for j in 0..4 {
            w[i * 4 + j] = w[(i - 8) * 4 + j] ^ temp[j];
        }
        i += 1;
    }
    w
}

/// Mã hóa 1 block 16-byte bằng AES-256 chuẩn FIPS 197
fn aes256_encrypt_block(block: &[u8; 16], round_keys: &[u8; 240]) -> [u8; 16] {
    let mut state = *block;

    // AddRoundKey(0)
    for i in 0..16 {
        state[i] ^= round_keys[i];
    }

    // Rounds 1..13
    for round in 1..14 {
        // SubBytes
        for b in state.iter_mut() {
            *b = AES_SBOX[*b as usize];
        }
        // ShiftRows
        let t1 = state[1];
        state[1] = state[5];
        state[5] = state[9];
        state[9] = state[13];
        state[13] = t1;

        let t2 = state[2];
        let t6 = state[6];
        state[2] = state[10];
        state[6] = state[14];
        state[10] = t2;
        state[14] = t6;

        let t3 = state[15];
        state[15] = state[11];
        state[11] = state[7];
        state[7] = state[3];
        state[3] = t3;

        // MixColumns
        for col in 0..4 {
            let idx = col * 4;
            let a0 = state[idx];
            let a1 = state[idx + 1];
            let a2 = state[idx + 2];
            let a3 = state[idx + 3];
            let tm = a0 ^ a1 ^ a2 ^ a3;
            state[idx] ^= tm ^ gf_mul2(a0 ^ a1);
            state[idx + 1] ^= tm ^ gf_mul2(a1 ^ a2);
            state[idx + 2] ^= tm ^ gf_mul2(a2 ^ a3);
            state[idx + 3] ^= tm ^ gf_mul2(a3 ^ a0);
        }

        // AddRoundKey
        let rk_offset = round * 16;
        for i in 0..16 {
            state[i] ^= round_keys[rk_offset + i];
        }
    }

    // Final Round 14 (no MixColumns)
    for b in state.iter_mut() {
        *b = AES_SBOX[*b as usize];
    }
    let t1 = state[1];
    state[1] = state[5];
    state[5] = state[9];
    state[9] = state[13];
    state[13] = t1;

    let t2 = state[2];
    let t6 = state[6];
    state[2] = state[10];
    state[6] = state[14];
    state[10] = t2;
    state[14] = t6;

    let t3 = state[15];
    state[15] = state[11];
    state[11] = state[7];
    state[7] = state[3];
    state[3] = t3;

    let rk_offset = 14 * 16;
    for i in 0..16 {
        state[i] ^= round_keys[rk_offset + i];
    }
    state
}

fn aes256_ctr_crypt(data: &[u8], key: &[u8; 32], nonce: &[u8; 12]) -> Vec<u8> {
    let round_keys = aes256_expand_key(key);
    let mut out = Vec::with_capacity(data.len());
    let mut counter: u32 = 1;

    for chunk in data.chunks(AES_BLOCK_SIZE) {
        let mut ctr_block = [0u8; 16];
        ctr_block[..12].copy_from_slice(nonce);
        ctr_block[12..16].copy_from_slice(&counter.to_be_bytes());
        let keystream = aes256_encrypt_block(&ctr_block, &round_keys);
        for (i, &b) in chunk.iter().enumerate() {
            out.push(b ^ keystream[i]);
        }
        counter = counter.wrapping_add(1);
    }
    out
}

fn hex_encode(bytes: &[u8]) -> String {
    const HEX_CHARS: &[u8; 16] = b"0123456789abcdef";
    let mut out = String::with_capacity(bytes.len() * 2);
    for &b in bytes {
        out.push(HEX_CHARS[(b >> 4) as usize] as char);
        out.push(HEX_CHARS[(b & 0x0f) as usize] as char);
    }
    out
}

fn hex_decode(hex: &str) -> Result<Vec<u8>, &'static str> {
    if hex.len() % 2 != 0 {
        return Err("Invalid hex length");
    }
    let mut out = Vec::with_capacity(hex.len() / 2);
    let bytes = hex.as_bytes();
    for i in (0..bytes.len()).step_by(2) {
        let hi = match bytes[i] {
            b'0'..=b'9' => bytes[i] - b'0',
            b'a'..=b'f' => bytes[i] - b'a' + 10,
            b'A'..=b'F' => bytes[i] - b'A' + 10,
            _ => return Err("Invalid hex character"),
        };
        let lo = match bytes[i + 1] {
            b'0'..=b'9' => bytes[i + 1] - b'0',
            b'a'..=b'f' => bytes[i + 1] - b'a' + 10,
            b'A'..=b'F' => bytes[i + 1] - b'A' + 10,
            _ => return Err("Invalid hex character"),
        };
        out.push((hi << 4) | lo);
    }
    Ok(out)
}

fn generate_nonce(plaintext: &[u8], master_key: &[u8]) -> [u8; 12] {
    let nanos = match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(d) => d.as_nanos(),
        Err(_) => 0x9e3779b97f4a7c15u128,
    };
    let mut seed = Vec::with_capacity(32 + plaintext.len());
    seed.extend_from_slice(&nanos.to_le_bytes());
    seed.extend_from_slice(master_key);
    seed.extend_from_slice(plaintext);
    let digest = sha256(&seed);
    let mut nonce = [0u8; 12];
    nonce.copy_from_slice(&digest[..12]);
    nonce
}

/// Mã hóa Authenticated AES-256-CTR + HMAC-SHA256
pub fn encrypt_secret_str(plaintext: &str, master_key: &str) -> Result<String, &'static str> {
    if plaintext.is_empty() {
        return Err("Plaintext cannot be empty");
    }
    if master_key.is_empty() {
        return Err("Master key cannot be empty");
    }
    let enc_key = sha256(&[b"ENC:", master_key.as_bytes()].concat());
    let mac_key = sha256(&[b"MAC:", master_key.as_bytes()].concat());
    let nonce = generate_nonce(plaintext.as_bytes(), master_key.as_bytes());
    let ciphertext = aes256_ctr_crypt(plaintext.as_bytes(), &enc_key, &nonce);

    let mut auth_payload = Vec::with_capacity(12 + ciphertext.len());
    auth_payload.extend_from_slice(&nonce);
    auth_payload.extend_from_slice(&ciphertext);
    let tag = hmac_sha256(&mac_key, &auth_payload);

    Ok(format!(
        "v1:{}:{}:{}",
        hex_encode(&nonce),
        hex_encode(&ciphertext),
        hex_encode(&tag)
    ))
}

/// Giải mã Authenticated AES-256-CTR + HMAC-SHA256 (Constant-Time MAC check)
pub fn decrypt_secret_str(encrypted: &str, master_key: &str) -> Result<String, &'static str> {
    let parts: Vec<&str> = encrypted.split(':').collect();
    if parts.len() != 4 || parts[0] != "v1" {
        return Err("Invalid encrypted payload format");
    }
    let nonce_vec = hex_decode(parts[1])?;
    if nonce_vec.len() != 12 {
        return Err("Invalid nonce length");
    }
    let ciphertext = hex_decode(parts[2])?;
    let expected_tag = hex_decode(parts[3])?;
    if expected_tag.len() != 32 {
        return Err("Invalid authentication tag length");
    }

    let enc_key = sha256(&[b"ENC:", master_key.as_bytes()].concat());
    let mac_key = sha256(&[b"MAC:", master_key.as_bytes()].concat());

    let mut auth_payload = Vec::with_capacity(12 + ciphertext.len());
    auth_payload.extend_from_slice(&nonce_vec);
    auth_payload.extend_from_slice(&ciphertext);
    let computed_tag = hmac_sha256(&mac_key, &auth_payload);

    if !constant_time_eq(&computed_tag, &expected_tag) {
        log_info("Authentication tag mismatch during secret decryption");
        return Err("Authentication tag verification failed");
    }

    let mut nonce = [0u8; 12];
    nonce.copy_from_slice(&nonce_vec);
    let plain_bytes = aes256_ctr_crypt(&ciphertext, &enc_key, &nonce);
    String::from_utf8(plain_bytes).map_err(|_| "Decrypted bytes are not valid UTF-8")
}

/// Tạo fingerprint ẩn danh cho Cookie/Token (không bao giờ lộ raw secret)
pub fn compute_fingerprint(secret: &str) -> String {
    let digest = sha256(secret.as_bytes());
    format!("fp_{}", &hex_encode(&digest)[..16])
}

// ============================================================================
// C ABI EXPORTS (FFI) + MEMORY LEAK GUARDS (PHAN 7 BAT BUOC)
// ============================================================================

#[no_mangle]
pub extern "C" fn core_encrypt_secret(
    plaintext_ptr: *const c_char,
    master_key_ptr: *const c_char,
) -> *mut c_char {
    if plaintext_ptr.is_null() || master_key_ptr.is_null() {
        log_info("core_encrypt_secret received null pointer");
        return std::ptr::null_mut();
    }
    let plaintext = unsafe {
        match CStr::from_ptr(plaintext_ptr).to_str() {
            Ok(s) => s,
            Err(_) => return std::ptr::null_mut(),
        }
    };
    let master_key = unsafe {
        match CStr::from_ptr(master_key_ptr).to_str() {
            Ok(s) => s,
            Err(_) => return std::ptr::null_mut(),
        }
    };

    match encrypt_secret_str(plaintext, master_key) {
        Ok(encrypted) => {
            log_info("Encrypted account secret via AES-256-CTR+HMAC-SHA256");
            match CString::new(encrypted) {
                Ok(cstr) => cstr.into_raw(),
                Err(_) => std::ptr::null_mut(),
            }
        }
        Err(e) => {
            log_info(&format!("Encryption failed: {}", e));
            std::ptr::null_mut()
        }
    }
}

#[no_mangle]
pub extern "C" fn core_decrypt_secret(
    encrypted_ptr: *const c_char,
    master_key_ptr: *const c_char,
) -> *mut c_char {
    if encrypted_ptr.is_null() || master_key_ptr.is_null() {
        log_info("core_decrypt_secret received null pointer");
        return std::ptr::null_mut();
    }
    let encrypted = unsafe {
        match CStr::from_ptr(encrypted_ptr).to_str() {
            Ok(s) => s,
            Err(_) => return std::ptr::null_mut(),
        }
    };
    let master_key = unsafe {
        match CStr::from_ptr(master_key_ptr).to_str() {
            Ok(s) => s,
            Err(_) => return std::ptr::null_mut(),
        }
    };

    match decrypt_secret_str(encrypted, master_key) {
        Ok(plain) => match CString::new(plain) {
            Ok(cstr) => cstr.into_raw(),
            Err(_) => std::ptr::null_mut(),
        },
        Err(e) => {
            log_info(&format!("Decryption failed: {}", e));
            std::ptr::null_mut()
        }
    }
}

#[no_mangle]
pub extern "C" fn core_constant_time_compare(a_ptr: *const c_char, b_ptr: *const c_char) -> i32 {
    if a_ptr.is_null() || b_ptr.is_null() {
        return 0;
    }
    let a = unsafe { CStr::from_ptr(a_ptr).to_bytes() };
    let b = unsafe { CStr::from_ptr(b_ptr).to_bytes() };
    if constant_time_eq(a, b) {
        1
    } else {
        0
    }
}

#[no_mangle]
pub extern "C" fn core_compute_token_fingerprint(token_ptr: *const c_char) -> *mut c_char {
    if token_ptr.is_null() {
        return std::ptr::null_mut();
    }
    let token = unsafe {
        match CStr::from_ptr(token_ptr).to_str() {
            Ok(s) => s,
            Err(_) => return std::ptr::null_mut(),
        }
    };
    let fp = compute_fingerprint(token);
    match CString::new(fp) {
        Ok(cstr) => cstr.into_raw(),
        Err(_) => std::ptr::null_mut(),
    }
}

/// FFI Memory Leak Guard (PHAN 7 BAT BUOC): Giải phóng CString cấp phát bởi Rust
#[no_mangle]
pub extern "C" fn free_rust_string(ptr: *mut c_char) {
    if ptr.is_null() {
        return;
    }
    unsafe {
        let _ = CString::from_raw(ptr);
    }
}

/// FFI Memory Leak Guard (PHAN 7 BAT BUOC): Giải phóng Vec<u8> cấp phát bởi Rust
#[no_mangle]
pub extern "C" fn free_rust_bytes(ptr: *mut u8, len: usize, cap: usize) {
    if ptr.is_null() || cap == 0 {
        return;
    }
    unsafe {
        let _ = Vec::from_raw_parts(ptr, len, cap);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_sha256_nist_vector() {
        let digest = sha256(b"abc");
        let hex = hex_encode(&digest);
        assert_eq!(
            hex,
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }

    #[test]
    fn test_encrypt_decrypt_roundtrip() {
        let cookie = "hatch_sess=eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.muse_node_01; hatch_gw=gw_us_east_1";
        let master = "muse_router_vault_key_2026";
        let enc = encrypt_secret_str(cookie, master).expect("Encryption must succeed");
        assert!(enc.starts_with("v1:"));
        let dec = decrypt_secret_str(&enc, master).expect("Decryption must succeed");
        assert_eq!(dec, cookie);
    }

    #[test]
    fn test_tampered_ciphertext_rejected() {
        let cookie = "hatch_sess=secret_cookie_value";
        let master = "muse_router_vault_key_2026";
        let mut enc = encrypt_secret_str(cookie, master).expect("Encryption must succeed");
        // Tamper last char of MAC tag
        let last = enc.pop().unwrap_or('0');
        enc.push(if last == 'a' { 'b' } else { 'a' });
        let res = decrypt_secret_str(&enc, master);
        assert!(res.is_err());
    }

    #[test]
    fn test_constant_time_eq() {
        assert!(constant_time_eq(b"token_12345", b"token_12345"));
        assert!(!constant_time_eq(b"token_12345", b"token_12346"));
        assert!(!constant_time_eq(b"short", b"longer"));
    }
}
