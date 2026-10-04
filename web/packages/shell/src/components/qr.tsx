import React, { useMemo } from 'react';
import qrcode from 'qrcode-generator';

/** QR code (member card, tickets, bookings) rendered as SVG — works offline. */
export function QRCode({ value, size = 200, label }: { value: string; size?: number; label?: string }) {
  const svg = useMemo(() => {
    const qr = qrcode(0, 'M');
    qr.addData(value);
    qr.make();
    const cells = qr.getModuleCount();
    const cell = Math.max(2, Math.floor(size / (cells + 8)));
    return qr.createSvgTag({ cellSize: cell, margin: cell * 4, scalable: true });
  }, [value, size]);
  return (
    <div role="img" aria-label={label ?? 'QR code'} style={{ width: size, height: size, background: '#fff', borderRadius: 12, padding: 4 }}
      dangerouslySetInnerHTML={{ __html: svg }} />
  );
}
