/**
 * WebCodecs Hardware-Accelerated Video Decoder.
 * Decodes VP9, VP8, H.264, and AV1 video streams directly to HTMLCanvasElement.
 */

import type { CodecType, VideoStats } from './types';

export interface DecoderOptions {
  canvas: HTMLCanvasElement;
  onResolutionChange?: (width: number, height: number) => void;
  onStats?: (stats: VideoStats) => void;
  onError?: (err: Error) => void;
}

const CODEC_STRINGS: Record<CodecType, string> = {
  vp9: 'vp09.00.10.08',
  vp8: 'vp8',
  h264: 'avc1.42E01F',
  av1: 'av01.0.04M.08',
};

export class VideoStreamDecoder {
  private canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D | null;
  private decoder: VideoDecoder | null = null;
  private currentCodec: CodecType | null = null;
  private currentWidth = 0;
  private currentHeight = 0;

  // Stats tracking
  private framesDecoded = 0;
  private framesDropped = 0;
  private bytesReceived = 0;
  private lastStatsTimestamp = performance.now();
  private fpsCounter = 0;
  private currentFps = 0;

  private onResolutionChange?: (width: number, height: number) => void;
  private onStats?: (stats: VideoStats) => void;
  private onError?: (err: Error) => void;

  private isClosed = false;

  constructor(options: DecoderOptions) {
    this.canvas = options.canvas;
    this.ctx = this.canvas.getContext('2d', { alpha: false, desynchronized: true });
    this.onResolutionChange = options.onResolutionChange;
    this.onStats = options.onStats;
    this.onError = options.onError;
  }

  static isSupported(): boolean {
    return typeof window !== 'undefined' && typeof window.VideoDecoder === 'function';
  }

  private initDecoder(codec: CodecType): void {
    if (this.decoder) {
      try {
        this.decoder.close();
      } catch {
        // ignore
      }
      this.decoder = null;
    }

    const codecStr = CODEC_STRINGS[codec];
    this.decoder = new VideoDecoder({
      output: (frame: VideoFrame) => this.handleFrame(frame),
      error: (e: DOMException) => {
        this.framesDropped++;
        this.onError?.(new Error(`WebCodecs VideoDecoder error: ${e.message}`));
      },
    });

    this.decoder.configure({
      codec: codecStr,
      optimizeForLatency: true,
    });
    this.currentCodec = codec;
  }

  private handleFrame(frame: VideoFrame): void {
    if (this.isClosed) {
      frame.close();
      return;
    }

    try {
      const dw = frame.displayWidth;
      const dh = frame.displayHeight;

      if (dw !== this.currentWidth || dh !== this.currentHeight) {
        this.currentWidth = dw;
        this.currentHeight = dh;
        this.canvas.width = dw;
        this.canvas.height = dh;
        this.onResolutionChange?.(dw, dh);
      }

      if (this.ctx) {
        this.ctx.drawImage(frame, 0, 0, dw, dh);
      }

      this.framesDecoded++;
      this.fpsCounter++;
      this.updateStats();
    } finally {
      frame.close();
    }
  }

  private updateStats(): void {
    const now = performance.now();
    const elapsed = now - this.lastStatsTimestamp;
    if (elapsed >= 1000) {
      this.currentFps = Math.round((this.fpsCounter * 1000) / elapsed);
      const bitrateKbps = Math.round(((this.bytesReceived * 8) / elapsed));

      this.fpsCounter = 0;
      this.bytesReceived = 0;
      this.lastStatsTimestamp = now;

      this.onStats?.({
        codec: this.currentCodec || 'unknown',
        width: this.currentWidth,
        height: this.currentHeight,
        fps: this.currentFps,
        framesDecoded: this.framesDecoded,
        framesDropped: this.framesDropped,
        bitrateKbps,
      });
    }
  }

  decodeChunk(codec: CodecType, data: Uint8Array, isKeyFrame: boolean, timestampUs?: number): void {
    if (this.isClosed) return;

    if (!this.decoder || this.currentCodec !== codec) {
      this.initDecoder(codec);
    }

    this.bytesReceived += data.byteLength;

    const chunk = new EncodedVideoChunk({
      type: isKeyFrame ? 'key' : 'delta',
      timestamp: timestampUs ?? Math.round(performance.now() * 1000),
      data,
    });

    try {
      this.decoder?.decode(chunk);
    } catch (err) {
      this.framesDropped++;
      this.onError?.(err instanceof Error ? err : new Error(String(err)));
    }
  }

  close(): void {
    this.isClosed = true;
    if (this.decoder) {
      try {
        if (this.decoder.state !== 'closed') {
          this.decoder.close();
        }
      } catch {
        // ignore
      }
      this.decoder = null;
    }
    if (this.ctx) {
      this.ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
    }
  }
}
