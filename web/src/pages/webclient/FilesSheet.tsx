import {
  ArrowUp,
  Download as DownloadIcon,
  Eye,
  EyeOff,
  File as FileIcon,
  Folder,
  FolderPlus,
  FolderUp,
  HardDrive,
  Home,
  Loader2,
  MoreVertical,
  Pencil,
  RefreshCw,
  Trash2,
  Upload as UploadIcon,
  X,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  FileTransferError,
  FileTransferSession,
  type ConflictAnswer,
  type RemoteEntry,
  type ServerConfig,
  type TransferProgress,
  type UploadConflict,
  type UploadItem,
} from "rdweb";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import { ZipWriter } from "./zip";

export interface FilesSheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  peerId: string;
  server: ServerConfig;
  /** Salted password hash of the remote-desktop login, so the files connection signs in silently. */
  rememberedHash: Uint8Array | null;
  myName?: string;
  myId?: string;
}

type ConnState = "idle" | "connecting" | "password" | "twoFactor" | "waiting" | "ready" | "error";

interface Transfer {
  id: number;
  label: string;
  direction: "download" | "upload";
  progress: TransferProgress | null;
  state: "running" | "done" | "failed" | "cancelled";
  message?: string;
  cancel: () => void;
}

type ShowDirectoryPicker = (o?: { mode?: "read" | "readwrite" }) => Promise<FileSystemDirectoryHandle>;

