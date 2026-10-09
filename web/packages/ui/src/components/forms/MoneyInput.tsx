import { forwardRef, useLayoutEffect, useRef, type InputHTMLAttributes } from 'react';

/*
 * Money typed in Rupiah (demo feedback 10 Oct 2026 #13): the field shows
 * "Rp 500.000" while the user types — thousands with a dot (id-ID), a comma
 * for decimals when the currency has them — and hands the app the plain
 * number ("500000", "-25000", "1234.5"), which is what the API takes. The
 * caret stays after the same digit when a dot is inserted or removed.
 */

export interface MoneyFormat {
  /** Digits after the decimal comma (IDR: 0). */
  decimals?: number;
  /** Allow a minus sign (adjustments, credits). */
  allowNegative?: boolean;
}

/** Keeps digits, one decimal comma/point and a leading minus: the plain value. */
export function parseMoney(text: string, { decimals = 0, allowNegative = false }: MoneyFormat = {}): string {
  const negative = allowNegative && text.replace(/^\s*Rp\s*/i, '').trim().startsWith('-');
  let body = text.replace(/[^\d,]/g, '');
  let frac = '';
  if (decimals > 0) {
    const i = body.indexOf(',');
    if (i >= 0) {
      frac = body.slice(i + 1).replace(/,/g, '').slice(0, decimals);
      body = body.slice(0, i);
    }
  } else {
    body = body.replace(/,/g, '');
  }
  body = body.replace(/^0+(?=\d)/, '');
  const hasComma = decimals > 0 && text.includes(',');
  if (!body && !frac && !hasComma) return negative ? '-' : '';
  return `${negative ? '-' : ''}${body || '0'}${hasComma ? `.${frac}` : ''}`;
}

/** Formats a plain value ("500000", "-25000.5") as "500.000" / "-25.000,5". */
export function formatMoneyValue(value: string | number | null | undefined, { decimals = 0 }: MoneyFormat = {}): string {
  if (value === null || value === undefined || value === '') return '';
  const v = String(value);
  if (v === '-') return '-';
  const negative = v.startsWith('-');
  const [int, frac] = v.replace('-', '').split('.');
  const grouped = (int || '0').replace(/\B(?=(\d{3})+(?!\d))/g, '.');
  const tail = decimals > 0 && frac !== undefined ? `,${frac.slice(0, decimals)}` : '';
  return `${negative ? '-' : ''}${grouped}${tail}`;
}

export type MoneyInputProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'value' | 'onChange' | 'type' | 'prefix'> & MoneyFormat & {
  /** Plain value: digits, optional minus and decimal point. */
  value: string | number | null | undefined;
  onChange: (value: string) => void;
  /** Shown before the number (default "Rp"); empty for none. */
  prefix?: string;
};

/** The input of a money field: formatted while typing, plain value out. */
export const MoneyInput = forwardRef<HTMLInputElement, MoneyInputProps>(function MoneyInput(
  { value, onChange, prefix = 'Rp', decimals = 0, allowNegative = false, className, ...rest }, outer,
) {
  const inner = useRef<HTMLInputElement | null>(null);
  // digits left of the caret, restored after formatting
  const caret = useRef<number | null>(null);
  const shown = formatMoneyValue(value === null || value === undefined ? '' : String(value), { decimals });
  const text = shown === '' ? '' : `${prefix ? `${prefix} ` : ''}${shown}`;
  useLayoutEffect(() => {
    const el = inner.current;
    if (!el || caret.current === null || document.activeElement !== el) return;
    let digits = caret.current;
    let pos = 0;
    while (pos < text.length && digits > 0) {
      if (/[\d,-]/.test(text[pos])) digits -= 1;
      pos += 1;
    }
    if (caret.current === 0) pos = prefix ? Math.min(text.length, prefix.length + 1) : 0;
    el.setSelectionRange(pos, pos);
    caret.current = null;
  }, [text, prefix]);
  return (
    <input
      {...rest}
      ref={(el) => {
        inner.current = el;
        if (typeof outer === 'function') outer(el);
        else if (outer) outer.current = el;
      }}
      className={className}
      type="text"
      inputMode={decimals > 0 || allowNegative ? 'decimal' : 'numeric'}
      autoComplete="off"
      value={text}
      onChange={(e) => {
        const raw = e.target.value;
        const at = e.target.selectionStart ?? raw.length;
        caret.current = raw.slice(0, at).replace(/[^\d,-]/g, '').length;
        onChange(parseMoney(raw, { decimals, allowNegative }));
      }}
    />
  );
});
