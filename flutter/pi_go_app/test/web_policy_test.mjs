// Tests the policy of web/index.html against what Forge does in a browser.
// Run with: node --test test/web_policy_test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { test } from 'node:test';

const page = fs.readFileSync(new URL('../web/index.html', import.meta.url), 'utf8');
const policy = /http-equiv="Content-Security-Policy" content="([^"]*)"/.exec(page)[1];
const sources = Object.fromEntries(
  policy.split(';').map((directive) => directive.trim().split(/\s+/)).map(([name, ...values]) => [name, values]),
);

test('a picked image can be read', () => {
  // The browser gives a picked file a blob: address, and its bytes are read
  // by a request to it. 'self' does not stand for blob:, in any browser.
  assert.ok(sources['connect-src'].includes('blob:'));
});

test('the agent and the relay can be reached, and over TLS only', () => {
  assert.ok(sources['connect-src'].includes('wss:'));
  assert.ok(sources['connect-src'].includes('https:'));
  assert.ok(!sources['connect-src'].includes('ws:'));
  assert.ok(!sources['connect-src'].includes('http:'));
  assert.ok(!sources['connect-src'].includes('*'));
});

test('no script of another site runs, but the one of the renderer', () => {
  assert.deepEqual(sources['default-src'], ["'self'"]);
  const foreign = sources['script-src'].filter((source) => !source.startsWith("'"));
  assert.deepEqual(foreign, ['https://www.gstatic.com']);
});
