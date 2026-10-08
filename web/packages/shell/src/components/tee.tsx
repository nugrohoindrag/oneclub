import React from 'react';

// Player / tee colour classification (demo feedback 9 Oct 2026): red tee
// for women, blue and white for general play, black for professionals.

export const TEE_CATEGORY: Record<string, string> = { women: 'Women', general: 'General', professional: 'Professional', senior: 'Senior', junior: 'Junior' };
const TEE_HEX: Record<string, string> = { red: '#d32f2f', blue: '#1565c0', white: '#ffffff', black: '#111111', gold: '#c9a227', yellow: '#f2c200', green: '#2e7d32' };

/** The hex of a tee colour name (grey when unknown). */
export const teeHex = (color?: string | null) => TEE_HEX[(color ?? '').toLowerCase()] ?? '#8a8a8a';

/** "● Red · Women" */
export function TeeBadge({ color, category, name }: { color?: string | null; category?: string | null; name?: string | null }) {
  if (!color && !name) return null;
  const label = name ?? (color ? color[0]!.toUpperCase() + color.slice(1) : '');
  return (
    <span className="oc-tee" title={category ? `${label} tee · ${TEE_CATEGORY[category] ?? category}` : `${label} tee`}>
      <i style={{ background: teeHex(color) }} aria-hidden />{label}{category ? ` · ${TEE_CATEGORY[category] ?? category}` : ''}
    </span>
  );
}
