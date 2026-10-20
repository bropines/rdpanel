/**
 * Zero-dependency Protobuf wire encoder and decoder
 * tailored specifically for RustDesk Rendezvous and Peer session protocol.
 */

import type { CodecType } from './types';

const utf8Encoder = new TextEncoder();
const utf8Decoder = new TextDecoder();

// Protobuf Wire Types
export const WIRE_VARINT = 0;
export const WIRE_FIXED64 = 1;
export const WIRE_LENGTH_DELIMITED = 2;
export const WIRE_FIXED32 = 5;

export class ProtoWriter {
  private chunks: Uint8Array[] = [];
  private totalLength = 0;

  writeTag(fieldNumber: number, wireType: number): void {
    this.writeVarint((fieldNumber << 3) | wireType);
  }

  writeVarint(val: number | bigint): void {
    let v = BigInt(val);
    const buf: number[] = [];
    while (v >= 0x80n) {
      buf.push(Number((v & 0x7fn) | 0x80n));
      v >>= 7n;
    }
    buf.push(Number(v));
    const chunk = new Uint8Array(buf);
    this.chunks.push(chunk);
    this.totalLength += chunk.length;
  }

  writeInt32(fieldNumber: number, val: number): void {
    if (val === 0) return;
    this.writeTag(fieldNumber, WIRE_VARINT);
    this.writeVarint(val < 0 ? BigInt.asUintN(64, BigInt(val)) : BigInt(val));
  }

  writeBool(fieldNumber: number, val: boolean): void {
    if (!val) return;
    this.writeTag(fieldNumber, WIRE_VARINT);
    this.writeVarint(1);
  }

  writeString(fieldNumber: number, val: string): void {
    if (!val) return;
    const bytes = utf8Encoder.encode(val);
    this.writeBytes(fieldNumber, bytes);
  }

  writeBytes(fieldNumber: number, bytes: Uint8Array): void {
    if (!bytes || bytes.length === 0) return;
    this.writeTag(fieldNumber, WIRE_LENGTH_DELIMITED);
    this.writeVarint(bytes.length);
    this.chunks.push(bytes);
    this.totalLength += bytes.length;
  }

  writeMessage(fieldNumber: number, msgBytes: Uint8Array): void {
    this.writeBytes(fieldNumber, msgBytes);
  }

  finish(): Uint8Array {
    const out = new Uint8Array(this.totalLength);
    let offset = 0;
    for (const chunk of this.chunks) {
      out.set(chunk, offset);
      offset += chunk.length;
    }
    return out;
  }
}

export class ProtoReader {
  private pos = 0;
  constructor(private buf: Uint8Array) {}

  get remaining(): number {
    return this.buf.length - this.pos;
  }

  hasMore(): boolean {
    return this.pos < this.buf.length;
  }

  readTag(): { field: number; wireType: number } | null {
    if (!this.hasMore()) return null;
    const tag = Number(this.readVarint());
    return { field: tag >>> 3, wireType: tag & 0x7 };
  }

  readVarint(): bigint {
    let res = 0n;
    let shift = 0n;
    while (this.pos < this.buf.length) {
      const b = this.buf[this.pos++];
      if (b === undefined) break;
      res |= BigInt(b & 0x7f) << shift;
      if ((b & 0x80) === 0) return res;
      shift += 7n;
    }
    throw new Error('proto: unexpected EOF in varint');
  }

  readInt32(): number {
    const v = this.readVarint();
    return Number(BigInt.asIntN(32, v));
  }

  readBool(): boolean {
    return this.readVarint() !== 0n;
  }

  readBytes(): Uint8Array<ArrayBuffer> {
    const len = Number(this.readVarint());
    if (this.pos + len > this.buf.length) {
      throw new Error(`proto: unexpected EOF reading bytes (len ${len})`);
    }
    const res = new Uint8Array(new ArrayBuffer(len));
    res.set(this.buf.subarray(this.pos, this.pos + len));
    this.pos += len;
    return res;
  }

  readString(): string {
    return utf8Decoder.decode(this.readBytes());
  }

