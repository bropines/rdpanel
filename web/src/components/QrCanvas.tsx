import QRCode from "qrcode";
import { useEffect, useRef } from "react";

/** Draws value as a QR code on a canvas (no data: URLs, CSP-friendly). */
export function QrCanvas({ value, size = 192, label = "QR" }: { value: string; size?: number; label?: string }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    if (!ref.current) return;
    void QRCode.toCanvas(ref.current, value, { width: size, margin: 1, errorCorrectionLevel: "M" }).catch(() => undefined);
  }, [value, size]);
  return <canvas ref={ref} width={size} height={size} className="rounded-md border bg-white" aria-label={label} role="img" />;
}
