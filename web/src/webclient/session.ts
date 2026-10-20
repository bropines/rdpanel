/**
 * Web Client Session Orchestrator.
 * Manages the full lifecycle of a browser remote desktop connection:
 * Rendezvous (hbbs) -> Relay pairing (hbbr) -> E2E Crypto Handshake ->
 * Authentication -> WebCodecs Video Streaming -> Mouse/Keyboard Input Forwarding.
 */

import { StreamCipher, sealSymmetricKey, loginPasswordHash } from './crypto';
import { VideoStreamDecoder } from './decoder';
import {
  encodeRequestRelay,
  decodeRelayResponse,
  encodeRelayRequest,
  encodePublicKeyMessage,
  encodeLoginRequest,
  encodeVideoReceivedAck,
  encodeMouseEvent,
  encodeKeyEvent,
  decodePeerMessage,
} from './protocol';
import type {
  WebClientConfig,
  SessionState,
  WebClientEventListener,
  WebClientEvent,
  VideoStats,
  CodecType,
} from './types';

export class WebClientSession {
  private config: WebClientConfig;
  private canvas: HTMLCanvasElement;
  private state: SessionState = 'idle';
  private listeners = new Set<WebClientEventListener>();

  private wsId: WebSocket | null = null;
  private wsRelay: WebSocket | null = null;
  private cipher: StreamCipher | null = null;
  private decoder: VideoStreamDecoder | null = null;

  private sessionUuid = '';
  private authSalt = '';
  private authChallenge = '';
  private pendingPasswordResolve: ((pwd: string) => void) | null = null;

  // Remote screen dimensions
  private remoteWidth = 1920;
  private remoteHeight = 1080;

  constructor(config: WebClientConfig, canvas: HTMLCanvasElement) {
    this.config = config;
    this.canvas = canvas;
  }

