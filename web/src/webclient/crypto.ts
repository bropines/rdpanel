/**
 * Web Client Cryptography Layer.
 * Implements NaCl crypto_box, crypto_secretbox, and SHA-256 hashing
 * for RustDesk end-to-end handshake and stream encryption.
 */

import nacl from 'tweetnacl';

const utf8Encoder = new TextEncoder();
const ZERO_NONCE_24 = new Uint8Array(24);

export function decodeB64(s: string): Uint8Array {
  const binary = atob(s.trim());
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

export function encodeB64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i++) {
    const b = bytes[i];
    if (b !== undefined) {
      binary += String.fromCharCode(b);
    }
  }
  return btoa(binary);
}

/**
 * Opens an Ed25519 signed message: signed = signature (64 bytes) || message.
 * Returns the unverified message bytes if the signature is valid, or throws.
 */
export function signOpen(signed: Uint8Array, ed25519Pk: Uint8Array): Uint8Array {
  const opened = nacl.sign.open(signed, ed25519Pk);
  if (!opened) {
    throw new Error('crypto: signature verification failed');
  }
  return opened;
}

/**
 * Seals a fresh 32-byte symmetric key using an ephemeral Curve25519 keypair
 * under an all-zero 24-byte nonce.
 */
export function sealSymmetricKey(theirBoxPk: Uint8Array): {
  ourBoxPk: Uint8Array;
  sealed: Uint8Array;
  key: Uint8Array;
} {
  const key = nacl.randomBytes(32);
  const eph = nacl.box.keyPair();
  const sealed = nacl.box(key, ZERO_NONCE_24, theirBoxPk, eph.secretKey);
  return { ourBoxPk: eph.publicKey, sealed, key };
}

/**
 * Builds a 24-byte nonce with the sequence number as an 8-byte little-endian uint at [0..8].
 */
export function buildNonce(seq: bigint): Uint8Array {
  const n = new Uint8Array(24);
  const dv = new DataView(n.buffer);
  dv.setBigUint64(0, seq, true);
  return n;
}

/**
 * Symmetric stream cipher maintaining separate monotonic sequence counters for sending and receiving.
 */
export class StreamCipher {
  private sendSeq = 0n;
  private recvSeq = 0n;
  private readonly key: Uint8Array;

  constructor(key: Uint8Array) {
    this.key = key;
  }

  seal(pt: Uint8Array): Uint8Array {
    this.sendSeq++;
    const nonce = buildNonce(this.sendSeq);
    return nacl.secretbox(pt, nonce, this.key);
  }

  encrypt(pt: Uint8Array): Uint8Array {
    return this.seal(pt);
  }

  open(ct: Uint8Array): Uint8Array {
    // RustDesk raw bypass for 1-byte control frames
    if (ct.length <= 1) {
      return ct;
    }
    this.recvSeq++;
    const nonce = buildNonce(this.recvSeq);
    const opened = nacl.secretbox.open(ct, nonce, this.key);
    if (!opened) {
      throw new Error(`crypto: secretbox decryption failed at recvSeq=${this.recvSeq}`);
    }
    return opened;
  }

  decrypt(ct: Uint8Array): Uint8Array {
    return this.open(ct);
  }
}

/**
 * Computes SHA-256 using browser WebCrypto.
 */
export async function sha256(data: Uint8Array): Promise<Uint8Array> {
  const copy = new Uint8Array(data.byteLength);
  copy.set(data);
  const hashBuf = await crypto.subtle.digest('SHA-256', copy.buffer as ArrayBuffer);
  return new Uint8Array(hashBuf);
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const c = new Uint8Array(a.length + b.length);
  c.set(a, 0);
  c.set(b, a.length);
  return c;
}

/**
 * Computes login password hash:
 * h1 = SHA256(utf8(password) + utf8(salt))
 * hash = SHA256(h1 + utf8(challenge))
 */
export async function loginPasswordHash(
  password: string,
  salt: string,
  challenge: string,
): Promise<Uint8Array> {
  if (!password) {
    return new Uint8Array(0);
  }
  const h1 = await sha256(concat(utf8Encoder.encode(password), utf8Encoder.encode(salt)));
  return sha256(concat(h1, utf8Encoder.encode(challenge)));
}
