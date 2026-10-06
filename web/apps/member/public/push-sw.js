// Push notifications of the installed app (PRD P5 FR-INT-P5-05), imported by the generated service worker.
// The payload is { title, body, link, tag } (notification service, channel "push"); nothing is cached here.
self.addEventListener('push', (event) => {
  let d = {};
  try {
    d = event.data ? event.data.json() : {};
  } catch (e) {
    d = { title: 'OneClub', body: event.data ? event.data.text() : '' };
  }
  event.waitUntil(self.registration.showNotification(d.title || 'OneClub', {
    body: d.body || '', tag: d.tag || undefined, icon: '/favicon.svg', badge: '/favicon.svg', data: { link: d.link || '/' },
  }));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const link = (event.notification.data && event.notification.data.link) || '/';
  const target = new URL(link, self.location.origin);
  event.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
    for (const c of list) {
      if ('focus' in c && new URL(c.url).origin === target.origin) {
        if ('navigate' in c) c.navigate(target.href).catch(() => undefined);
        return c.focus();
      }
    }
    return self.clients.openWindow(target.href);
  }));
});
