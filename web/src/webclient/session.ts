/**
 * Web Client Session Orchestrator.
 * Manages the full lifecycle of a browser remote desktop connection:
 * Rendezvous (hbbs) -> Relay pairing (hbbr) -> E2E Crypto Handshake ->
 * Authentication -> WebCodecs Video Streaming -> Mouse/Keyboard Input Forwarding.
 */

import { StreamCipher, sealSymmetricKey, loginPasswordHash, decodeB64, signOpen } from './crypto';
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
  encodeCtrlAltDel,
  decodePeerMessage,
  decodeIdPk,
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

  // Scroll smoothing accumulators
  private wheelAccumulatorY = 0;
  private wheelAccumulatorX = 0;
  private lastWheelTime = 0;

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

      const tryConnect = (url: string, fallbackUrl?: string) => {
        let ws: WebSocket;
        try {
          ws = new WebSocket(url);
        } catch (e) {
          if (fallbackUrl) {
            tryConnect(fallbackUrl);
            return;
          }
          reject(new Error(`Failed to create WebSocket to rendezvous: ${e}`));
          return;
        }

        this.wsId = ws;
        ws.binaryType = 'arraybuffer';

        const timeout = setTimeout(() => {
          if (ws.readyState !== WebSocket.OPEN) {
            ws.close();
            if (fallbackUrl) {
              tryConnect(fallbackUrl);
              return;
            }
            reject(new Error('Rendezvous connection timeout (10s)'));
          }
        }, 8000);

        let relayTimeout: ReturnType<typeof setTimeout> | null = null;

        ws.onopen = () => {
          clearTimeout(timeout);
          this.requestRelay();
          relayTimeout = setTimeout(() => {
            if (this.state === 'requesting_relay') {
              try {
                ws.close();
              } catch {
                // ignore
              }
              reject(new Error('Relay request timed out: remote device did not respond. Is RustDesk running on the device?'));
            }
          }, 15000);
        };

        ws.onmessage = async (evt: MessageEvent) => {
          try {
            if (relayTimeout) {
              clearTimeout(relayTimeout);
              relayTimeout = null;
            }
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

              const relayServer = resp.relayServer || this.config.relayServer || '';
              const uuid = resp.uuid || this.sessionUuid;
              await this.connectRelay(relayServer, uuid);
              resolve();
            }
          } catch (e) {
            reject(e);
          }
        };

        ws.onerror = () => {
          clearTimeout(timeout);
          if (relayTimeout) {
            clearTimeout(relayTimeout);
            relayTimeout = null;
          }
          if (fallbackUrl) {
            tryConnect(fallbackUrl);
            return;
          }
          reject(new Error(`Failed to connect to rendezvous at ${url}`));
        };

        ws.onclose = () => {
          clearTimeout(timeout);
          if (relayTimeout) {
            clearTimeout(relayTimeout);
            relayTimeout = null;
          }
        };
      };

      const primaryUrl = this.config.wsIdUrl;
      const host = window.location.host;
      const isHttps = window.location.protocol === 'https:';
      // Only attempt direct port 21118 on plaintext http (e.g. local dev). On https, browsers block ws://
      // and port 21118 doesn't speak TLS, so falling back to wss://:21118 causes spurious TLS errors.
      const fallbackUrl = (!isHttps && primaryUrl.includes('/ws/id'))
        ? `ws://${host}:21118`
        : undefined;

      tryConnect(primaryUrl, fallbackUrl);
    });
  }

  private requestRelay(): void {
    if (!this.wsId || this.wsId.readyState !== WebSocket.OPEN) return;
    this.emit({ type: 'state', state: 'requesting_relay' });

    this.sessionUuid = crypto.randomUUID ? crypto.randomUUID() : String(Date.now());
    const req = encodeRequestRelay(
      this.config.peerId,
      this.sessionUuid,
      this.config.relayServer,
      this.config.serverKeyB64,
    );
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

      const tryConnectRelay = (url: string, fallbackUrl?: string) => {
        let ws: WebSocket;
        try {
          ws = new WebSocket(url);
        } catch (e) {
          if (fallbackUrl) {
            tryConnectRelay(fallbackUrl);
            return;
          }
          reject(new Error(`Failed to create WebSocket to relay: ${e}`));
          return;
        }

        this.wsRelay = ws;
        ws.binaryType = 'arraybuffer';

        const timeout = setTimeout(() => {
          if (ws.readyState !== WebSocket.OPEN) {
            ws.close();
            if (fallbackUrl) {
              tryConnectRelay(fallbackUrl);
              return;
            }
            reject(new Error('Relay connection timeout (15s)'));
          }
        }, 12000);

        ws.onopen = () => {
          clearTimeout(timeout);
          this.emit({ type: 'state', state: 'pairing_relay' });
          const req = encodeRelayRequest(uuid, this.config.peerId, this.config.serverKeyB64);
          ws.send(req);
        };

        ws.onmessage = (evt: MessageEvent) => {
          void this.handleRelayMessage(new Uint8Array(evt.data as ArrayBuffer));
        };

        ws.onerror = () => {
          clearTimeout(timeout);
          if (fallbackUrl) {
            tryConnectRelay(fallbackUrl);
            return;
          }
          reject(new Error(`Failed to connect to relay at ${url}`));
        };

        ws.onclose = (evt) => {
          clearTimeout(timeout);
          if (this.state !== 'closed' && this.state !== 'error') {
            this.emit({ type: 'state', state: 'closed', detail: evt.reason || 'Relay connection closed' });
          }
        };
      };

      const primaryUrl = wsUrl;
      const host = window.location.host;
      const isHttps = window.location.protocol === 'https:';
      // Only attempt direct port 21119 on plaintext http.
      const fallbackUrl = (!isHttps && primaryUrl.includes('/ws/relay'))
        ? `ws://${host}:21119`
        : undefined;

      tryConnectRelay(primaryUrl, fallbackUrl);
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
            if (this.pendingPasswordResolve) {
              const resolve = this.pendingPasswordResolve;
              this.pendingPasswordResolve = null;
              resolve('');
            }
            if (msg.error) {
              const errLower = msg.error.toLowerCase();
              if (errLower.includes('wrong') || errLower.includes('password') || errLower.includes('bad password')) {
                this.emit({ type: 'auth_error', message: msg.error });
                this.emit({ type: 'need_password' });
              } else {
                this.handleError(`Authentication failed: ${msg.error}`);
              }
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

    let idPkBytes: Uint8Array | null = null;
    if (this.config.serverKeyB64) {
      try {
        const serverKey = decodeB64(this.config.serverKeyB64);
        idPkBytes = signOpen(signedId, serverKey);
      } catch (e) {
        console.warn('Server key signature verification failed, falling back to payload parsing', e);
      }
    }

    if (!idPkBytes) {
      // In libsodium crypto_sign, signature is 64 bytes prepended to the message.
      idPkBytes = signedId.length > 64 ? signedId.subarray(64) : signedId;
    }

    const { pk } = decodeIdPk(idPkBytes);
    let peerCurvePk = pk;
    if (peerCurvePk.length !== 32) {
      // Fallback: take last 32 bytes
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
      // Send an empty LoginRequest so remote host triggers incoming connection request (try_start_cm).
      // This displays the "Accept / Dismiss" dialog to the remote operator immediately!
      const emptyReq = encodeLoginRequest(
        this.config.peerId,
        new Uint8Array(0),
        this.config.myId || 'web',
        this.config.myName || 'RD Panel Web',
      );
      this.sendEncrypted(emptyReq);

      this.emit({ type: 'need_password' });
      password = await new Promise<string>((res) => {
        this.pendingPasswordResolve = res;
      });
      if (!password) {
        // Resolved with empty string (e.g. accepted on remote peer or session closed)
        return;
      }
    }

    await this.sendLoginWithPassword(password);
  }

  private async sendLoginWithPassword(password: string): Promise<void> {
    const passHash = await loginPasswordHash(password, this.authSalt, this.authChallenge);
    const loginReq = encodeLoginRequest(
      this.config.peerId,
      passHash,
      this.config.myId || 'web',
      this.config.myName || 'RD Panel Web',
    );

    this.sendEncrypted(loginReq);
  }

  submitPassword(password: string): void {
    if (this.pendingPasswordResolve) {
      const resolve = this.pendingPasswordResolve;
      this.pendingPasswordResolve = null;
      resolve(password);
    } else {
      void this.sendLoginWithPassword(password);
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

    // MOUSE_TYPE_MOVE = 0, no button
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

    // RustDesk: mask = (button << 3) | MOUSE_TYPE_DOWN (1)
    // Buttons: left = 1, right = 2, wheel/middle = 4
    const btnBit = button === 'left' ? 1 : button === 'right' ? 2 : 4;
    const mask = (btnBit << 3) | 1;
    const msg = encodeMouseEvent(mask, x, y);
    this.sendEncrypted(msg);
  }

  sendMouseUp(button: 'left' | 'right' | 'middle', clientX: number, clientY: number): void {
    if (this.state !== 'streaming') return;
    const rect = this.canvas.getBoundingClientRect();
    const normX = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    const normY = Math.max(0, Math.min(1, (clientY - rect.top) / rect.height));

    const x = Math.round(normX * this.remoteWidth);
    const y = Math.round(normY * this.remoteHeight);

    // RustDesk: mask = (button << 3) | MOUSE_TYPE_UP (2)
    const btnBit = button === 'left' ? 1 : button === 'right' ? 2 : 4;
    const mask = (btnBit << 3) | 2;
    const msg = encodeMouseEvent(mask, x, y);
    this.sendEncrypted(msg);
  }

  sendMouseWheel(deltaX: number, deltaY: number, deltaMode = 0): void {
    if (this.state !== 'streaming') return;

    const now = performance.now();
    if (now - this.lastWheelTime > 250) {
      this.wheelAccumulatorY = 0;
      this.wheelAccumulatorX = 0;
    }
    this.lastWheelTime = now;

    let stepsY = 0;
    let stepsX = 0;

    if (deltaMode === 1) {
      // DOM_DELTA_LINE: discrete mouse wheel lines (e.g. Firefox or Windows mouse wheel)
      // Positive deltaY (scroll down) -> negative stepsY (down)
      stepsY = deltaY > 0 ? -1 : deltaY < 0 ? 1 : 0;
      stepsX = deltaX > 0 ? -1 : deltaX < 0 ? 1 : 0;
    } else if (deltaMode === 2) {
      // DOM_DELTA_PAGE
      stepsY = deltaY > 0 ? -3 : deltaY < 0 ? 3 : 0;
      stepsX = deltaX > 0 ? -3 : deltaX < 0 ? 3 : 0;
    } else {
      // DOM_DELTA_PIXEL: trackpad, smooth wheel, touch gesture
      this.wheelAccumulatorY += deltaY;
      this.wheelAccumulatorX += deltaX;

      const PIXELS_PER_STEP = 45;

      if (Math.abs(this.wheelAccumulatorY) >= PIXELS_PER_STEP) {
        const count = Math.trunc(this.wheelAccumulatorY / PIXELS_PER_STEP);
        // Clamp to prevent huge jumps from fast gestures
        stepsY = -Math.min(5, Math.max(-5, count));
        this.wheelAccumulatorY -= count * PIXELS_PER_STEP;
      }

      if (Math.abs(this.wheelAccumulatorX) >= PIXELS_PER_STEP) {
        const count = Math.trunc(this.wheelAccumulatorX / PIXELS_PER_STEP);
        stepsX = -Math.min(5, Math.max(-5, count));
        this.wheelAccumulatorX -= count * PIXELS_PER_STEP;
      }
    }

    if (stepsY !== 0 || stepsX !== 0) {
      // RustDesk protocol: mask = MOUSE_TYPE_WHEEL (3), x = stepsX, y = stepsY
      // Host side (input_service.rs) on Windows multiplies x and y by WHEEL_DELTA (120)
      const mask = 3;
      const msg = encodeMouseEvent(mask, stepsX, stepsY);
      this.sendEncrypted(msg);
    }
  }

  sendCtrlAltDel(): void {
    if (this.state !== 'streaming') return;
    const msg = encodeCtrlAltDel();
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
    if (this.pendingPasswordResolve) {
      const resolve = this.pendingPasswordResolve;
      this.pendingPasswordResolve = null;
      resolve('');
    }
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
