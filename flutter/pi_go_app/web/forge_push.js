// Reads the encrypted notifications of an agent for the service worker. It is
// kept apart from the worker so that a test runs the same code.
(() => {
  const DATABASE = 'forge-push-content';
  const KEYS = 'keys';
  const STATE = 'state';
  const IDENTIFIER = /^[A-Za-z0-9_-]{16,128}$/;
  const TYPES = ['approval_required', 'session_completed'];

  function decodeBase64Url(value) {
    if (typeof value !== 'string' || !/^[A-Za-z0-9_-]*$/.test(value)) {
      throw new Error('invalid encoding');
    }
    const padded = value.replaceAll('-', '+').replaceAll('_', '/')
      .padEnd(Math.ceil(value.length / 4) * 4, '=');
    return Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
  }

  // Why a notification is shown without its content. It names no key, no
  // session and nothing of the message.
  class Unread extends Error {
    constructor(reason) {
      super(reason);
      this.reason = reason;
    }
  }

  function requireIdentifier(value) {
    if (typeof value !== 'string' || !IDENTIFIER.test(value)) throw new Error('invalid identifier');
    return value;
  }

  function requireText(value, maximum) {
    if (typeof value !== 'string' || value.length === 0 || value.length > maximum) {
      throw new Error('invalid text');
    }
    return value;
  }

  // The key is kept as bytes. A key object is wrapped by the browser when it
  // is stored, and Safari on a locked iPhone cannot unwrap it, which is when
  // most notifications arrive. A record of an earlier version holds the object.
  function keyOf(record) {
    if (record.raw === undefined) return record.key;
    return crypto.subtle.importKey('raw', record.raw, { name: 'AES-GCM' }, false, ['decrypt']);
  }

  // Decrypts one message with the key it names. `record` holds the key and the
  // agent and device it was provisioned for, which the ciphertext is bound to.
  async function decryptWith(message, record) {
    if (message === null || typeof message !== 'object' || Number(message.version) !== 1) {
      throw new Error('unsupported version');
    }
    const keyId = requireIdentifier(message.keyId);
    const eventId = requireIdentifier(message.eventId);
    const agentId = requireIdentifier(record.agentId);
    const deviceId = requireIdentifier(record.deviceId);
    const nonce = decodeBase64Url(message.nonce);
    const ciphertext = decodeBase64Url(message.ciphertext);
    if (nonce.length !== 12) throw new Error('invalid nonce');
    if (ciphertext.length < 17 || ciphertext.length > 8192) throw new Error('invalid size');
    const associated = ['FORGE-PUSH-CONTENT-V1', agentId, deviceId, eventId, keyId].join('\n');
    const plaintext = new Uint8Array(await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv: nonce, additionalData: new TextEncoder().encode(associated) },
      await keyOf(record),
      ciphertext,
    ));
    if (plaintext.length > 4096) throw new Error('invalid size');
    const content = JSON.parse(new TextDecoder().decode(plaintext));
    if (!TYPES.includes(content.type)) throw new Error('unsupported event');
    return {
      agentId,
      eventId,
      type: content.type,
      sessionId: requireText(content.sessionId, 200),
      title: requireText(content.title, 100),
      body: requireText(content.body, 300),
    };
  }

  function openDatabase() {
    return new Promise((resolve, reject) => {
      const request = indexedDB.open(DATABASE, 1);
      request.onupgradeneeded = () => {
        for (const store of [KEYS, STATE]) {
          if (!request.result.objectStoreNames.contains(store)) request.result.createObjectStore(store);
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  }

  async function transact(store, mode, action) {
    const database = await openDatabase();
    try {
      return await new Promise((resolve, reject) => {
        const request = action(database.transaction(store, mode).objectStore(store));
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
    } finally {
      database.close();
    }
  }

  async function decrypt(message) {
    let keyId;
    try {
      keyId = requireIdentifier(message === null || typeof message !== 'object' ? '' : message.keyId);
    } catch (_) {
      throw new Unread('it could not be decrypted');
    }
    let record;
    try {
      record = await transact(KEYS, 'readonly', (store) => store.get(keyId));
    } catch (_) {
      throw new Unread('its key could not be read');
    }
    if (record === undefined || record === null) throw new Unread('this browser has no key for it');
    try {
      return await decryptWith(message, record);
    } catch (_) {
      throw new Unread('it could not be decrypted');
    }
  }

  const writeState = (name, value) => transact(STATE, 'readwrite', (store) => store.put(value, name));

  globalThis.forgePush = { decrypt, decryptWith, writeState };
})();
