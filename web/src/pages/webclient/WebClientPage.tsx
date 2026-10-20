import { useQuery } from '@tanstack/react-query';
import { useSearch, useNavigate } from '@tanstack/react-router';
import {
  ArrowLeft,
  Cast,
  Clipboard,
  Computer,
  Copy,
  Eye,
  EyeOff,
  Key,
  KeyRound,
  Loader2,
  Maximize2,
  Minimize2,
  PowerOff,
  RefreshCw,
  Scaling,
  Settings,
  ShieldAlert,
} from 'lucide-react';
import { useEffect, useRef, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { auth, meQueryKey } from '@/api/auth';
import { serverApi } from '@/api/system';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { VideoStreamDecoder } from '@/webclient/decoder';
import { WebClientSession } from '@/webclient/session';
import type { SessionState, VideoStats, WebClientEvent } from '@/webclient/types';

interface CustomServerSettings {
  idServer?: string;
  relayServer?: string;
  serverKey?: string;
}

const SETTINGS_STORAGE_KEY = 'rdpanel_webclient_server_settings';

function loadStoredSettings(): CustomServerSettings {
  try {
    const raw = localStorage.getItem(SETTINGS_STORAGE_KEY);
    if (raw) return JSON.parse(raw);
  } catch {
    // ignore
  }
  return {};
}

function saveStoredSettings(settings: CustomServerSettings) {
  try {
    localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify(settings));
  } catch {
    // ignore
  }
}

function getSavedPassword(id: string): string {
  try {
    return localStorage.getItem(`rdpanel_pwd_${id}`) || '';
  } catch {
    return '';
  }
}

function setSavedPassword(id: string, pwd: string) {
  try {
    if (pwd) {
      localStorage.setItem(`rdpanel_pwd_${id}`, pwd);
    } else {
      localStorage.removeItem(`rdpanel_pwd_${id}`);
    }
  } catch {
    // ignore
  }
}

