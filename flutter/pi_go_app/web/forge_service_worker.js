importScripts('./forge_push.js?v=14');

const CACHE_NAME = 'forge-web-v14';
const APP_SHELL = [
  './',
  './index.html',
  './flutter_bootstrap.js?v=14',
  './flutter.js',
  './forge_push.js?v=14',
  // The WebAssembly build runs where supported; main.dart.js is the fallback.
  './main.dart.wasm?v=14',
  './main.dart.mjs?v=14',
  './main.dart.js?v=14',
  './manifest.json',
  './version.json',
  './favicon.png',
  './icons/Icon-192.png',
  './icons/Icon-512.png',
  './icons/Icon-maskable-192.png',
  './icons/Icon-maskable-512.png',
  './assets/AssetManifest.bin',
  './assets/AssetManifest.bin.json',
  './assets/FontManifest.json',
  './assets/NOTICES',
  './assets/fonts/MaterialIcons-Regular.otf',
  './assets/packages/cupertino_icons/assets/CupertinoIcons.ttf',
  './assets/shaders/ink_sparkle.frag',
  './assets/shaders/stretch_effect.frag',
  './canvaskit/skwasm.js',
  './canvaskit/skwasm.wasm',
  './canvaskit/canvaskit.js',
  './canvaskit/canvaskit.wasm',
  './canvaskit/chromium/canvaskit.js',
  './canvaskit/chromium/canvaskit.wasm',
];

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE_NAME).then((cache) => cache.addAll(APP_SHELL)));
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((key) => key !== CACHE_NAME).map((key) => caches.delete(key))),
    ),
  );
  self.clients.claim();
});

self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET') return;
  const url = new URL(event.request.url);

  // Cache Google Font CSS/files after first successful load. The app itself
  // remains usable with system fallback if fonts were never fetched.
  if (url.origin === 'https://fonts.googleapis.com' || url.origin === 'https://fonts.gstatic.com') {
    event.respondWith(
      caches.match(event.request).then((cached) => {
        if (cached) return cached;
        return fetch(event.request).then((response) => {
          if (response.ok) {
            const copy = response.clone();
            caches.open(CACHE_NAME).then((cache) => cache.put(event.request, copy));
          }
          return response;
        });
      }),
    );
    return;
  }

  if (url.origin !== self.location.origin) return;

  // Always check the network for navigations so an installed PWA discovers a
  // new release without requiring site-data deletion. Offline launches still
  // fall back to the precached shell.
  if (event.request.mode === 'navigate') {
    event.respondWith(
      fetch(event.request)
        .then((response) => {
          if (response.ok) {
            const copy = response.clone();
            caches.open(CACHE_NAME).then((cache) => cache.put('./index.html', copy));
          }
          return response;
        })
        .catch(() => caches.match('./index.html')),
    );
    return;
  }

  event.respondWith(
    caches.match(event.request).then((cached) => {
      if (cached) return cached;
      return fetch(event.request).then((response) => {
        if (response.ok) {
          const copy = response.clone();
          caches.open(CACHE_NAME).then((cache) => cache.put(event.request, copy));
        }
        return response;
      }).catch(() => {
        if (event.request.mode === 'navigate') return caches.match('./index.html');
        throw new Error('offline asset unavailable');
      });
    }),
  );
});

self.addEventListener('push', (event) => {
  event.waitUntil(showPush(event.data));
});

// A browser requires a notification for every push, and withdraws the
// subscription of a site that shows none. A message that cannot be read is
// therefore shown without content, and none is held back for the session on
// screen.
async function showPush(data) {
  let title = 'Forge';
  let body = 'Encrypted notification';
  let tag;
  let tap = null;
  try {
    const push = await forgePush.decrypt(data.json());
    title = push.title;
    body = push.body;
    tag = push.eventId;
    tap = { agentId: push.agentId, sessionId: push.sessionId };
  } catch (_) {
    // Never log ciphertext, key identifiers, routing metadata, or plaintext.
  }
  await self.registration.showNotification(title, {
    body,
    tag,
    icon: './icons/Icon-192.png',
    data: tap,
  });
}

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(openForge(event.notification.data));
});

// The tap is left in the database for Forge to collect, which also reaches a
// window that is opened only now.
async function openForge(tap) {
  if (tap) await forgePush.writeState('tap', { ...tap, at: Date.now() });
  const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
  for (const client of windows) {
    if ('focus' in client) {
      await client.focus();
      client.postMessage({ type: 'forge-notification-tap' });
      return;
    }
  }
  await self.clients.openWindow('./');
}
