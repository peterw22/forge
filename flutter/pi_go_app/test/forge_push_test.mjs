// Tests web/forge_push.js, the code with which the service worker reads a
// notification. Run with: node --test test/forge_push_test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

vm.runInThisContext(
  fs.readFileSync(new URL('../web/forge_push.js', import.meta.url), 'utf8'),
);
const { decryptWith } = globalThis.forgePush;

// Encrypted by the agent; cmd/pi-go-agent/push_content_crypto_test.go holds
// the same envelope.
const message = {
  version: 1,
  eventId: 'event-identifier-1234',
  keyId: 'key-identifier-123456',
  nonce: 'QypfuyMwe3ZrsA93',
  ciphertext:
    'O32vvImZoAcD6DzxUlC4cluUAJFVCsWGVPGNR0AZmzUNKmKXXY_DI3Mkq-oPfh0I43dsZzxgrr0HyQzgDrqv' +
    'CJEmk1DMyxaiSYS8KT3keKtaS4IuRROHmoh4oRt0GX8epnqF0AfYHJCdltDjlI4sOhOuMzdSIS1tI5oRkoVZ' +
    'mFfo5FPydjd32QssKhAgoTGvR-NIw4OEJ6NolVLKSj8Xio4ghB5J',
};

async function record(overrides = {}) {
  const key = await crypto.subtle.importKey(
    'raw',
    Uint8Array.from({ length: 32 }, (_, index) => index + 1),
    'AES-GCM',
    false,
    ['decrypt'],
  );
  return { agentId: 'agent-identifier-12345', deviceId: 'device-identifier-123', key, ...overrides };
}

test('reads a notification that the agent encrypted', async () => {
  assert.deepEqual(await decryptWith(message, await record()), {
    agentId: 'agent-identifier-12345',
    eventId: 'event-identifier-1234',
    type: 'approval_required',
    sessionId: 'private-session-name',
    title: 'Forge approval required',
    body: 'This operation may delete workspace files.',
  });
});

test('reads the version as the relay and as Android send it', async () => {
  assert.equal((await decryptWith({ ...message, version: '1' }, await record())).type, 'approval_required');
});

test('refuses a notification that was altered or is for another', async () => {
  const flipped = (message.ciphertext[0] === 'A' ? 'B' : 'A') + message.ciphertext.slice(1);
  await assert.rejects(decryptWith({ ...message, ciphertext: flipped }, await record()));
  await assert.rejects(decryptWith({ ...message, eventId: 'event-identifier-9999' }, await record()));
  await assert.rejects(decryptWith(message, await record({ deviceId: 'device-identifier-999' })));
  await assert.rejects(decryptWith(message, await record({ agentId: 'agent-identifier-99999' })));
});

test('refuses a notification of the wrong form', async () => {
  await assert.rejects(decryptWith({ ...message, version: 2 }, await record()));
  await assert.rejects(decryptWith({ ...message, keyId: 'short' }, await record()));
  await assert.rejects(decryptWith({ ...message, nonce: 'AAAA' }, await record()));
  await assert.rejects(decryptWith({ ...message, ciphertext: 'not base64url!' }, await record()));
  await assert.rejects(decryptWith(null, await record()));
});

test('refuses content that is not a notification of Forge', async () => {
  const key = await crypto.subtle.importKey(
    'raw',
    Uint8Array.from({ length: 32 }, (_, index) => index + 1),
    'AES-GCM',
    false,
    ['encrypt', 'decrypt'],
  );
  const seal = async (content) => {
    const nonce = crypto.getRandomValues(new Uint8Array(12));
    const associated = ['FORGE-PUSH-CONTENT-V1', 'agent-identifier-12345', 'device-identifier-123',
      message.eventId, message.keyId].join('\n');
    const sealed = await crypto.subtle.encrypt(
      { name: 'AES-GCM', iv: nonce, additionalData: new TextEncoder().encode(associated) },
      key,
      new TextEncoder().encode(JSON.stringify(content)),
    );
    const encode = (bytes) => Buffer.from(bytes).toString('base64url');
    return { ...message, nonce: encode(nonce), ciphertext: encode(sealed) };
  };
  const content = { type: 'session_completed', sessionId: 's', title: 'Forge session completed', body: 'Done.' };
  assert.equal((await decryptWith(await seal(content), await record({ key }))).body, 'Done.');
  await assert.rejects(decryptWith(await seal({ ...content, type: 'other' }), await record({ key })));
  await assert.rejects(decryptWith(await seal({ ...content, title: '' }), await record({ key })));
  await assert.rejects(decryptWith(await seal({ ...content, body: 'x'.repeat(301) }), await record({ key })));
});