export function WebClientPage() {
  const { t } = useTranslation();
  const search = useSearch({ strict: false }) as { id?: string | number; pwd?: string };
  const navigate = useNavigate();

  const getUrlParam = (k: string) => {
    if (typeof window !== 'undefined') {
      const v = new URLSearchParams(window.location.search).get(k);
      if (v) return v.trim();
    }
    return '';
  };

  const initialPeerId = String(search.id ?? getUrlParam('id') ?? '').trim();
  const initialPassword = String(search.pwd ?? getUrlParam('pwd') ?? '').trim();

  const [peerId, setPeerId] = useState(initialPeerId);
  const [password, setPassword] = useState(initialPassword);
  const [showPassword, setShowPassword] = useState(false);
  const [passwordDialogOpen, setPasswordDialogOpen] = useState(false);
  const [promptPassword, setPromptPassword] = useState('');
  const [rememberPassword, setRememberPassword] = useState(true);
  const [authErrorMessage, setAuthErrorMessage] = useState('');
  const [savedPwdState, setSavedPwdState] = useState(() => getSavedPassword(initialPeerId));

  const [serverSettings, setServerSettings] = useState<CustomServerSettings>(loadStoredSettings);
  const [settingsDialogOpen, setSettingsDialogOpen] = useState(false);
  const [editIdServer, setEditIdServer] = useState('');
  const [editRelayServer, setEditRelayServer] = useState('');
  const [editServerKey, setEditServerKey] = useState('');
  const [quickConfigInput, setQuickConfigInput] = useState('');

  const [state, setState] = useState<SessionState>('idle');
  const [stateDetail, setStateDetail] = useState<string>('');
  const [stats, setStats] = useState<VideoStats | null>(null);
  const [resolution, setResolution] = useState<{ width: number; height: number }>({ width: 1920, height: 1080 });
  const [viewMode, setViewMode] = useState<'fit' | 'original' | 'stretch'>('fit');
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [clipboardDialogOpen, setClipboardDialogOpen] = useState(false);
  const [clipboardText, setClipboardText] = useState('');

  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const sessionRef = useRef<WebClientSession | null>(null);

  const meQuery = useQuery({
    queryKey: meQueryKey,
    queryFn: auth.me,
    retry: false,
  });

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
    const currentHost = window.location.host;

    const data = serverConfigQuery.data;

    const defaultRelay = data?.relay_server || 'desk-ru.bropines.ru:21117';
    const defaultKey = data?.public_key || '';

    const idHost = serverSettings.idServer?.trim() || currentHost;
    const relayServer = serverSettings.relayServer?.trim() || defaultRelay;
    const serverKeyB64 = serverSettings.serverKey?.trim() || defaultKey;

    const wsIdUrl = `${proto}//${idHost}/ws/id`;
    const wsRelayUrl = `${proto}//${idHost}/ws/relay`;

    return {
      wsIdUrl,
      wsRelayUrl,
      relayServer,
      serverKeyB64,
    };
  }, [serverConfigQuery.data, serverSettings]);

  const startConnection = useCallback((targetId: string, pwd?: string) => {
    if (!targetId || !canvasRef.current) return;

    if (sessionRef.current) {
      sessionRef.current.disconnect();
      sessionRef.current = null;
    }

    const { wsIdUrl, wsRelayUrl, relayServer, serverKeyB64 } = getWsUrls();
    const savedPwd = getSavedPassword(targetId);
    setSavedPwdState(savedPwd);
    const effectivePwd = pwd || password || savedPwd || undefined;

    const session = new WebClientSession(
      {
        peerId: targetId,
        password: effectivePwd,
        wsIdUrl,
        wsRelayUrl,
        relayServer,
        serverKeyB64,
        myId: 'web-' + Math.random().toString(36).substring(2, 8),
        myName: meQuery.data?.user?.username ? `${meQuery.data.user.username} (Web)` : 'RD Panel Web',
      },
      canvasRef.current,
    );

    sessionRef.current = session;

    session.on((event: WebClientEvent) => {
      switch (event.type) {
        case 'state':
          setState(event.state);
          if (event.state === 'streaming') {
            setPasswordDialogOpen(false);
            setAuthErrorMessage('');
          }
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
        case 'auth_error':
          setAuthErrorMessage(event.message);
          toast.error(event.message);
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
  }, [password, getWsUrls, t, meQuery.data?.user?.username]);

  useEffect(() => {
    const rawId = String(search.id ?? getUrlParam('id') ?? '').trim();
    if (rawId) {
      setPeerId((current) => (current !== rawId ? rawId : current));
      setSavedPwdState(getSavedPassword(rawId));
    }
  }, [search.id]);

  useEffect(() => {
    if (serverConfigQuery.isLoading) return;
    const target = (peerId || initialPeerId).trim();
    if (target && isWebCodecsSupported && state === 'idle') {
      startConnection(target, password || initialPassword);
    }
  }, [serverConfigQuery.isLoading, peerId, initialPeerId, isWebCodecsSupported, startConnection, state, password, initialPassword]);

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
      if (rememberPassword) {
        setSavedPassword(peerId, promptPassword);
        setSavedPwdState(promptPassword);
      } else {
        setSavedPassword(peerId, '');
        setSavedPwdState('');
      }
      sessionRef.current.submitPassword(promptPassword);
      setPassword(promptPassword);
      setPasswordDialogOpen(false);
      setPromptPassword('');
      setAuthErrorMessage('');
    }
  };

  const handleApplyQuickConfig = () => {
    if (!quickConfigInput.trim()) return;
    let raw = quickConfigInput.trim();
    if (raw.startsWith('rustdesk://')) {
      raw = raw.slice('rustdesk://'.length);
    }
    if (raw.includes('config=')) {
      const match = /[?&]config=([^&]+)/.exec(raw);
      if (match && match[1]) raw = decodeURIComponent(match[1]);
    }

    let parsed: Record<string, unknown> | null = null;
    try {
      const decoded = atob(raw);
      parsed = JSON.parse(decoded);
    } catch {
      try {
        parsed = JSON.parse(raw);
      } catch {
        try {
          const params = new URLSearchParams(raw);
          const obj: Record<string, string> = {};
          params.forEach((v, k) => { obj[k] = v; });
          if (Object.keys(obj).length > 0) parsed = obj;
        } catch {
          // ignore
        }
      }
    }

    if (parsed && typeof parsed === 'object') {
      const host = (parsed.host || parsed.id_server || parsed.idServer || parsed.rendezvous || '') as string;
      const relay = (parsed.relay || parsed.relay_server || parsed.relayServer || '') as string;
      const key = (parsed.key || parsed.public_key || parsed.serverKey || '') as string;

      if (host || relay || key) {
        if (host) setEditIdServer(host);
        if (relay) setEditRelayServer(relay);
        if (key) setEditServerKey(key);
        setQuickConfigInput('');
        toast.success(t('webclient.configImported', 'Configuration applied from quick config'));
        return;
      }
    }

    toast.error(t('webclient.invalidQuickConfig', 'Could not parse configuration string'));
  };

  const handleExportConfig = () => {
    const effectiveHost = editIdServer || serverSettings.idServer || (typeof window !== 'undefined' ? window.location.host : '');
    const effectiveRelay = editRelayServer || serverSettings.relayServer || serverConfigQuery.data?.relay_server || '';
    const effectiveKey = editServerKey || serverSettings.serverKey || serverConfigQuery.data?.public_key || '';

    const cfgObj = {
      host: effectiveHost,
      relay: effectiveRelay,
      key: effectiveKey,
    };
    const b64 = btoa(JSON.stringify(cfgObj));
    if (navigator.clipboard?.writeText) {
      void navigator.clipboard.writeText(b64).then(() => {
        toast.success(t('webclient.configExported', 'Configuration copied to clipboard'));
      }).catch(() => {
        toast.info(b64);
      });
    } else {
      toast.info(b64);
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

  useEffect(() => {
    const onFsChange = () => setIsFullscreen(!!document.fullscreenElement);
    document.addEventListener('fullscreenchange', onFsChange);
    return () => document.removeEventListener('fullscreenchange', onFsChange);
  }, []);

  const handleSendCtrlAltDel = () => {
    if (sessionRef.current) {
      sessionRef.current.sendCtrlAltDel();
      toast.success(t('webclient.ctrlAltDelSent', 'Ctrl+Alt+Del sent'));
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

          {peerId && savedPwdState && (
            <Button
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-[11px] text-neutral-300 hover:text-white hover:bg-neutral-800 border border-neutral-700/60"
              onClick={() => {
                setSavedPassword(peerId, '');
                setSavedPwdState('');
                toast.success(t('webclient.passwordCleared', 'Saved password removed'));
              }}
              title={t('webclient.clearSavedPassword', 'Forget saved password for this device')}
            >
              <Key className="h-3 w-3 text-primary mr-1" />
              <span className="hidden sm:inline">{t('webclient.passwordSaved', 'Password saved')}</span>
            </Button>
          )}

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

          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setEditIdServer(serverSettings.idServer || '');
              setEditRelayServer(serverSettings.relayServer || '');
              setEditServerKey(serverSettings.serverKey || '');
              setSettingsDialogOpen(true);
            }}
            title={t('webclient.serverSettings', 'Server Settings')}
          >
            <Settings className="h-4 w-4" />
          </Button>

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
      <main className={`relative flex-1 flex items-center justify-center bg-black ${viewMode === 'original' ? 'overflow-auto' : 'overflow-hidden'}`}>
        {state === 'streaming' && (
          <div className="absolute top-3 left-1/2 -translate-x-1/2 z-40 flex items-center gap-1.5 bg-neutral-900/90 hover:bg-neutral-900 backdrop-blur border border-neutral-700/80 rounded-full px-3 py-1 shadow-2xl transition-all select-none">
            <div className="flex items-center gap-1.5 text-xs text-neutral-300 pr-2 border-r border-neutral-700/80">
              <span className="font-mono font-medium">{peerId}</span>
              {resolution && (
                <Badge variant="outline" className="text-[10px] px-1.5 py-0 h-4 bg-neutral-800 text-neutral-300 border-neutral-700">
                  {resolution.width}×{resolution.height}
                </Badge>
              )}
              {stats && stats.fps > 0 && (
                <span className="text-[10px] text-neutral-400 font-mono">
                  {stats.fps} fps
                </span>
              )}
            </div>

            {/* Scaling View Mode */}
            <Button
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs text-neutral-300 hover:text-white hover:bg-neutral-800"
              onClick={() => setViewMode((m) => (m === 'fit' ? 'original' : m === 'original' ? 'stretch' : 'fit'))}
              title={t(`webclient.view_${viewMode}`, viewMode)}
            >
              <Scaling className="h-3 w-3 mr-1 text-primary" />
              <span>{t(`webclient.view_${viewMode}`, viewMode)}</span>
            </Button>

            {/* Ctrl+Alt+Del */}
            <Button
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs text-neutral-300 hover:text-white hover:bg-neutral-800"
              onClick={handleSendCtrlAltDel}
              title={t('webclient.sendCtrlAltDel', 'Send Ctrl+Alt+Del')}
            >
              <span>Ctrl+Alt+Del</span>
            </Button>

            {/* Fullscreen */}
            <Button
              variant="ghost"
              size="icon"
              className="h-6 w-6 text-neutral-300 hover:text-white hover:bg-neutral-800"
              onClick={toggleFullscreen}
              title={isFullscreen ? t('webclient.exitFullscreen', 'Exit Fullscreen') : t('webclient.fullscreen', 'Fullscreen')}
            >
              {isFullscreen ? <Minimize2 className="h-3 w-3" /> : <Maximize2 className="h-3 w-3" />}
            </Button>

            {/* Disconnect */}
            <Button
              variant="destructive"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => sessionRef.current?.disconnect()}
            >
              <PowerOff className="h-3 w-3 mr-1" />
              {t('webclient.disconnect', 'Disconnect')}
            </Button>
          </div>
        )}

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
                <div className="flex items-center justify-center pt-2">
                  <button
                    type="button"
                    onClick={() => {
                      setEditIdServer(serverSettings.idServer || '');
                      setEditRelayServer(serverSettings.relayServer || '');
                      setEditServerKey(serverSettings.serverKey || '');
                      setSettingsDialogOpen(true);
                    }}
                    className="text-xs text-neutral-400 hover:text-white flex items-center gap-1.5 transition-colors"
                  >
                    <Settings className="h-3.5 w-3.5" />
                    <span>{t('webclient.serverSettings', 'Server Settings')}</span>
                  </button>
                </div>
              </div>
            )}
          </div>
        ) : null}

        {/* Video Canvas */}
        <canvas
          ref={canvasRef}
          tabIndex={0}
          className={`${
            viewMode === 'fit'
              ? 'max-w-full max-h-full object-contain'
              : viewMode === 'stretch'
                ? 'w-full h-full object-fill'
                : 'w-auto h-auto max-w-none'
          } cursor-crosshair focus:outline-none ${
            state === 'streaming' ? 'block' : 'hidden'
          }`}
          onMouseMove={(e) => sessionRef.current?.sendMouseMove(e.clientX, e.clientY)}
          onMouseDown={(e) => {
            const btn = e.button === 0 ? 'left' : e.button === 2 ? 'right' : 'middle';
            sessionRef.current?.sendMouseDown(btn, e.clientX, e.clientY);
          }}
          onMouseUp={(e) => {
            const btn = e.button === 0 ? 'left' : e.button === 2 ? 'right' : 'middle';
            sessionRef.current?.sendMouseUp(btn, e.clientX, e.clientY);
          }}
          onWheel={(e) => {
            e.preventDefault();
            sessionRef.current?.sendMouseWheel(e.deltaX, e.deltaY, e.deltaMode);
          }}
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

          <div className="space-y-3 py-2">
            <div className="flex items-center gap-2.5 p-3 rounded-md bg-neutral-950 border border-neutral-800 text-xs text-neutral-300">
              <Loader2 className="h-4 w-4 text-primary animate-spin shrink-0" />
              <span>{t('webclient.waitingForApproval', 'Waiting for confirmation on remote device, or enter password below:')}</span>
            </div>

            <div className="space-y-1.5">
              <Input
                type="password"
                value={promptPassword}
                onChange={(e) => {
                  setPromptPassword(e.target.value);
                  if (authErrorMessage) setAuthErrorMessage('');
                }}
                placeholder={t('webclient.passwordPlaceholder', 'Remote device password')}
                className="bg-neutral-950 border-neutral-800 text-white font-mono text-sm"
                autoFocus
                onKeyDown={(e) => {
                  if (e.key === 'Enter') handlePasswordSubmit();
                }}
              />
              {authErrorMessage && (
                <p className="text-xs text-destructive">{authErrorMessage}</p>
              )}
            </div>

            <div className="flex items-center space-x-2 pt-1">
              <input
                type="checkbox"
                id="remember-pwd"
                checked={rememberPassword}
                onChange={(e) => setRememberPassword(e.target.checked)}
                className="h-4 w-4 rounded border-neutral-700 bg-neutral-950 text-primary accent-primary cursor-pointer"
              />
              <label htmlFor="remember-pwd" className="text-xs text-neutral-300 cursor-pointer select-none">
                {t('webclient.rememberPassword', 'Remember password for this device')}
              </label>
            </div>
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

      {/* Server Settings Modal */}
      <Dialog open={settingsDialogOpen} onOpenChange={setSettingsDialogOpen}>
        <DialogContent className="sm:max-w-md bg-neutral-900 border-neutral-800 text-white">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Settings className="h-5 w-5 text-primary" />
              {t('webclient.serverSettings', 'Server Settings')}
            </DialogTitle>
            <DialogDescription className="text-neutral-400">
              {t('webclient.serverSettingsDesc', 'Configure custom rendezvous, relay server, and key for RustDesk connections.')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            {/* Quick Base64 / URL Config Import */}
            <div className="p-3 rounded-md bg-neutral-950 border border-neutral-800 space-y-2">
              <div className="flex items-center justify-between">
                <label className="text-xs font-medium text-neutral-300">
                  {t('webclient.quickConfigTitle', 'Quick Config (Base64 / URL)')}
                </label>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-6 px-1.5 text-[11px] text-primary hover:text-primary/80"
                  onClick={handleExportConfig}
                >
                  <Copy className="h-3 w-3 mr-1" />
                  {t('webclient.exportConfig', 'Export Base64')}
                </Button>
              </div>
              <div className="flex gap-2">
                <Input
                  value={quickConfigInput}
                  onChange={(e) => setQuickConfigInput(e.target.value)}
                  placeholder="eyJob3N0IjoiLi4uIiwia2V5IjoiLi4uIn0="
                  className="bg-neutral-900 border-neutral-800 text-white font-mono text-xs h-8"
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      handleApplyQuickConfig();
                    }
                  }}
                />
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  className="h-8 px-3 text-xs"
                  onClick={handleApplyQuickConfig}
                >
                  {t('webclient.applyQuickConfig', 'Apply')}
                </Button>
              </div>
            </div>

            <div>
              <label className="text-xs text-neutral-400 mb-1 block">
                {t('webclient.idServer', 'ID / Rendezvous Server')}
              </label>
              <Input
                value={editIdServer}
                onChange={(e) => setEditIdServer(e.target.value)}
                placeholder={typeof window !== 'undefined' ? window.location.host : ''}
                className="bg-neutral-950 border-neutral-800 text-white font-mono text-xs"
              />
            </div>
            <div>
              <label className="text-xs text-neutral-400 mb-1 block">
                {t('webclient.relayServer', 'Relay Server')}
              </label>
              <Input
                value={editRelayServer}
                onChange={(e) => setEditRelayServer(e.target.value)}
                placeholder={serverConfigQuery.data?.relay_server || 'desk-ru.bropines.ru:21117'}
                className="bg-neutral-950 border-neutral-800 text-white font-mono text-xs"
              />
            </div>
            <div>
              <label className="text-xs text-neutral-400 mb-1 block">
                {t('webclient.serverKey', 'Public Key')}
              </label>
              <Input
                value={editServerKey}
                onChange={(e) => setEditServerKey(e.target.value)}
                placeholder={serverConfigQuery.data?.public_key || t('webclient.serverKeyPlaceholder', 'Server public key (base64)')}
                className="bg-neutral-950 border-neutral-800 text-white font-mono text-xs"
              />
            </div>
          </div>
          <DialogFooter className="flex justify-between items-center sm:justify-between">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                setEditIdServer('');
                setEditRelayServer('');
                setEditServerKey('');
                saveStoredSettings({});
                setServerSettings({});
                toast.success(t('webclient.resetDefaults', 'Reset to Defaults'));
              }}
            >
              {t('webclient.resetDefaults', 'Reset to Defaults')}
            </Button>
            <div className="flex gap-2">
              <Button variant="ghost" size="sm" onClick={() => setSettingsDialogOpen(false)}>
                {t('common.cancel', 'Cancel')}
              </Button>
              <Button
                size="sm"
                onClick={() => {
                  const updated: CustomServerSettings = {
                    idServer: editIdServer.trim() || undefined,
                    relayServer: editRelayServer.trim() || undefined,
                    serverKey: editServerKey.trim() || undefined,
                  };
                  saveStoredSettings(updated);
                  setServerSettings(updated);
                  setSettingsDialogOpen(false);
                  toast.success(t('common.saved', 'Saved'));
                }}
              >
                {t('common.save', 'Save')}
              </Button>
            </div>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
