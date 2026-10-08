'use client';

import { useEffect } from 'react';
import { usePathname } from 'next/navigation';

/** ID / EN of the live site, keeping the current page. */
export function LangSwitch({ lang }: { lang: 'en' | 'id' }) {
  const path = usePathname() ?? `/${lang}`;
  const rest = path.replace(/^\/(en|id)(?=\/|$)/, '');
  return (
    <div className="lang-bt">
      <a href={`/id${rest}`} className={lang === 'id' ? 'active' : ''}>ID</a> /{' '}
      <a href={`/en${rest}`} className={lang === 'en' ? 'active' : ''}>EN</a>
    </div>
  );
}

const loaded = new Map<string, Promise<void>>();

function load(src: string): Promise<void> {
  let p = loaded.get(src);
  if (!p) {
    p = new Promise((resolve, reject) => {
      const s = document.createElement('script');
      s.src = src;
      s.async = false;
      s.onload = () => resolve();
      s.onerror = () => reject(new Error(src));
      document.body.appendChild(s);
    });
    loaded.set(src, p);
  }
  return p;
}

/**
 * The live site's scripts (jQuery, Owl Carousel, main.js and the page init:
 * sliders, burger menu, sticky header, accordions, tabs), loaded in order
 * after React has hydrated the page so jQuery never races React.
 */
export function ThemeScripts() {
  useEffect(() => {
    const base = '/themes/modern-golf/assets/js/';
    void (async () => {
      for (const f of ['jquery-1.11.0.min.js', 'owl.carousel.min.js', 'main.js', 'init.js']) await load(base + f);
    })();
  }, []);
  return null;
}

/**
 * The live site's forms (Contact) post to the OneClub API: the message is
 * logged as a customer interaction (Contact form, PRD P2 §6 #14).
 */
export function ContactForms({ propertyId, lang }: { propertyId: string; lang: string }) {
  useEffect(() => {
    const forms = Array.from(document.querySelectorAll<HTMLFormElement>('form[data-request$="onFormSubmit"]'));
    const handlers = forms.map((form) => {
      const h = async (e: SubmitEvent) => {
        e.preventDefault();
        e.stopImmediatePropagation();
        const v = (n: string) => (form.elements.namedItem(n) as HTMLInputElement | null)?.value?.trim() ?? '';
        const flash = form.querySelector<HTMLElement>('.form-flash');
        const say = (msg: string, ok: boolean) => {
          if (flash) flash.innerHTML = `<div class="notification ${ok ? 'is-success' : 'is-danger'}">${msg}</div>`;
        };
        const btn = form.querySelector<HTMLButtonElement>('button');
        if (btn) btn.disabled = true;
        try {
          const r = await fetch('/api/v1/public/contact', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ propertyId, guest: { name: v('name'), email: v('email'), phone: v('phone') }, topic: v('subject') || 'other',
              message: [v('subject'), v('message')].filter(Boolean).join('\n\n') }),
          });
          if (!r.ok) throw new Error(String(r.status));
          form.reset();
          say(lang === 'id' ? 'Terima kasih, pesan Anda sudah kami terima.' : 'Thank you, we have received your message.', true);
        } catch {
          say(lang === 'id' ? 'Pesan belum terkirim. Lengkapi nama dan e-mail, lalu coba lagi.' : 'Your message was not sent. Fill in your name and e-mail and try again.', false);
        } finally {
          if (btn) btn.disabled = false;
        }
      };
      form.addEventListener('submit', h);
      return () => form.removeEventListener('submit', h);
    });
    return () => handlers.forEach((off) => off());
  }, [propertyId, lang]);
  return null;
}

/** The weather widget of the home hero (weatherwidget.io, as on the live site). */
export function WeatherScript() {
  useEffect(() => {
    void load('https://weatherwidget.io/js/widget.min.js').catch(() => undefined);
  }, []);
  return null;
}