function formatSize(n: number, lang: string): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toLocaleString(lang, { maximumFractionDigits: v < 10 ? 1 : 0 })} ${units[i]}`;
}

function isDir(e: RemoteEntry): boolean {
  return e.type === "dir" || e.type === "dirLink" || e.type === "drive";
}

function saveBlob(blob: Blob, name: string): void {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 30_000);
}

/** Files from a drop, including folders (via the entries API where available). */
async function droppedItems(dt: DataTransfer): Promise<UploadItem[]> {
  const out: UploadItem[] = [];
  const entries = [...dt.items]
    .map((i) => (typeof i.webkitGetAsEntry === "function" ? i.webkitGetAsEntry() : null))
    .filter((e): e is FileSystemEntry => !!e);
  if (!entries.length) {
    for (const f of [...dt.files]) out.push({ name: f.name, size: f.size, lastModified: f.lastModified, data: f });
    return out;
  }
  const walk = async (entry: FileSystemEntry, prefix: string): Promise<void> => {
    if (entry.isFile) {
      const file = await new Promise<File>((res, rej) => (entry as FileSystemFileEntry).file(res, rej));
      out.push({ name: file.name, relativePath: prefix + file.name, size: file.size, lastModified: file.lastModified, data: file });
    } else if (entry.isDirectory) {
      const reader = (entry as FileSystemDirectoryEntry).createReader();
      for (;;) {
        const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej));
        if (!batch.length) break;
        for (const e of batch) await walk(e, `${prefix}${entry.name}/`);
      }
    }
  };
  for (const e of entries) await walk(e, "");
  return out;
}

export function FilesSheet(p: FilesSheetProps) {
  const { t, i18n } = useTranslation();
  const ftRef = useRef<FileTransferSession | null>(null);
  const [conn, setConn] = useState<ConnState>("idle");
  const [connError, setConnError] = useState("");
  const [pwWrong, setPwWrong] = useState(false);
  const [secret, setSecret] = useState("");
  const [cwd, setCwd] = useState("");
  const [pathInput, setPathInput] = useState("");
  const [entries, setEntries] = useState<RemoteEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [showHidden, setShowHidden] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [transfers, setTransfers] = useState<Transfer[]>([]);
  const [conflict, setConflict] = useState<{ c: UploadConflict; answer: (a: ConflictAnswer) => void } | null>(null);
  const [nameDialog, setNameDialog] = useState<{ kind: "mkdir" | "rename"; entry?: RemoteEntry } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<RemoteEntry[] | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const lang = i18n.language;
  const isWindows = ftRef.current?.peer?.os === "windows";

  const fail = useCallback(
    (e: unknown) => {
      const code = e instanceof FileTransferError ? e.code : "";
      const known = ["not-found", "permission-denied", "exists", "not-empty", "invalid-name", "overflow", "too-large", "timeout"];
      toast.error(code && known.includes(code) ? t(`webclient.files.errors.${code}`) : (e as Error).message);
    },
    [t],
  );

  const list = useCallback(
    async (path: string, hidden = showHidden) => {
      const ft = ftRef.current;
      if (!ft) return;
      setLoading(true);
      try {
        const dir = await ft.listDir(path, { showHidden: hidden });
        setCwd(dir.path);
        setPathInput(dir.path);
        setEntries(
          [...dir.entries].sort((a, b) => Number(isDir(b)) - Number(isDir(a)) || a.name.localeCompare(b.name, undefined, { numeric: true })),
        );
        setSelected(new Set());
      } catch (e) {
        fail(e);
      } finally {
        setLoading(false);
      }
    },
    [fail, showHidden],
  );

  // Connect lazily on first open; keep the connection while the remote session lives.
  useEffect(() => {
    if (!p.open || ftRef.current) return;
    const ft = new FileTransferSession({
      peerId: p.peerId,
      server: p.server,
      rememberedHash: p.rememberedHash ?? undefined,
      myName: p.myName,
      myId: p.myId,
    });
    ftRef.current = ft;
    setConn("connecting");
    ft.on("password", ({ wrong }) => {
      setPwWrong(wrong);
      setConn("password");
    });
    ft.on("twoFactor", ({ wrong }) => {
      setPwWrong(wrong);
      setConn("twoFactor");
    });
    ft.on("waitingForApproval", () => setConn("waiting"));
    ft.on("connected", ({ home }) => {
      setConn("ready");
      if (home) {
        setCwd(home.path);
        setPathInput(home.path);
        setEntries([...home.entries].sort((a, b) => Number(isDir(b)) - Number(isDir(a)) || a.name.localeCompare(b.name)));
      }
    });
    ft.on("conflict", ({ conflict: c, answer }) => setConflict({ c, answer }));
    ft.on("close", ({ reason }) => {
      setConn((s) => (s === "ready" ? "error" : s === "idle" ? s : "error"));
      setConnError(reason);
      ftRef.current = null;
    });
    ft.connect().catch((e: Error) => {
      setConnError(e instanceof FileTransferError && e.code === "permission-denied" ? t("webclient.files.errors.permission-denied") : e.message);
      setConn("error");
    });
  }, [p.open, p.peerId, p.server, p.rememberedHash, p.myName, p.myId, t]);

  useEffect(
    () => () => {
      ftRef.current?.close("closed");
      ftRef.current = null;
    },
    [],
  );

  const upsertTransfer = (tr: Partial<Transfer> & { id: number }) =>
    setTransfers((list) => {
      const i = list.findIndex((x) => x.id === tr.id);
      if (i < 0) return [tr as Transfer, ...list].slice(0, 30);
      const next = [...list];
      next[i] = { ...next[i]!, ...tr };
      return next;
    });

  const track = (job: { id: number; done: Promise<unknown>; cancel: () => void }, label: string, direction: "download" | "upload", after?: () => void) => {
    upsertTransfer({ id: job.id, label, direction, progress: null, state: "running", cancel: job.cancel });
    job.done.then(
      () => {
        upsertTransfer({ id: job.id, state: "done" });
        after?.();
      },
      (e: unknown) => {
        const cancelled = e instanceof FileTransferError && e.code === "cancelled";
        upsertTransfer({ id: job.id, state: cancelled ? "cancelled" : "failed", message: cancelled ? undefined : (e as Error).message });
        if (!cancelled) fail(e);
      },
    );
  };

  const onProgress = (pr: TransferProgress) => upsertTransfer({ id: pr.jobId, progress: pr });

  const download = async (items: RemoteEntry[]) => {
    const ft = ftRef.current;
    if (!ft || !items.length) return;
    const picker = (window as unknown as { showDirectoryPicker?: ShowDirectoryPicker }).showDirectoryPicker;
    const needsFolder = items.length > 1 || items.some(isDir);
    if (needsFolder && picker) {
      let target: FileSystemDirectoryHandle;
      try {
        target = await picker({ mode: "readwrite" });
      } catch {
        return; // picker cancelled
      }
      for (const e of items) {
        const job = ft.download(ft.joinPath(cwd, e.name), { showHidden, onProgress });
        track(job, e.name, "download");
        void job
          .saveTo(async (file) => {
            const parts = file.relativePath.split("/").filter(Boolean);
            let d = target;
            for (const part of parts.slice(0, -1)) d = await d.getDirectoryHandle(part, { create: true });
            const fh = await d.getFileHandle(parts.at(-1) ?? file.name, { create: true });
            return fh.createWritable();
          })
          .catch(() => {});
      }
      return;
    }
    if (!needsFolder) {
      const e = items[0]!;
      const job = ft.download(ft.joinPath(cwd, e.name), { onProgress });
      track(job, e.name, "download");
      try {
        for await (const file of job) {
          const blob = await file.blob(4 * 1024 ** 3);
          saveBlob(blob, e.name);
        }
      } catch {
        /* reported by track() */
      }
      return;
    }
    // No folder picker (Safari, Firefox): one zip archive.
    const zip = new ZipWriter();
    const archive = items.length === 1 ? `${items[0]!.name}.zip` : `${p.peerId}-files.zip`;
    let failed = false;
    for (const e of items) {
      const job = ft.download(ft.joinPath(cwd, e.name), { showHidden, onProgress });
      track(job, e.name, "download");
      try {
        for await (const file of job) await zip.add(file.relativePath, file.modified, file);
        await job.done;
      } catch (err) {
        failed = true;
        job.cancel();
        if (!(err instanceof FileTransferError)) fail(err);
        break;
      }
    }
    if (!failed) saveBlob(zip.finish(), archive);
  };

  const upload = (items: UploadItem[]) => {
    const ft = ftRef.current;
    if (!ft || !items.length) return;
    const label = items.length === 1 ? (items[0]!.relativePath ?? items[0]!.name) : t("webclient.files.nFiles", { count: items.length });
    const job = ft.upload(items, cwd, { onProgress });
    track(job, label, "upload", () => void list(cwd));
  };

  const fromInput = (files: FileList | null) => {
    if (!files) return;
    upload(
      [...files].map((f) => ({
        name: f.name,
        relativePath: f.webkitRelativePath || f.name,
        size: f.size,
        lastModified: f.lastModified,
        data: f,
      })),
    );
  };

  const remove = async (items: RemoteEntry[]) => {
    const ft = ftRef.current;
    if (!ft) return;
    for (const e of items) {
      try {
        await ft.remove(ft.joinPath(cwd, e.name), { dir: isDir(e), recursive: true });
      } catch (err) {
        fail(err);
        break;
      }
    }
    void list(cwd);
  };

  const selectedEntries = useMemo(() => entries.filter((e) => selected.has(e.name)), [entries, selected]);
  const ready = conn === "ready";
  const visibleEntries = entries;

  return (
    <Sheet open={p.open} onOpenChange={p.onOpenChange}>
      <SheetContent className="sm:max-w-2xl" closeLabel={t("common.close")}>
        <SheetHeader>
          <SheetTitle>{t("webclient.files.title")}</SheetTitle>
          <SheetDescription className="truncate text-xs">{ready ? cwd || "/" : t(`webclient.files.state.${conn}`)}</SheetDescription>
        </SheetHeader>

        {ready && (
          <div className="flex flex-wrap items-center gap-1 border-b px-3 py-2">
            <Button variant="ghost" size="icon" className="h-8 w-8" title={t("webclient.files.up")} aria-label={t("webclient.files.up")} onClick={() => void list(ftRef.current!.parentPath(cwd))}>
              <ArrowUp className="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8" title={t("webclient.files.home")} aria-label={t("webclient.files.home")} onClick={() => void list("")}>
              <Home className="h-4 w-4" />
            </Button>
            {isWindows && (
              <Button variant="ghost" size="icon" className="h-8 w-8" title={t("webclient.files.drives")} aria-label={t("webclient.files.drives")} onClick={() => void list("/")}>
                <HardDrive className="h-4 w-4" />
              </Button>
            )}
            <form
              className="min-w-0 flex-1 basis-40"
              onSubmit={(e) => {
                e.preventDefault();
                void list(pathInput.trim());
              }}
            >
              <Input value={pathInput} onChange={(e) => setPathInput(e.target.value)} className="h-8 font-mono text-xs" aria-label={t("webclient.files.path")} />
            </form>
            <Button variant="ghost" size="icon" className="h-8 w-8" title={t("common.refresh")} aria-label={t("common.refresh")} onClick={() => void list(cwd)}>
              <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8"
              title={t("webclient.files.showHidden")}
              aria-label={t("webclient.files.showHidden")}
              aria-pressed={showHidden}
              onClick={() => {
                setShowHidden(!showHidden);
                void list(cwd, !showHidden);
              }}
            >
              {showHidden ? <Eye className="h-4 w-4" /> : <EyeOff className="h-4 w-4" />}
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8" title={t("webclient.files.newFolder")} aria-label={t("webclient.files.newFolder")} onClick={() => setNameDialog({ kind: "mkdir" })}>
              <FolderPlus className="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8" title={t("webclient.files.uploadFiles")} aria-label={t("webclient.files.uploadFiles")} onClick={() => fileInput.current?.click()}>
              <UploadIcon className="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8" title={t("webclient.files.uploadFolder")} aria-label={t("webclient.files.uploadFolder")} onClick={() => folderInput.current?.click()}>
              <FolderUp className="h-4 w-4" />
            </Button>
            <input ref={fileInput} type="file" multiple hidden onChange={(e) => (fromInput(e.target.files), (e.target.value = ""))} />
            <input
              ref={folderInput}
              type="file"
              hidden
              {...({ webkitdirectory: "" } as Record<string, string>)}
              onChange={(e) => (fromInput(e.target.files), (e.target.value = ""))}
            />
          </div>
        )}

        {ready && selectedEntries.length > 0 && (
          <div className="flex items-center gap-2 border-b bg-muted/40 px-4 py-1.5 text-sm">
            <span className="flex-1">{t("webclient.files.selected", { count: selectedEntries.length })}</span>
            <Button size="sm" variant="outline" onClick={() => void download(selectedEntries)}>
              <DownloadIcon className="mr-1.5 h-4 w-4" />
              {t("webclient.files.download")}
            </Button>
            <Button size="sm" variant="outline" className="text-destructive" onClick={() => setConfirmDelete(selectedEntries)}>
              <Trash2 className="mr-1.5 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </div>
        )}

        <SheetBody
          className={cn("p-0", dragOver && "bg-primary/5 ring-2 ring-inset ring-primary")}
          onDragOver={(e) => {
            if (!ready) return;
            e.preventDefault();
            setDragOver(true);
          }}
          onDragLeave={() => setDragOver(false)}
          onDrop={(e) => {
            if (!ready) return;
            e.preventDefault();
            setDragOver(false);
            void droppedItems(e.dataTransfer).then(upload, fail);
          }}
        >
          {!ready && (
            <ConnectPane
              state={conn}
              error={connError}
              wrong={pwWrong}
              secret={secret}
              setSecret={setSecret}
              onSubmit={() => {
                const ft = ftRef.current;
                if (!ft || !secret) return;
                if (conn === "twoFactor") ft.submitTwoFactor(secret);
                else void ft.login(secret);
                setSecret("");
                setConn("connecting");
              }}
              onRetry={() => {
                setConn("idle");
                setConnError("");
                ftRef.current = null;
                // reopen triggers a fresh connection
                p.onOpenChange(false);
                setTimeout(() => p.onOpenChange(true), 0);
              }}
            />
          )}
          {ready && (
            <ul className="divide-y" aria-busy={loading}>
              {visibleEntries.length === 0 && <li className="px-4 py-8 text-center text-sm text-muted-foreground">{t("webclient.files.empty")}</li>}
              {visibleEntries.map((e) => (
                <li key={e.name} className={cn("flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-muted/50", e.hidden && "opacity-60")}>
                  {e.type !== "drive" && (
                    <Checkbox
                      checked={selected.has(e.name)}
                      onCheckedChange={(v) =>
                        setSelected((s) => {
                          const n = new Set(s);
                          if (v === true) n.add(e.name);
                          else n.delete(e.name);
                          return n;
                        })
                      }
                      aria-label={e.name}
                    />
                  )}
                  <button
                    type="button"
                    className="flex min-w-0 flex-1 items-center gap-2 text-left"
                    onClick={() => (isDir(e) ? void list(ftRef.current!.joinPath(cwd, e.name)) : void download([e]))}
                    title={isDir(e) ? e.name : t("webclient.files.download")}
                  >
                    {e.type === "drive" ? (
                      <HardDrive className="h-4 w-4 shrink-0 text-muted-foreground" />
                    ) : isDir(e) ? (
                      <Folder className="h-4 w-4 shrink-0 text-amber-500" />
                    ) : (
                      <FileIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                    )}
                    <span className="min-w-0 flex-1 truncate">{e.name}</span>
                  </button>
                  <span className="hidden w-20 shrink-0 text-right text-xs tabular-nums text-muted-foreground sm:inline">{isDir(e) ? "" : formatSize(e.size, lang)}</span>
                  <span className="hidden w-32 shrink-0 text-right text-xs tabular-nums text-muted-foreground md:inline">
                    {e.modified ? new Date(e.modified).toLocaleString(lang, { dateStyle: "short", timeStyle: "short" }) : ""}
                  </span>
                  {e.type !== "drive" && (
                    <DropdownMenu modal={false}>
                      <DropdownMenuTrigger asChild>
                        <Button variant="ghost" size="icon" className="h-7 w-7 shrink-0" aria-label={t("common.more")}>
                          <MoreVertical className="h-4 w-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onSelect={() => void download([e])}>
                          <DownloadIcon className="mr-2 h-4 w-4" />
                          {t("webclient.files.download")}
                        </DropdownMenuItem>
                        <DropdownMenuItem onSelect={() => setNameDialog({ kind: "rename", entry: e })}>
                          <Pencil className="mr-2 h-4 w-4" />
                          {t("webclient.files.rename")}
                        </DropdownMenuItem>
                        <DropdownMenuItem className="text-destructive" onSelect={() => setConfirmDelete([e])}>
                          <Trash2 className="mr-2 h-4 w-4" />
                          {t("common.delete")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  )}
                </li>
              ))}
            </ul>
          )}
        </SheetBody>

        {transfers.length > 0 && (
          <div className="max-h-48 overflow-y-auto border-t px-3 py-2">
            <div className="mb-1 flex items-center justify-between text-xs font-medium text-muted-foreground">
              <span>{t("webclient.files.transfers")}</span>
              <button type="button" className="hover:underline" onClick={() => setTransfers((l) => l.filter((x) => x.state === "running"))}>
                {t("webclient.files.clearFinished")}
              </button>
            </div>
            <ul className="space-y-1.5">
              {transfers.map((tr) => {
                const pr = tr.progress;
                const pct = pr && pr.totalBytes > 0 ? Math.min(100, Math.round((pr.bytes / pr.totalBytes) * 100)) : tr.state === "done" ? 100 : 0;
                return (
                  <li key={tr.id} className="text-xs">
                    <div className="flex items-center gap-2">
                      {tr.direction === "download" ? <DownloadIcon className="h-3.5 w-3.5 shrink-0" /> : <UploadIcon className="h-3.5 w-3.5 shrink-0" />}
                      <span className="min-w-0 flex-1 truncate">{tr.label}</span>
                      <span className="shrink-0 tabular-nums text-muted-foreground">
                        {tr.state === "running"
                          ? pr
                            ? `${formatSize(pr.bytes, lang)} / ${formatSize(pr.totalBytes, lang)}${pr.files > 1 ? ` · ${pr.filesDone}/${pr.files}` : ""}`
                            : "…"
                          : t(`webclient.files.status.${tr.state}`)}
                      </span>
                      {tr.state === "running" && (
                        <button type="button" className="shrink-0 text-muted-foreground hover:text-foreground" aria-label={t("common.cancel")} onClick={tr.cancel}>
                          <X className="h-3.5 w-3.5" />
                        </button>
                      )}
                    </div>
                    <div className="mt-0.5 h-1 overflow-hidden rounded bg-muted">
                      <div className={cn("h-full transition-all", tr.state === "failed" ? "bg-destructive" : "bg-primary")} style={{ width: `${pct}%` }} />
                    </div>
                  </li>
                );
              })}
            </ul>
          </div>
        )}
      </SheetContent>

      <NameDialog
        state={nameDialog}
        onClose={() => setNameDialog(null)}
        onSubmit={async (name) => {
          const ft = ftRef.current;
          const d = nameDialog;
          setNameDialog(null);
          if (!ft || !d) return;
          try {
            if (d.kind === "mkdir") await ft.mkdir(ft.joinPath(cwd, name));
            else if (d.entry) await ft.rename(ft.joinPath(cwd, d.entry.name), name);
          } catch (e) {
            fail(e);
          }
          void list(cwd);
        }}
      />
      <Dialog open={!!confirmDelete} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("webclient.files.deleteTitle", { count: confirmDelete?.length ?? 0 })}</DialogTitle>
            <DialogDescription className="break-all">{confirmDelete?.map((e) => e.name).join(", ")}</DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2">
            <Button variant="outline" onClick={() => setConfirmDelete(null)}>
              {t("common.cancel")}
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                const items = confirmDelete ?? [];
                setConfirmDelete(null);
                void remove(items);
              }}
            >
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog open={!!conflict} onOpenChange={(o) => !o && conflict && (conflict.answer("skip"), setConflict(null))}>
        <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("webclient.files.conflictTitle")}</DialogTitle>
            <DialogDescription className="break-all">
              {conflict &&
                t("webclient.files.conflictDesc", {
                  name: conflict.c.file.relativePath,
                  remote: formatSize(conflict.c.remote.size, lang),
                  local: formatSize(conflict.c.file.size, lang),
                })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="flex-wrap gap-2">
            {(["skip", "skipAll", "overwrite", "overwriteAll"] as ConflictAnswer[]).map((a) => (
              <Button
                key={a}
                variant={a.startsWith("overwrite") ? "default" : "outline"}
                onClick={() => {
                  conflict?.answer(a);
                  setConflict(null);
                }}
              >
                {t(`webclient.files.conflict.${a}`)}
              </Button>
            ))}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Sheet>
  );
}

function ConnectPane({
  state,
  error,
  wrong,
  secret,
  setSecret,
  onSubmit,
  onRetry,
}: {
  state: ConnState;
  error: string;
  wrong: boolean;
  secret: string;
  setSecret: (v: string) => void;
  onSubmit: () => void;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  if (state === "password" || state === "twoFactor" || state === "waiting") {
    return (
      <form
        className="mx-auto max-w-sm space-y-3 p-6"
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit();
        }}
      >
        <p className="text-sm text-muted-foreground">
          {state === "twoFactor" ? t("webclient.twoFactorDesc") : state === "waiting" ? t("webclient.waitingForApproval") : t("webclient.files.passwordDesc")}
        </p>
        {wrong && <p className="text-sm text-destructive">{state === "twoFactor" ? t("webclient.twoFactorWrong") : t("webclient.wrongPassword")}</p>}
        <Input
          type={state === "twoFactor" ? "text" : "password"}
          inputMode={state === "twoFactor" ? "numeric" : undefined}
          autoComplete={state === "twoFactor" ? "one-time-code" : "off"}
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          aria-label={state === "twoFactor" ? t("webclient.twoFactorTitle") : t("webclient.passwordPlaceholder")}
          placeholder={state === "twoFactor" ? "123456" : t("webclient.passwordPlaceholder")}
          autoFocus
        />
        <Button type="submit" className="w-full" disabled={!secret}>
          {state === "twoFactor" ? t("webclient.verify") : t("webclient.connect")}
        </Button>
      </form>
    );
  }
  if (state === "error") {
    return (
      <div className="space-y-3 p-6 text-center">
        <p className="text-sm text-destructive">{error || t("webclient.files.state.error")}</p>
        <Button variant="outline" onClick={onRetry}>
          <RefreshCw className="mr-2 h-4 w-4" />
          {t("webclient.reconnect")}
        </Button>
      </div>
    );
  }
  return (
    <div className="flex items-center justify-center gap-2 p-10 text-sm text-muted-foreground">
      <Loader2 className="h-4 w-4 animate-spin" />
      {t("webclient.files.state.connecting")}
    </div>
  );
}

function NameDialog({
  state,
  onClose,
  onSubmit,
}: {
  state: { kind: "mkdir" | "rename"; entry?: RemoteEntry } | null;
  onClose: () => void;
  onSubmit: (name: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  useEffect(() => setName(state?.entry?.name ?? ""), [state]);
  const bad = !name.trim() || /[\\/]/.test(name) || name === "." || name === "..";
  return (
    <Dialog open={!!state} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-sm">
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (!bad) onSubmit(name.trim());
          }}
        >
          <DialogHeader>
            <DialogTitle>{state?.kind === "mkdir" ? t("webclient.files.newFolder") : t("webclient.files.rename")}</DialogTitle>
            <DialogDescription className="sr-only">{t("webclient.files.nameHint")}</DialogDescription>
          </DialogHeader>
          <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus aria-label={t("webclient.files.name")} />
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={bad}>
              {t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
