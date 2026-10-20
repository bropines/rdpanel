// Minimal ZIP writer (stored, no compression) for folder downloads in
// browsers without the File System Access API. Entries are streamed into
// Blob parts; sizes and CRCs go into data descriptors, so nothing has to be
// known up front. No ZIP64: archives are limited to 4 GiB.

const LIMIT = 0xffff_ffff;

let crcTable: Uint32Array | null = null;
function table(): Uint32Array {
  if (crcTable) return crcTable;
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  crcTable = t;
  return t;
}

export function crc32(data: Uint8Array, crc = 0): number {
  const t = table();
  let c = (crc ^ 0xffffffff) >>> 0;
  for (let i = 0; i < data.length; i++) c = t[(c ^ data[i]!) & 0xff]! ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function dosTime(ms: number): { time: number; date: number } {
  const d = new Date(ms > 0 ? ms : Date.now());
  const year = Math.max(1980, d.getFullYear());
  return {
    time: (d.getHours() << 11) | (d.getMinutes() << 5) | (d.getSeconds() >> 1),
    date: ((year - 1980) << 9) | ((d.getMonth() + 1) << 5) | d.getDate(),
  };
}

interface Entry {
  name: Uint8Array;
  crc: number;
  size: number;
  offset: number;
  time: number;
  date: number;
}

export class ZipTooLargeError extends Error {
  constructor() {
    super("zip: archive exceeds 4 GiB");
    this.name = "ZipTooLargeError";
  }
}

export class ZipWriter {
  private parts: BlobPart[] = [];
  private entries: Entry[] = [];
  private offset = 0;
  private readonly enc = new TextEncoder();

  private push(b: Uint8Array): void {
    if (this.offset + b.length > LIMIT) throw new ZipTooLargeError();
    this.parts.push(b as Uint8Array<ArrayBuffer>);
    this.offset += b.length;
  }

  /** Adds a file; `chunks` is read to the end. */
  async add(path: string, modified: number, chunks: AsyncIterable<Uint8Array>): Promise<void> {
    const name = this.enc.encode(path.replace(/^\/+/, ""));
    const { time, date } = dosTime(modified);
    const offset = this.offset;
    const h = new DataView(new ArrayBuffer(30));
    h.setUint32(0, 0x04034b50, true);
    h.setUint16(4, 20, true); // version needed
    h.setUint16(6, 0x0808, true); // data descriptor + UTF-8 names
    h.setUint16(8, 0, true); // stored
    h.setUint16(10, time, true);
    h.setUint16(12, date, true);
    h.setUint16(26, name.length, true);
    this.push(new Uint8Array(h.buffer));
    this.push(name);
    let crc = 0;
    let size = 0;
    for await (const c of chunks) {
      crc = crc32(c, crc);
      size += c.length;
      this.push(c);
    }
    const dd = new DataView(new ArrayBuffer(16));
    dd.setUint32(0, 0x08074b50, true);
    dd.setUint32(4, crc, true);
    dd.setUint32(8, size, true);
    dd.setUint32(12, size, true);
    this.push(new Uint8Array(dd.buffer));
    this.entries.push({ name, crc, size, offset, time, date });
  }

  finish(): Blob {
    const start = this.offset;
    for (const e of this.entries) {
      const c = new DataView(new ArrayBuffer(46));
      c.setUint32(0, 0x02014b50, true);
      c.setUint16(4, 20, true); // made by
      c.setUint16(6, 20, true); // needed
      c.setUint16(8, 0x0808, true);
      c.setUint16(10, 0, true);
      c.setUint16(12, e.time, true);
      c.setUint16(14, e.date, true);
      c.setUint32(16, e.crc, true);
      c.setUint32(20, e.size, true);
      c.setUint32(24, e.size, true);
      c.setUint16(28, e.name.length, true);
      c.setUint32(42, e.offset, true);
      this.push(new Uint8Array(c.buffer));
      this.push(e.name);
    }
    const end = new DataView(new ArrayBuffer(22));
    end.setUint32(0, 0x06054b50, true);
    end.setUint16(8, this.entries.length, true);
    end.setUint16(10, this.entries.length, true);
    end.setUint32(12, this.offset - start, true);
    end.setUint32(16, start, true);
    this.push(new Uint8Array(end.buffer));
    const blob = new Blob(this.parts, { type: "application/zip" });
    this.parts = [];
    return blob;
  }
}
