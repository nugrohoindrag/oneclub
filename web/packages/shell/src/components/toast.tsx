import React, { createContext, useCallback, useContext, useState } from 'react';

type ToastType = 'success' | 'error' | 'info';
interface ToastMsg {
  id: number;
  type: ToastType;
  text: string;
}

const Ctx = createContext<(text: string, type?: ToastType) => void>(() => undefined);

/** Lightweight toasts (role="status" so screen readers announce them). */
export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<ToastMsg[]>([]);
  const push = useCallback((text: string, type: ToastType = 'success') => {
    const id = Date.now() + Math.random();
    setItems((x) => [...x, { id, type, text }]);
    setTimeout(() => setItems((x) => x.filter((t) => t.id !== id)), type === 'error' ? 6000 : 3500);
  }, []);
  return (
    <Ctx.Provider value={push}>
      {children}
      <div className="oc-toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className="oc-toast" data-type={t.type}>
            {t.text}
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}

export function useToast() {
  return useContext(Ctx);
}
