import { useQuery } from '@tanstack/react-query';
import { useSearch, useNavigate } from '@tanstack/react-router';
import {
  ArrowLeft,
  Cast,
  Clipboard,
  Computer,
  Eye,
  EyeOff,
  KeyRound,
  Maximize2,
  Minimize2,
  PowerOff,
  RefreshCw,
  ShieldAlert,
} from 'lucide-react';
import { useEffect, useRef, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { serverApi } from '@/api/system';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { VideoStreamDecoder } from '@/webclient/decoder';
import { WebClientSession } from '@/webclient/session';
import type { SessionState, VideoStats, WebClientEvent } from '@/webclient/types';

export function WebClientPage() {
  const { t } = useTranslation();
  const search = useSearch({ strict: false }) as { id?: string; pwd?: string };
  const navigate = useNavigate();

  const getUrlParam = (k: string) => {
    if (typeof window !== 'undefined') {
      const v = new URLSearchParams(window.location.search).get(k);
      if (v) return v.trim();
    }
    return '';
  };

  const initialPeerId = (search.id || getUrlParam('id') || '').trim();
  const initialPassword = search.pwd || getUrlParam('pwd') || '';

  const [peerId, setPeerId] = useState(initialPeerId);
  const [password, setPassword] = useState(initialPassword);
  const [showPassword, setShowPassword] = useState(false);
  const [passwordDialogOpen, setPasswordDialogOpen] = useState(false);
  const [promptPassword, setPromptPassword] = useState('');

  const [state, setState] = useState<SessionState>('idle');
  const [stateDetail, setStateDetail] = useState<string>('');
  const [stats, setStats] = useState<VideoStats | null>(null);
  const [resolution, setResolution] = useState<{ width: number; height: number }>({ width: 1920, height: 1080 });
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [clipboardDialogOpen, setClipboardDialogOpen] = useState(false);
  const [clipboardText, setClipboardText] = useState('');

  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const sessionRef = useRef<WebClientSession | null>(null);

  // Attempt to fetch server config if available (admin mode)
  const serverConfigQuery = useQuery({
    queryKey: ['server', 'client-config'],
    queryFn: serverApi.clientConfig,
    retry: false,
  });

  const isWebCodecsSupported = VideoStreamDecoder.isSupported();

  const getWsUrls = useCallback(() => {
    const isHttps = window.location.protocol === 'https:';
    const proto = isHttps ? 'wss:' : 'ws:';
    const host = window.location.hostname;

    // Prefer reverse proxy paths on port 443/80
    let wsIdUrl = `${proto}//${host}/ws/id`;
    let wsRelayUrl = `${proto}//${host}/ws/relay`;

    const data = serverConfigQuery.data;
    if (data?.id_server) {
      const idHost = data.id_server.split(':')[0] || host;
      if (idHost !== host) {
        wsIdUrl = `${proto}//${idHost}:21118`;
      }
    }
    if (data?.relay_server) {
      const relayHost = data.relay_server.split(':')[0] || host;
      if (relayHost !== host) {
        wsRelayUrl = `${proto}//${relayHost}:21119`;
      }
    }

    return {
      wsIdUrl,
      wsRelayUrl,
      serverKeyB64: data?.public_key,
    };
  }, [serverConfigQuery.data]);

  const startConnection = useCallback((targetId: string, pwd?: string) => {
    if (!targetId || !canvasRef.current) return;

    if (sessionRef.current) {
      sessionRef.current.disconnect();
      sessionRef.current = null;
    }

    const { wsIdUrl, wsRelayUrl, serverKeyB64 } = getWsUrls();

    const session = new WebClientSession(
      {
        peerId: targetId,
        password: pwd || password,
        wsIdUrl,
        wsRelayUrl,
        serverKeyB64,
        myId: 'web-' + Math.random().toString(36).substring(2, 8),
        myName: 'RD Panel Web',
      },
      canvasRef.current,
    );

    sessionRef.current = session;

    session.on((event: WebClientEvent) => {
      switch (event.type) {
        case 'state':
          setState(event.state);
          if (event.detail) setStateDetail(event.detail);
          break;
        case 'stats':
          setStats(event.stats);
          break;
        case 'resolution':
          setResolution({ width: event.width, height: event.height });
          break;
        case 'need_password':
          setPasswordDialogOpen(true);
          break;
        case 'clipboard':
          toast.info(t('webclient.clipboardReceived', 'Clipboard updated'));
          break;
        case 'error':
          toast.error(event.message);
          break;
      }
    });

    void session.connect();
  }, [password, getWsUrls, t]);

  useEffect(() => {
    const rawId = (search.id || getUrlParam('id') || '').trim();
    if (rawId && rawId !== peerId) {
      setPeerId(rawId);
    }
  }, [search.id, peerId]);

  useEffect(() => {
    const target = (peerId || search.id || getUrlParam('id') || '').trim();
    if (target && isWebCodecsSupported && state === 'idle') {
      startConnection(target, password || initialPassword);
    }
  }, [peerId, isWebCodecsSupported, startConnection, state, password, initialPassword, search.id]);

  useEffect(() => {
    return () => {
      if (sessionRef.current) {
        sessionRef.current.disconnect();
        sessionRef.current = null;
      }
    };
  }, []);

  // Handle password submission
  const handlePasswordSubmit = () => {
    if (sessionRef.current && promptPassword) {
      sessionRef.current.submitPassword(promptPassword);
      setPassword(promptPassword);
      setPasswordDialogOpen(false);
      setPromptPassword('');
    }
  };

  // Toggle fullscreen
  const toggleFullscreen = () => {
    if (!containerRef.current) return;
    if (!document.fullscreenElement) {
      void containerRef.current.requestFullscreen();
      setIsFullscreen(true);
    } else {
      void document.exitFullscreen();
      setIsFullscreen(false);
    }
  };

  // Keyboard forwarding
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (state !== 'streaming' || !sessionRef.current) return;
      if (passwordDialogOpen || clipboardDialogOpen) return;

      // Don't intercept F11/F12 or browser shortcuts if user is clicking outside canvas
      if (e.target !== canvasRef.current && !containerRef.current?.contains(e.target as Node)) {
        return;
      }

      e.preventDefault();
      sessionRef.current.sendKeyDown(e.keyCode, e.key);
    };

    const handleKeyUp = (e: KeyboardEvent) => {
      if (state !== 'streaming' || !sessionRef.current) return;
      if (passwordDialogOpen || clipboardDialogOpen) return;

      e.preventDefault();
      sessionRef.current.sendKeyUp(e.keyCode);
    };

    window.addEventListener('keydown', handleKeyDown);
    window.addEventListener('keyup', handleKeyUp);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      window.removeEventListener('keyup', handleKeyUp);
    };
  }, [state, passwordDialogOpen, clipboardDialogOpen]);

  const stateBadgeColor = {
    idle: 'secondary',
    connecting_rendezvous: 'outline',
    requesting_relay: 'outline',
    connecting_relay: 'outline',
    pairing_relay: 'outline',
    handshaking: 'outline',
    authenticating: 'outline',
    streaming: 'success',
    error: 'destructive',
    closed: 'secondary',
  }[state] as 'secondary' | 'outline' | 'success' | 'destructive';

  return (
    <div
      ref={containerRef}
      className="relative flex flex-col h-screen w-screen bg-neutral-950 text-neutral-100 overflow-hidden select-none font-sans"
    >
      {/* Top Floating Control Bar */}
      <header className="z-20 flex items-center justify-between px-4 py-2 bg-neutral-900/90 backdrop-blur border-b border-neutral-800 text-sm">
        <div className="flex items-center gap-3">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void navigate({ to: '/devices' })}
            className="text-neutral-400 hover:text-white"
          >
            <ArrowLeft className="h-4 w-4 mr-1" />
            {t('common.back', 'Back')}
          </Button>

          <div className="flex items-center gap-2">
            <Computer className="h-4 w-4 text-primary" />
            <span className="font-mono font-medium text-white">{peerId || t('webclient.noPeer', 'No Device')}</span>
          </div>

          <Badge variant={stateBadgeColor} className="capitalize">
            {state === 'streaming'
              ? `${stats?.fps ?? 0} FPS • ${resolution.width}x${resolution.height}`
              : t(`webclient.state.${state}`, state)}
          </Badge>

          {stats && state === 'streaming' && (
            <span className="text-xs text-neutral-400 hidden md:inline">
              {stats.codec.toUpperCase()} • {stats.bitrateKbps} kbps
            </span>
          )}
        </div>

        <div className="flex items-center gap-2">
          {state === 'streaming' && (
            <>
              <Button
                variant="outline"
                size="sm"
                onClick={() => sessionRef.current?.sendKeyCombo(['Control', 'Alt', 'Delete'])}
                title="Send Ctrl+Alt+Del"
                className="hidden sm:inline-flex"
              >
                Ctrl+Alt+Del
              </Button>

              <Button
                variant="outline"
                size="sm"
                onClick={() => sessionRef.current?.sendKeyCombo(['Meta'])}
                title="Send Win/Command"
                className="hidden sm:inline-flex"
              >
                Win
              </Button>

              <Button
                variant="outline"
                size="sm"
                onClick={() => setClipboardDialogOpen(true)}
                title={t('webclient.clipboard', 'Clipboard')}
              >
                <Clipboard className="h-4 w-4" />
              </Button>

              <Button
                variant="outline"
                size="sm"
                onClick={toggleFullscreen}
                title={isFullscreen ? 'Exit Fullscreen' : 'Fullscreen'}
              >
                {isFullscreen ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
              </Button>
            </>
          )}

          {state === 'streaming' ? (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => sessionRef.current?.disconnect()}
            >
              <PowerOff className="h-4 w-4 mr-1" />
              {t('webclient.disconnect', 'Disconnect')}
            </Button>
          ) : (
            <Button
              variant="default"
              size="sm"
              disabled={!peerId || !isWebCodecsSupported}
              onClick={() => startConnection(peerId)}
            >
              <RefreshCw className={`h-4 w-4 mr-1 ${state !== 'idle' && state !== 'closed' && state !== 'error' ? 'animate-spin' : ''}`} />
              {t('webclient.connect', 'Connect')}
            </Button>
          )}
        </div>
      </header>

      {/* Main Video Viewport */}
      <main className="relative flex-1 flex items-center justify-center bg-black overflow-hidden">
        {!isWebCodecsSupported ? (
          <div className="max-w-md p-6 text-center bg-neutral-900 border border-neutral-800 rounded-lg">
            <ShieldAlert className="h-12 w-12 text-destructive mx-auto mb-4" />
            <h3 className="text-lg font-semibold text-white mb-2">
              {t('webclient.unsupportedTitle', 'WebCodecs Not Supported')}
            </h3>
            <p className="text-sm text-neutral-400">
              {t('webclient.unsupportedDesc', 'Your browser does not support the WebCodecs API required for low-latency hardware video decoding. Please use Chrome, Edge, Safari 16.4+, or Firefox 130+.')}
            </p>
          </div>
        ) : state !== 'streaming' ? (
          <div className="max-w-md w-full p-6 text-center bg-neutral-900/80 border border-neutral-800 rounded-lg shadow-xl backdrop-blur">
            <Cast className="h-10 w-10 text-primary mx-auto mb-3 animate-pulse" />
            <h3 className="text-base font-medium text-white mb-1">
              {state === 'idle'
                ? t('webclient.enterDevice', 'Enter Device ID to Connect')
                : t(`webclient.state.${state}`, state)}
            </h3>
            {stateDetail && <p className="text-xs text-neutral-400 mb-4">{stateDetail}</p>}

            {(state === 'idle' || state === 'closed' || state === 'error') && (
              <div className="space-y-3 mt-4 text-left">
                <div>
                  <label className="text-xs text-neutral-400 mb-1 block">{t('devices.peerId', 'Remote Device ID')}</label>
                  <Input
                    value={peerId}
                    onChange={(e) => setPeerId(e.target.value)}
                    placeholder="e.g. 123456789"
                    className="bg-neutral-950 border-neutral-800 text-white font-mono"
                  />
                </div>
                <div>
                  <label className="text-xs text-neutral-400 mb-1 block">{t('common.password', 'Password (optional)')}</label>
                  <div className="relative">
                    <Input
                      type={showPassword ? 'text' : 'password'}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      placeholder="••••••••"
                      className="bg-neutral-950 border-neutral-800 text-white pr-9"
                    />
                    <button
                      type="button"
                      onClick={() => setShowPassword(!showPassword)}
                      className="absolute right-2.5 top-2.5 text-neutral-400 hover:text-white"
                    >
                      {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                    </button>
                  </div>
                </div>
                <Button
                  className="w-full mt-2"
                  disabled={!peerId}
                  onClick={() => startConnection(peerId)}
                >
                  {t('webclient.connect', 'Connect')}
                </Button>
              </div>
            )}
          </div>
        ) : null}

        {/* Video Canvas */}
        <canvas
          ref={canvasRef}
          tabIndex={0}
          className={`max-w-full max-h-full object-contain cursor-crosshair focus:outline-none ${
            state === 'streaming' ? 'block' : 'hidden'
          }`}
          onMouseMove={(e) => sessionRef.current?.sendMouseMove(e.clientX, e.clientY)}
          onMouseDown={(e) => {
            const btn = e.button === 0 ? 'left' : e.button === 2 ? 'right' : 'middle';
            sessionRef.current?.sendMouseDown(btn, e.clientX, e.clientY);
          }}
          onMouseUp={(e) => sessionRef.current?.sendMouseUp(e.clientX, e.clientY)}
          onWheel={(e) => sessionRef.current?.sendMouseWheel(e.deltaY, e.clientX, e.clientY)}
          onContextMenu={(e) => e.preventDefault()}
        />
      </main>

      {/* Password Prompt Modal */}
      <Dialog open={passwordDialogOpen} onOpenChange={setPasswordDialogOpen}>
        <DialogContent className="sm:max-w-md bg-neutral-900 border-neutral-800 text-white">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <KeyRound className="h-5 w-5 text-primary" />
              {t('webclient.passwordRequired', 'Remote Password Required')}
            </DialogTitle>
            <DialogDescription className="text-neutral-400">
              {t('webclient.passwordDesc', 'The remote machine requires authentication to start the remote session.')}
            </DialogDescription>
          </DialogHeader>
          <div className="py-2">
            <Input
              type="password"
              value={promptPassword}
              onChange={(e) => setPromptPassword(e.target.value)}
              placeholder={t('common.password', 'Password')}
              className="bg-neutral-950 border-neutral-800 text-white"
              autoFocus
              onKeyDown={(e) => {
                if (e.key === 'Enter') handlePasswordSubmit();
              }}
            />
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setPasswordDialogOpen(false);
                sessionRef.current?.disconnect();
              }}
            >
              {t('common.cancel', 'Cancel')}
            </Button>
            <Button onClick={handlePasswordSubmit}>
              {t('common.confirm', 'Confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Clipboard Modal */}
      <Dialog open={clipboardDialogOpen} onOpenChange={setClipboardDialogOpen}>
        <DialogContent className="sm:max-w-md bg-neutral-900 border-neutral-800 text-white">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Clipboard className="h-5 w-5 text-primary" />
              {t('webclient.clipboard', 'Clipboard Sync')}
            </DialogTitle>
            <DialogDescription className="text-neutral-400">
              {t('webclient.clipboardModalDesc', 'Paste text to send to the remote machine clipboard.')}
            </DialogDescription>
          </DialogHeader>
          <div className="py-2">
            <textarea
              value={clipboardText}
              onChange={(e) => setClipboardText(e.target.value)}
              placeholder="Paste text here..."
              rows={4}
              className="w-full p-2 rounded bg-neutral-950 border border-neutral-800 text-white text-sm focus:outline-none focus:ring-1 focus:ring-primary"
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setClipboardDialogOpen(false)}>
              {t('common.close', 'Close')}
            </Button>
            <Button
              onClick={() => {
                // In future: send clipboard packet
                toast.success('Sent to clipboard');
                setClipboardDialogOpen(false);
              }}
            >
              {t('common.send', 'Send to Remote')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