  skip(wireType: number): void {
    switch (wireType) {
      case WIRE_VARINT:
        this.readVarint();
        break;
      case WIRE_FIXED64:
        this.pos += 8;
        break;
      case WIRE_LENGTH_DELIMITED: {
        const len = Number(this.readVarint());
        this.pos += len;
        break;
      }
      case WIRE_FIXED32:
        this.pos += 4;
        break;
      default:
        throw new Error(`proto: unknown wire type ${wireType}`);
    }
  }
}

// ============================================================================
// Specialized RustDesk Protocol Messages
// ============================================================================

export interface IdPkData {
  id: string;
  pk: Uint8Array;
}

export function decodeIdPk(buf: Uint8Array): IdPkData {
  const r = new ProtoReader(buf);
  let id = '';
  let pk = new Uint8Array(0);
  while (r.hasMore()) {
    const tag = r.readTag();
    if (!tag) break;
    if (tag.field === 1) id = r.readString();
    else if (tag.field === 2) pk = r.readBytes();
    else r.skip(tag.wireType);
  }
  return { id, pk };
}

export interface RelayResponseData {
  socketAddr: Uint8Array;
  uuid: string;
  relayServer: string;
  pk: Uint8Array;
  refuseReason?: string;
}

export function encodeRequestRelay(id: string, uuid: string, relayServer?: string): Uint8Array {
  const wReq = new ProtoWriter();
  wReq.writeString(1, id);
  wReq.writeString(2, uuid);
  if (relayServer) wReq.writeString(4, relayServer);
  wReq.writeBool(5, true); // secure = true

  const wMsg = new ProtoWriter();
  wMsg.writeMessage(18, wReq.finish()); // field 18 = request_relay
  return wMsg.finish();
}

export function encodeRelayRequest(uuid: string, id: string): Uint8Array {
  const wReq = new ProtoWriter();
  wReq.writeString(1, uuid);
  wReq.writeString(2, id);
  wReq.writeBool(4, true); // secure = true

  const wMsg = new ProtoWriter();
  wMsg.writeMessage(10, wReq.finish()); // field 10 = relay_request
  return wMsg.finish();
}

export function decodeRelayResponse(buf: Uint8Array): RelayResponseData | null {
  const r = new ProtoReader(buf);
  while (r.hasMore()) {
    const tag = r.readTag();
    if (!tag) break;
    if (tag.field === 19) { // relay_response
      const rInner = new ProtoReader(r.readBytes());
      let socketAddr = new Uint8Array(0);
      let uuid = '';
      let relayServer = '';
      let pk = new Uint8Array(0);
      let refuseReason = '';

      while (rInner.hasMore()) {
        const t = rInner.readTag();
        if (!t) break;
        if (t.field === 1) socketAddr = rInner.readBytes();
        else if (t.field === 2) uuid = rInner.readString();
        else if (t.field === 3) relayServer = rInner.readString();
        else if (t.field === 4) pk = rInner.readBytes();
        else if (t.field === 5) refuseReason = rInner.readString();
        else rInner.skip(t.wireType);
      }
      return { socketAddr, uuid, relayServer, pk, refuseReason };
    }
    r.skip(tag.wireType);
  }
  return null;
}

export function encodePublicKeyMessage(asymmetricVal: Uint8Array, symmetricVal: Uint8Array): Uint8Array {
  const wPk = new ProtoWriter();
  wPk.writeBytes(1, asymmetricVal);
  wPk.writeBytes(2, symmetricVal);

  const wMsg = new ProtoWriter();
  wMsg.writeMessage(2, wPk.finish()); // field 2 = public_key
  return wMsg.finish();
}

export function encodeLoginRequest(username: string, passwordHash: Uint8Array, myId: string, myName: string): Uint8Array {
  const wReq = new ProtoWriter();
  wReq.writeString(1, username);
  wReq.writeBytes(2, passwordHash);
  wReq.writeString(3, myId);
  wReq.writeString(4, myName);
  wReq.writeString(5, '1.4.0');

  const wMsg = new ProtoWriter();
  wMsg.writeMessage(11, wReq.finish()); // field 11 = login_request
  return wMsg.finish();
}

export function encodeVideoReceivedAck(): Uint8Array {
  const wMsg = new ProtoWriter();
  wMsg.writeMessage(30, new Uint8Array(0)); // field 30 = video_received (empty)
  return wMsg.finish();
}