  on(listener: WebClientEventListener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private emit(event: WebClientEvent): void {
    if (event.type === 'state') {
      this.state = event.state;
    }
    for (const listener of this.listeners) {
      try {
        listener(event);
      } catch (e) {
        console.error('WebClient event listener error:', e);
      }
    }
  }

  getState(): SessionState {
    return this.state;
  }

  async connect(): Promise<void> {
    if (this.state !== 'idle' && this.state !== 'closed' && this.state !== 'error') {
      return;
    }

    try {
      this.initDecoder();
      await this.connectRendezvous();
    } catch (err) {
      this.handleError(err instanceof Error ? err.message : String(err));
    }
  }

  private initDecoder(): void {
    this.decoder = new VideoStreamDecoder({
      canvas: this.canvas,
      onResolutionChange: (w, h) => {
        this.remoteWidth = w;
        this.remoteHeight = h;
        this.emit({ type: 'resolution', width: w, height: h });
      },
      onStats: (stats: VideoStats) => {
        this.emit({ type: 'stats', stats });
      },
      onError: (err) => {
        console.warn('Decoder warning:', err);
      },
    });
  }

  private connectRendezvous(): Promise<void> {
    return new Promise((resolve, reject) => {
      this.emit({ type: 'state', state: 'connecting_rendezvous' });

      let ws: WebSocket;
      try {
        ws = new WebSocket(this.config.wsIdUrl);
      } catch (e) {
        reject(new Error(`Failed to create WebSocket to rendezvous: ${e}`));
        return;
      }

      this.wsId = ws;
      ws.binaryType = 'arraybuffer';

      const timeout = setTimeout(() => {
        if (ws.readyState !== WebSocket.OPEN) {
          ws.close();
          reject(new Error('Rendezvous connection timeout (10s)'));
        }
      }, 10000);

      ws.onopen = () => {
        clearTimeout(timeout);
        this.requestRelay();
      };

      ws.onmessage = async (evt: MessageEvent) => {
        try {
          const buf = new Uint8Array(evt.data as ArrayBuffer);
          const resp = decodeRelayResponse(buf);

          if (resp) {
            clearTimeout(timeout);
            ws.close();
            this.wsId = null;

            if (resp.refuseReason) {
              reject(new Error(`Relay refused: ${resp.refuseReason}`));
              return;
            }

            const relayServer = resp.relayServer || '';
            const uuid = resp.uuid || this.sessionUuid;
            await this.connectRelay(relayServer, uuid);
            resolve();
          }
        } catch (e) {
          reject(e);
        }
      };

      ws.onerror = (e) => {
        clearTimeout(timeout);
        reject(new Error(`WebSocket error connecting to rendezvous: ${JSON.stringify(e)}`));
      };

      ws.onclose = () => {
        clearTimeout(timeout);
      };
    });
  }

  private requestRelay(): void {
    if (!this.wsId || this.wsId.readyState !== WebSocket.OPEN) return;
    this.emit({ type: 'state', state: 'requesting_relay' });

    this.sessionUuid = crypto.randomUUID ? crypto.randomUUID() : String(Date.now());
    const req = encodeRequestRelay(this.config.peerId, this.sessionUuid);
    this.wsId.send(req);
  }

  private connectRelay(relayServerFromHbbs: string, uuid: string): Promise<void> {
    return new Promise((resolve, reject) => {
      this.emit({ type: 'state', state: 'connecting_relay' });

      let wsUrl = this.config.wsRelayUrl;
      if (!wsUrl && relayServerFromHbbs) {
        const host = relayServerFromHbbs.split(':')[0] || window.location.hostname;
        const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        wsUrl = `${proto}//${host}:21119`;
      }

      let ws: WebSocket;
      try {
        ws = new WebSocket(wsUrl);
      } catch (e) {
        reject(new Error(`Failed to create WebSocket to relay: ${e}`));
        return;
      }

      this.wsRelay = ws;
      ws.binaryType = 'arraybuffer';

      const timeout = setTimeout(() => {
        if (ws.readyState !== WebSocket.OPEN) {
          ws.close();
          reject(new Error('Relay connection timeout (15s)'));
        }
      }, 15000);

      ws.onopen = () => {
        clearTimeout(timeout);
        this.emit({ type: 'state', state: 'pairing_relay' });
        // Send pairing RelayRequest
        const req = encodeRelayRequest(uuid, this.config.peerId);
        ws.send(req);
      };

      ws.onmessage = (evt: MessageEvent) => {
        void this.handleRelayMessage(new Uint8Array(evt.data as ArrayBuffer));
      };

      ws.onerror = (e) => {
        clearTimeout(timeout);
        reject(new Error(`Relay WebSocket error: ${JSON.stringify(e)}`));
      };

      ws.onclose = (evt) => {
        clearTimeout(timeout);
        if (this.state !== 'closed' && this.state !== 'error') {
          this.emit({ type: 'state', state: 'closed', detail: evt.reason || 'Relay connection closed' });
        }
      };

      resolve();
    });
  }

  private async handleRelayMessage(raw: Uint8Array): Promise<void> {
    try {
      if (!this.cipher) {
        const msg = decodePeerMessage(raw);
        if (!msg) return;

        if (msg.type === 'signed_id') {
          await this.handlePeerSignedId(msg.signedId);
          return;
        }

        if (msg.type === 'hash') {
          this.authSalt = msg.salt;
          this.authChallenge = msg.challenge;
          await this.authenticate();
          return;
        }
      } else {
        const decrypted = this.cipher.decrypt(raw);
        const msg = decodePeerMessage(decrypted);
        if (!msg) return;

        switch (msg.type) {
          case 'login_response':
            if (msg.error) {
              this.handleError(`Authentication failed: ${msg.error}`);
            } else {
              this.emit({ type: 'state', state: 'streaming' });
            }
            break;

          case 'video_frame':
            this.handleVideoFrame(msg.codec, msg.data, msg.key);
            break;

          case 'hash':
            this.authSalt = msg.salt;
            this.authChallenge = msg.challenge;
            await this.authenticate();
            break;

          default:
            break;
        }
      }
    } catch (err) {
      console.error('Error handling peer message:', err);
    }
  }

  private async handlePeerSignedId(signedId: Uint8Array): Promise<void> {
    this.emit({ type: 'state', state: 'handshaking' });

    let peerCurvePk = signedId;
    if (signedId.length > 32) {
      peerCurvePk = signedId.subarray(signedId.length - 32);
    }

    const { ourBoxPk, sealed, key } = sealSymmetricKey(peerCurvePk);
    this.cipher = new StreamCipher(key);

    const reply = encodePublicKeyMessage(ourBoxPk, sealed);
    this.sendRaw(reply);
  }

  private async authenticate(): Promise<void> {
    this.emit({ type: 'state', state: 'authenticating' });

    let password = this.config.password;
    if (!password) {
      this.emit({ type: 'need_password' });
      password = await new Promise<string>((res) => {
        this.pendingPasswordResolve = res;
      });
    }

    const passHash = await loginPasswordHash(password, this.authSalt, this.authChallenge);
    const loginReq = encodeLoginRequest(
      this.config.myName || 'WebClient',
      passHash,
      this.config.myId || 'web',
      this.config.myName || 'WebClient',
    );

    this.sendEncrypted(loginReq);
  }

  submitPassword(password: string): void {
    if (this.pendingPasswordResolve) {
      const resolve = this.pendingPasswordResolve;
      this.pendingPasswordResolve = null;
      resolve(password);
    }
  }

  private handleVideoFrame(codec: CodecType, data: Uint8Array, key: boolean): void {
    if (!this.decoder) return;

    // Send ACK back to the peer so it sends the next frame
    const ack = encodeVideoReceivedAck();
    this.sendEncrypted(ack);

    this.decoder.decodeChunk(codec, data, key);
  }

  private sendRaw(data: Uint8Array): void {
    if (this.wsRelay && this.wsRelay.readyState === WebSocket.OPEN) {
      this.wsRelay.send(data);
    }
  }

  private sendEncrypted(data: Uint8Array): void {
    if (!this.cipher) {
      this.sendRaw(data);
      return;
    }
    const encrypted = this.cipher.encrypt(data);
    this.sendRaw(encrypted);
  }

  // --- Input forwarding methods ---

  sendMouseMove(clientX: number, clientY: number): void {
    if (this.state !== 'streaming') return;
    const rect = this.canvas.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) return;

    const normX = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    const normY = Math.max(0, Math.min(1, (clientY - rect.top) / rect.height));

    const x = Math.round(normX * this.remoteWidth);
    const y = Math.round(normY * this.remoteHeight);

    const msg = encodeMouseEvent(0, x, y);
    this.sendEncrypted(msg);
  }

