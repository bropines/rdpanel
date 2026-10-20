/**
 * Web Client Types and Contracts.
 * Defines the state machine, input events, and session options.
 */

export type SessionState =
  | 'idle'
  | 'connecting_rendezvous'
  | 'requesting_relay'
  | 'connecting_relay'
  | 'pairing_relay'
  | 'handshaking'
  | 'authenticating'
  | 'streaming'
  | 'error'
  | 'closed';

export type CodecType = 'vp9' | 'vp8' | 'h264' | 'av1';

export interface WebClientConfig {
  peerId: string;
  password?: string;
  serverKeyB64?: string;
  wsIdUrl: string;
  wsRelayUrl: string;
  myId?: string;
  myName?: string;
}

export interface RemoteDisplay {
  index: number;
  x: number;
  y: number;
  width: number;
  height: number;
  name?: string;
  scale?: number;
}

export interface VideoStats {
  codec: string;
  width: number;
  height: number;
  fps: number;
  framesDecoded: number;
  framesDropped: number;
  bitrateKbps: number;
}

export type WebClientEvent =
  | { type: 'state'; state: SessionState; detail?: string }
  | { type: 'displays'; displays: RemoteDisplay[]; current: number }
  | { type: 'resolution'; width: number; height: number }
  | { type: 'stats'; stats: VideoStats }
  | { type: 'cursor'; dataUrl?: string; hotx: number; hoty: number }
  | { type: 'clipboard'; text: string }
  | { type: 'need_password' }
  | { type: 'error'; message: string };

export type WebClientEventListener = (event: WebClientEvent) => void;