export function encodeMouseEvent(mask: number, x: number, y: number, wheelDelta = 0): Uint8Array {
  const wEv = new ProtoWriter();
  wEv.writeInt32(1, mask);
  wEv.writeInt32(2, x);
  wEv.writeInt32(3, y);
  if (wheelDelta !== 0) wEv.writeInt32(4, wheelDelta);

  const wMsg = new ProtoWriter();
  wMsg.writeMessage(20, wEv.finish()); // field 20 = mouse_event
  return wMsg.finish();
}

export function encodeKeyEvent(keyCode: number, down: boolean, ctrl = false, alt = false, shift = false, command = false, chr = 0): Uint8Array {
  const wEv = new ProtoWriter();
  wEv.writeBool(1, down);
  if (alt) wEv.writeBool(2, true);
  if (ctrl) wEv.writeBool(3, true);
  if (shift) wEv.writeBool(4, true);
  if (command) wEv.writeBool(5, true);
  if (chr !== 0) wEv.writeInt32(6, chr);
  if (keyCode !== 0) wEv.writeInt32(7, keyCode);

  const wMsg = new ProtoWriter();
  wMsg.writeMessage(21, wEv.finish()); // field 21 = key_event
  return wMsg.finish();
}

export type DecodedPeerMessage =
  | { type: 'signed_id'; signedId: Uint8Array }
  | { type: 'hash'; salt: string; challenge: string }
  | { type: 'login_response'; error?: string; peerInfo?: Record<string, unknown> }
  | { type: 'video_frame'; codec: 'vp8' | 'vp9' | 'h264' | 'av1'; data: Uint8Array; key: boolean; pts: bigint }
  | { type: 'other'; field: number };

export function decodePeerMessage(buf: Uint8Array): DecodedPeerMessage | null {
  const r = new ProtoReader(buf);
  const tag = r.readTag();
  if (!tag) return null;

  switch (tag.field) {
    case 1: { // signed_id
      const rInner = new ProtoReader(r.readBytes());
      let signedId = new Uint8Array(0);
      while (rInner.hasMore()) {
        const t = rInner.readTag();
        if (!t) break;
        if (t.field === 1) signedId = rInner.readBytes();
        else rInner.skip(t.wireType);
      }
      return { type: 'signed_id', signedId };
    }
    case 10: { // hash
      const rInner = new ProtoReader(r.readBytes());
      let salt = '';
      let challenge = '';
      while (rInner.hasMore()) {
        const t = rInner.readTag();
        if (!t) break;
        if (t.field === 1) salt = rInner.readString();
        else if (t.field === 2) challenge = rInner.readString();
        else rInner.skip(t.wireType);
      }
      return { type: 'hash', salt, challenge };
    }
    case 12: { // login_response
      const rInner = new ProtoReader(r.readBytes());
      let error = '';
      while (rInner.hasMore()) {
        const t = rInner.readTag();
        if (!t) break;
        if (t.field === 1) error = rInner.readString();
        else rInner.skip(t.wireType);
      }
      return { type: 'login_response', error: error || undefined };
    }
    case 14: { // video_frame
      const rInner = new ProtoReader(r.readBytes());
      let codec: CodecType = 'vp9';
      let frameBytes = new Uint8Array(0);

      while (rInner.hasMore()) {
        const t = rInner.readTag();
        if (!t) break;
        if (t.field === 1) { codec = 'vp9'; frameBytes = rInner.readBytes(); }
        else if (t.field === 2) { codec = 'vp8'; frameBytes = rInner.readBytes(); }
        else if (t.field === 3) { codec = 'h264'; frameBytes = rInner.readBytes(); }
        else if (t.field === 5) { codec = 'av1'; frameBytes = rInner.readBytes(); }
        else rInner.skip(t.wireType);
      }

      if (frameBytes.length > 0) {
        const rFrame = new ProtoReader(frameBytes);
        let data = new Uint8Array(0);
        let pts = 0n;
        let key = false;
        while (rFrame.hasMore()) {
          const tf = rFrame.readTag();
          if (!tf) break;
          if (tf.field === 1) data = rFrame.readBytes();
          else if (tf.field === 2) pts = rFrame.readVarint();
          else if (tf.field === 3) key = rFrame.readBool();
          else rFrame.skip(tf.wireType);
        }
        return { type: 'video_frame', codec, data, key, pts };
      }
      break;
    }
    default:
      return { type: 'other', field: tag.field };
  }
  return null;
}