  sendMouseDown(button: 'left' | 'right' | 'middle', clientX: number, clientY: number): void {
    if (this.state !== 'streaming') return;
    const rect = this.canvas.getBoundingClientRect();
    const normX = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    const normY = Math.max(0, Math.min(1, (clientY - rect.top) / rect.height));

    const x = Math.round(normX * this.remoteWidth);
    const y = Math.round(normY * this.remoteHeight);

    const mask = button === 'left' ? 1 : button === 'right' ? 2 : 4;
    const msg = encodeMouseEvent(mask, x, y);
    this.sendEncrypted(msg);
  }

  sendMouseUp(clientX: number, clientY: number): void {
    if (this.state !== 'streaming') return;
    const rect = this.canvas.getBoundingClientRect();
    const normX = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    const normY = Math.max(0, Math.min(1, (clientY - rect.top) / rect.height));

    const x = Math.round(normX * this.remoteWidth);
    const y = Math.round(normY * this.remoteHeight);

    const msg = encodeMouseEvent(0, x, y);
    this.sendEncrypted(msg);
  }

  sendMouseWheel(deltaY: number, clientX: number, clientY: number): void {
    if (this.state !== 'streaming') return;
    const rect = this.canvas.getBoundingClientRect();
    const normX = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    const normY = Math.max(0, Math.min(1, (clientY - rect.top) / rect.height));

    const x = Math.round(normX * this.remoteWidth);
    const y = Math.round(normY * this.remoteHeight);

    const wheelDelta = deltaY > 0 ? -120 : 120;
    const msg = encodeMouseEvent(0, x, y, wheelDelta);
    this.sendEncrypted(msg);
  }

  sendKeyDown(keyCode: number, char = ''): void {
    if (this.state !== 'streaming') return;
    const chrCode = char.length === 1 ? char.charCodeAt(0) : 0;
    const msg = encodeKeyEvent(keyCode, true, false, false, false, false, chrCode);
    this.sendEncrypted(msg);
  }

  sendKeyUp(keyCode: number): void {
    if (this.state !== 'streaming') return;
    const msg = encodeKeyEvent(keyCode, false);
    this.sendEncrypted(msg);
  }

  sendKeyCombo(keys: string[]): void {
    const codeMap: Record<string, number> = {
      Control: 17,
      Alt: 18,
      Delete: 46,
      Meta: 91,
      Escape: 27,
      Tab: 9,
    };

    for (const k of keys) {
      const code = codeMap[k] || 0;
      if (code) this.sendKeyDown(code);
    }
    setTimeout(() => {
      for (const k of [...keys].reverse()) {
        const code = codeMap[k] || 0;
        if (code) this.sendKeyUp(code);
      }
    }, 100);
  }

  private handleError(message: string): void {
    this.emit({ type: 'error', message });
    this.emit({ type: 'state', state: 'error', detail: message });
    this.disconnect();
  }

  disconnect(): void {
    if (this.wsId) {
      try {
        this.wsId.close();
      } catch {
        // ignore
      }
      this.wsId = null;
    }
    if (this.wsRelay) {
      try {
        this.wsRelay.close();
      } catch {
        // ignore
      }
      this.wsRelay = null;
    }
    if (this.decoder) {
      this.decoder.close();
      this.decoder = null;
    }
    this.cipher = null;
    this.emit({ type: 'state', state: 'closed' });
  }
}
