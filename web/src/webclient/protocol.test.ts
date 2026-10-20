import { describe, it, expect } from 'vitest';
import {
  ProtoWriter,
  ProtoReader,
  encodeRelayRequest,
  encodePublicKeyMessage,
  encodeLoginRequest,
  encodeVideoReceivedAck,
  encodeMouseEvent,
  encodeKeyEvent,
  decodePeerMessage,
  decodeRelayResponse,
} from './protocol';

describe('WebClient Protocol', () => {
  it('encodes and decodes zigzag sint32', () => {
    const w = new ProtoWriter();
    w.writeSint32(1, 42);
    w.writeSint32(2, -42);
    w.writeSint32(3, 1080);
    w.writeSint32(4, -1920);

    const r = new ProtoReader(w.finish());
    expect(r.readTag()).toEqual({ field: 1, wireType: 0 });
    expect(r.readSint32()).toBe(42);
    expect(r.readTag()).toEqual({ field: 2, wireType: 0 });
    expect(r.readSint32()).toBe(-42);
    expect(r.readTag()).toEqual({ field: 3, wireType: 0 });
    expect(r.readSint32()).toBe(1080);
    expect(r.readTag()).toEqual({ field: 4, wireType: 0 });
    expect(r.readSint32()).toBe(-1920);
  });

  it('encodes relay request matching RustDesk rendezvous spec', () => {
    const buf = encodeRelayRequest('test-uuid-1234', '103146982', 'test-key-b64');
    const r = new ProtoReader(buf);
    const tag = r.readTag();
    expect(tag?.field).toBe(18); // request_relay field in RendezvousMessage

    const rInner = new ProtoReader(r.readBytes());
    expect(rInner.readTag()?.field).toBe(1); // id
    expect(rInner.readString()).toBe('103146982');
    expect(rInner.readTag()?.field).toBe(2); // uuid
    expect(rInner.readString()).toBe('test-uuid-1234');
    expect(rInner.readTag()?.field).toBe(5); // secure
    expect(rInner.readBool()).toBe(true);
    expect(rInner.readTag()?.field).toBe(6); // licence_key
    expect(rInner.readString()).toBe('test-key-b64');
  });

  it('decodes relay response', () => {
    const wResp = new ProtoWriter();
    wResp.writeBytes(1, new Uint8Array([1, 2, 3, 4]));
    wResp.writeString(2, 'uuid-abc');
    wResp.writeString(3, 'relay.example.com:21117');

    const wMsg = new ProtoWriter();
    wMsg.writeMessage(19, wResp.finish());

    const decoded = decodeRelayResponse(wMsg.finish());
    expect(decoded).toBeTruthy();
    expect(decoded?.uuid).toBe('uuid-abc');
    expect(decoded?.relayServer).toBe('relay.example.com:21117');
    expect(decoded?.socketAddr).toEqual(new Uint8Array([1, 2, 3, 4]));
  });

  it('encodes public key message with field 4', () => {
    const asym = new Uint8Array(32).fill(0xaa);
    const sym = new Uint8Array(48).fill(0xbb);
    const buf = encodePublicKeyMessage(asym, sym);

    const r = new ProtoReader(buf);
    expect(r.readTag()?.field).toBe(4); // public_key in Message
    const rInner = new ProtoReader(r.readBytes());
    expect(rInner.readTag()?.field).toBe(1);
    expect(rInner.readBytes()).toEqual(asym);
    expect(rInner.readTag()?.field).toBe(2);
    expect(rInner.readBytes()).toEqual(sym);
  });

  it('encodes login request with field 7', () => {
    const hash = new Uint8Array(32).fill(0x11);
    const buf = encodeLoginRequest('admin', hash, 'web-client', 'Web Browser');

    const r = new ProtoReader(buf);
    expect(r.readTag()?.field).toBe(7); // login_request in Message
    const rInner = new ProtoReader(r.readBytes());
    expect(rInner.readTag()?.field).toBe(1);
    expect(rInner.readString()).toBe('admin');
    expect(rInner.readTag()?.field).toBe(2);
    expect(rInner.readBytes()).toEqual(hash);
    expect(rInner.readTag()?.field).toBe(4);
    expect(rInner.readString()).toBe('web-client');
    expect(rInner.readTag()?.field).toBe(5);
    expect(rInner.readString()).toBe('Web Browser');
    expect(rInner.readTag()?.field).toBe(11);
    expect(rInner.readString()).toBe('1.4.0');
  });

  it('encodes video received ack under Misc (field 19 -> field 12)', () => {
    const buf = encodeVideoReceivedAck();
    const r = new ProtoReader(buf);
    expect(r.readTag()?.field).toBe(19); // misc
    const rInner = new ProtoReader(r.readBytes());
    expect(rInner.readTag()?.field).toBe(12); // video_received
    expect(rInner.readBool()).toBe(true);
  });

  it('encodes mouse and key events correctly', () => {
    const mouseBuf = encodeMouseEvent(1, 100, 200);
    const rm = new ProtoReader(mouseBuf);
    expect(rm.readTag()?.field).toBe(10); // mouse_event

    const keyBuf = encodeKeyEvent(0, true, true, false, false, false, 65);
    const rk = new ProtoReader(keyBuf);
    expect(rk.readTag()?.field).toBe(15); // key_event
  });

  it('decodes hash and video frames from peer message', () => {
    // Hash message: field 9
    const wHash = new ProtoWriter();
    wHash.writeString(1, 'mysalt');
    wHash.writeString(2, 'mychallenge');
    const wMsgHash = new ProtoWriter();
    wMsgHash.writeMessage(9, wHash.finish());

    const decHash = decodePeerMessage(wMsgHash.finish());
    expect(decHash).toEqual({ type: 'hash', salt: 'mysalt', challenge: 'mychallenge' });

    // VideoFrame message: field 6
    // EncodedVideoFrames: repeated EncodedVideoFrame (field 1)
    const wEvf = new ProtoWriter();
    wEvf.writeBytes(1, new Uint8Array([0xde, 0xad, 0xbe, 0xef]));
    wEvf.writeBool(2, true); // keyframe
    wEvf.writeInt32(3, 1000); // pts

    const wEvfs = new ProtoWriter();
    wEvfs.writeMessage(1, wEvf.finish()); // frames = 1

    const wVf = new ProtoWriter();
    wVf.writeMessage(6, wEvfs.finish()); // vp9s = 6

    const wMsgVf = new ProtoWriter();
    wMsgVf.writeMessage(6, wVf.finish()); // video_frame = 6

    const decVf = decodePeerMessage(wMsgVf.finish());
    expect(decVf).toEqual({
      type: 'video_frame',
      codec: 'vp9',
      data: new Uint8Array([0xde, 0xad, 0xbe, 0xef]),
      key: true,
      pts: 1000n,
    });
  });
});
