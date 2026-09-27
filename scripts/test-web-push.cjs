#!/usr/bin/env node
// Tests notifications in a browser from end to end, in a demo of its own.
//
//     node scripts/test-web-push.cjs [--headed] [--keep] [--home <directory>]
//
// It starts a relay with a database and a key of its own, an agent with its
// own configuration, and Chrome with an empty profile, and runs the web
// client in it: flutter/pi_go_app/tool/web_push/main.dart turns notifications
// on, pairs with the agent and has it finish a turn. The notification then
// travels as it does for a user: agent, relay, the push service of Chrome,
// the service worker. Nothing of your own agent, relay or browser is touched.
//
// The message passes through Google's push service, encrypted twice. The
// demo agent uses Claude Code for one short turn, so `claude` must be
// installed and signed in, and the run uses that account.
//
// Needs macOS with Chrome, Flutter, Go, Node 22 and the packages of
// worker-push (npm install).
const { spawn, execFileSync } = require('node:child_process');
const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const net = require('node:net');
const path = require('node:path');
const tls = require('node:tls');

const ROOT = path.resolve(__dirname, '..');
const APP = path.join(ROOT, 'flutter', 'pi_go_app');
const RELAY = path.join(ROOT, 'worker-push');
const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const PORT = { agent: 17446, agentTls: 17447, site: 17448, relay: 17449, chrome: 17450, relayTls: 17451 };
const ORIGIN = `http://127.0.0.1:${PORT.site}`;
const RELAY_URL = `http://127.0.0.1:${PORT.relay}`;
const TYPES = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.mjs': 'text/javascript',
  '.json': 'application/json', '.wasm': 'application/wasm', '.png': 'image/png',
  '.otf': 'font/otf', '.ttf': 'font/ttf',
};

const options = { home: '/private/tmp/forge-push-demo', model: 'claude/claude-sonnet-5', headed: false, keep: false };
for (let index = 2; index < process.argv.length; index += 1) {
  const name = process.argv[index].replace(/^--/, '');
  if (!(name in options)) throw new Error(`unknown option ${process.argv[index]}`);
  if (typeof options[name] === 'boolean') options[name] = true;
  else options[name] = process.argv[index += 1];
}
const HOME = path.resolve(options.home);
const children = [];
const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));
const log = (...values) => console.log(...values);

function start(name, command, parameters, settings = {}) {
  const output = fs.openSync(path.join(HOME, `${name}.log`), 'a');
  // A group of its own, so that what the process starts ends with it.
  const child = spawn(command, parameters, { stdio: ['ignore', output, output], detached: true, ...settings });
  child.on('exit', (code) => { child.ended = code ?? 1; });
  children.push(child);
  return child;
}

function stopAll() {
  for (const child of children) {
    if (child.ended !== undefined) continue;
    try { process.kill(-child.pid, 'SIGTERM'); } catch {}
  }
}

for (const signal of ['SIGINT', 'SIGTERM', 'SIGALRM', 'SIGHUP']) {
  process.on(signal, () => {
    stopAll();
    process.exit(1);
  });
}

async function reachable(url, seconds, what) {
  const deadline = Date.now() + seconds * 1000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return response;
    } catch {}
    await sleep(300);
  }
  throw new Error(`${what} did not start; see ${HOME}`);
}

function wrangler(parameters) {
  return execFileSync('npx', ['wrangler', ...parameters, '--config', path.join(HOME, 'relay', 'wrangler.toml')], {
    cwd: RELAY, env: { ...process.env, CI: '1', WRANGLER_SEND_METRICS: 'false' },
    stdio: ['ignore', 'pipe', 'pipe'], encoding: 'utf8',
  });
}

// The relay of the repository, with a key and a database that exist only here.
async function startRelay() {
  const directory = path.join(HOME, 'relay');
  fs.mkdirSync(directory, { recursive: true });
  const { privateKey, publicKey } = crypto.generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const open = publicKey.export({ format: 'jwk' });
  const point = Buffer.concat([Buffer.of(4), Buffer.from(open.x, 'base64url'), Buffer.from(open.y, 'base64url')]);
  const configuration = fs.readFileSync(path.join(RELAY, 'wrangler.toml'), 'utf8')
    .replace(/^name = .*$/m, 'name = "forge-push-demo"')
    .replace(/^main = .*$/m, `main = ${JSON.stringify(path.join(RELAY, 'src', 'index.ts'))}`)
    .replace(/^VAPID_PUBLIC_KEY = .*$/m, `VAPID_PUBLIC_KEY = "${point.toString('base64url')}"`)
    .replace(/^database_id = .*$/m,
      `database_id = "00000000-0000-0000-0000-000000000000"\nmigrations_dir = ${JSON.stringify(path.join(RELAY, 'migrations'))}`)
    .replace(/\[triggers\][\s\S]*$/, '');
  fs.writeFileSync(path.join(directory, 'wrangler.toml'), configuration);
  fs.writeFileSync(path.join(directory, '.dev.vars'), [
    `VAPID_PRIVATE_KEY="${privateKey.export({ format: 'jwk' }).d}"`,
    `PUSH_TOKEN_ENCRYPTION_KEY="${crypto.randomBytes(32).toString('base64url')}"`,
    '',
  ].join('\n'), { mode: 0o600 });
  const state = path.join(directory, 'state');
  wrangler(['d1', 'migrations', 'apply', 'forge-push', '--local', '--persist-to', state]);
  start('relay', 'npx', [
    'wrangler', 'dev', '--config', path.join(directory, 'wrangler.toml'), '--ip', '127.0.0.1',
    '--port', String(PORT.relay), '--persist-to', state, '--show-interactive-dev-session=false',
  ], { cwd: RELAY, env: { ...process.env, CI: '1', WRANGLER_SEND_METRICS: 'false' } });
  await reachable(`${RELAY_URL}/healthz`, 60, 'the demo relay');
  const key = await (await fetch(`${RELAY_URL}/v1/web-push-key`)).json();
  if (key.publicKey !== point.toString('base64url')) throw new Error('the demo relay has another key');
  return state;
}

function rows(state, query) {
  const output = wrangler(['d1', 'execute', 'forge-push', '--local', '--persist-to', state, '--json', '--command', query]);
  return JSON.parse(output.slice(output.indexOf('[')))[0].results;
}

function prepareAgent() {
  const configuration = path.join(HOME, 'config');
  const workspace = path.join(HOME, 'workspace');
  fs.mkdirSync(configuration, { recursive: true, mode: 0o700 });
  fs.mkdirSync(workspace);
  fs.writeFileSync(path.join(workspace, 'README.md'), '# Demo\n');
  fs.writeFileSync(path.join(configuration, 'classifier.json'),
    JSON.stringify({ version: 1, model: options.model }), { mode: 0o600 });
  execFileSync('go', ['build', '-o', path.join(HOME, 'pi-go-agent'), './cmd/pi-go-agent'], { cwd: ROOT, stdio: 'inherit' });
  return { configuration, workspace };
}

function startAgent(agent, device) {
  fs.writeFileSync(path.join(agent.configuration, 'authorized-devices.json'),
    JSON.stringify({ version: 1, devices: [device] }, null, 2), { mode: 0o600 });
  return start('agent', path.join(HOME, 'pi-go-agent'), [
    '--listen', `ws://127.0.0.1:${PORT.agent}/ws`, '--cwd', agent.workspace, '--model', options.model,
  ], { env: { ...process.env, PI_GO_CONFIG_DIR: agent.configuration, PI_GO_PUSH_RELAY_URL: RELAY_URL } });
}

// The web client speaks to its agent and its relay over TLS only. The demo
// browser accepts the certificate that is made here, and no other browser is
// asked to.
function makeCertificate() {
  const key = path.join(HOME, 'tls-key.pem');
  const certificate = path.join(HOME, 'tls-certificate.pem');
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
    '-subj', '/CN=127.0.0.1', '-keyout', key, '-out', certificate], { stdio: 'ignore' });
  return { key: fs.readFileSync(key), cert: fs.readFileSync(certificate) };
}

function startTls(certificate, port, target) {
  const server = tls.createServer(certificate, (socket) => {
    const upstream = net.connect(target, '127.0.0.1');
    socket.on('error', () => upstream.destroy());
    upstream.on('error', () => socket.destroy());
    socket.pipe(upstream).pipe(socket);
  });
  return new Promise((resolve) => server.listen(port, '127.0.0.1', () => resolve(server)));
}

function buildSite() {
  const site = path.join(HOME, 'site');
  execFileSync('flutter', [
    'build', 'web', '--wasm', '--release', '-t', 'tool/web_push/main.dart', '-o', site,
    `--dart-define=FORGE_PUSH_RELAY_URL=https://127.0.0.1:${PORT.relayTls}`,
    `--dart-define=DEMO_ENDPOINT=wss://127.0.0.1:${PORT.agentTls}/ws`,
    `--dart-define=DEMO_REPORT=${ORIGIN}`,
  ], { cwd: APP, stdio: ['ignore', fs.openSync(path.join(HOME, 'build.log'), 'a'), 'inherit'] });
  return site;
}

function serve(site, heard) {
  const server = http.createServer((request, response) => {
    if (request.method === 'POST') {
      const parts = [];
      request.on('data', (part) => parts.push(part));
      request.on('end', () => {
        response.writeHead(204).end();
        heard(Buffer.concat(parts).toString());
      });
      return;
    }
    const name = decodeURIComponent(new URL(request.url, ORIGIN).pathname);
    const file = path.resolve(site, `.${name}`);
    const inside = file === site || file.startsWith(site + path.sep);
    const target = inside && fs.existsSync(file) && fs.statSync(file).isFile() ? file : path.join(site, 'index.html');
    response.setHeader('Content-Type', TYPES[path.extname(target)] || 'application/octet-stream');
    response.setHeader('Cache-Control', 'no-cache');
    fs.createReadStream(target).pipe(response);
  });
  return new Promise((resolve) => server.listen(PORT.site, '127.0.0.1', () => resolve(server)));
}

// Chrome, spoken to over its debugging protocol.
class Browser {
  static async open() {
    const profile = path.join(HOME, 'profile');
    start('chrome', CHROME, [
      `--user-data-dir=${profile}`, `--remote-debugging-port=${PORT.chrome}`, '--no-first-run',
      '--no-default-browser-check', '--ignore-certificate-errors', '--window-size=1280,900',
      ...(options.headed ? [] : ['--headless=new']), 'about:blank',
    ]);
    const version = await (await reachable(`http://127.0.0.1:${PORT.chrome}/json/version`, 30, 'Chrome')).json();
    const browser = new Browser(new WebSocket(version.webSocketDebuggerUrl));
    await new Promise((resolve, reject) => {
      browser.socket.onopen = resolve;
      browser.socket.onerror = () => reject(new Error('Chrome refused the debugger'));
    });
    return browser;
  }

  constructor(socket) {
    this.socket = socket;
    this.sent = 0;
    this.pending = new Map();
    this.console = fs.createWriteStream(path.join(HOME, 'browser.log'), { flags: 'a' });
    socket.onmessage = (message) => {
      const data = JSON.parse(message.data);
      if (data.id !== undefined && this.pending.has(data.id)) {
        const { resolve, reject } = this.pending.get(data.id);
        this.pending.delete(data.id);
        if (data.error) reject(new Error(`${data.error.message}`));
        else resolve(data.result);
      } else if (data.method === 'ServiceWorker.workerRegistrationUpdated') {
        // Chrome has service workers of its own, for its extensions.
        const own = data.params.registrations.find((registration) => registration.scopeURL.startsWith(ORIGIN));
        if (own) this.registration = own.registrationId;
      } else if (data.method === 'Runtime.consoleAPICalled') {
        this.console.write(`${data.params.args.map((value) => value.value ?? value.description ?? '').join(' ')}\n`);
      } else if (data.method === 'Runtime.exceptionThrown') {
        this.console.write(`exception ${JSON.stringify(data.params.exceptionDetails).slice(0, 2000)}\n`);
      }
    };
  }

  send(method, parameters = {}, sessionId = undefined) {
    return new Promise((resolve, reject) => {
      this.sent += 1;
      this.pending.set(this.sent, { resolve, reject });
      this.socket.send(JSON.stringify({ id: this.sent, method, params: parameters, sessionId }));
    });
  }

  async attach(targetId) {
    const { sessionId } = await this.send('Target.attachToTarget', { targetId, flatten: true });
    await this.send('Runtime.enable', {}, sessionId);
    return sessionId;
  }

  async openPage(url) {
    const { targetId } = await this.send('Target.createTarget', { url });
    this.page = await this.attach(targetId);
  }

  async evaluate(sessionId, expression) {
    const result = await this.send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, sessionId);
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails).slice(0, 500));
    return result.result.value;
  }

  allowNotifications() {
    return this.send('Browser.grantPermissions', { origin: ORIGIN, permissions: ['notifications'] });
  }

  // A notification of the system cannot be tapped from here. The service
  // worker is given the event that a tap causes instead.
  async tapNotification() {
    for (let attempt = 0; attempt < 20; attempt += 1) {
      // A message wakes a service worker that the browser has put to sleep.
      await this.evaluate(this.page,
        'navigator.serviceWorker.ready.then((registration) => registration.active.postMessage({ type: "wake" }))');
      const { targetInfos } = await this.send('Target.getTargets');
      const worker = targetInfos.find((target) => target.type === 'service_worker' && target.url.startsWith(ORIGIN));
      if (worker) {
        const sessionId = await this.attach(worker.targetId);
        return this.evaluate(sessionId, `(async () => {
          const [notification] = await self.registration.getNotifications();
          if (!notification) return 'no notification';
          self.dispatchEvent(new NotificationEvent('notificationclick', { notification }));
          return 'tapped';
        })()`);
      }
      await sleep(500);
    }
    return 'no service worker';
  }

  // Hands the service worker a push that no key of its own can read, as a
  // relay or a push service might send one.
  async pushUnreadable() {
    await this.send('ServiceWorker.enable', {}, this.page);
    for (let attempt = 0; attempt < 20 && this.registration === undefined; attempt += 1) await sleep(250);
    if (this.registration === undefined) return [];
    await this.send('ServiceWorker.deliverPushMessage', {
      origin: ORIGIN,
      registrationId: this.registration,
      data: JSON.stringify({
        version: 1, eventId: 'event-of-nobody-0001', keyId: 'key-of-nobody-000001',
        nonce: 'AAAAAAAAAAAAAAAA', ciphertext: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
      }),
    }, this.page);
    await sleep(1500);
    return this.evaluate(this.page, `navigator.serviceWorker.ready
      .then((registration) => registration.getNotifications())
      .then((shown) => shown.map((notification) => ({ title: notification.title, body: notification.body })))`);
  }

  async picture(name) {
    const { data } = await this.send('Page.captureScreenshot', { format: 'png' }, this.page);
    fs.writeFileSync(path.join(HOME, name), Buffer.from(data, 'base64'));
  }
}

function tail(name, lines = 15) {
  const file = path.join(HOME, `${name}.log`);
  if (!fs.existsSync(file)) return '';
  return fs.readFileSync(file, 'utf8').trimEnd().split('\n').slice(-lines).join('\n');
}

async function main() {
  fs.rmSync(HOME, { recursive: true, force: true });
  fs.mkdirSync(HOME, { recursive: true });
  const checks = [];
  const check = (what, passed, detail = '') => {
    checks.push({ what, passed });
    log(passed ? 'ok  ' : 'FAIL', what, detail);
  };

  log('relay');
  const state = await startRelay();
  log('agent');
  const agent = prepareAgent();
  const certificate = makeCertificate();
  await startTls(certificate, PORT.agentTls, PORT.agent);
  await startTls(certificate, PORT.relayTls, PORT.relay);
  log('web client');
  const site = buildSite();

  let browser;
  let finish;
  const finished = new Promise((resolve) => { finish = resolve; });
  const heard = [];
  // One message is handled after the other, in the order of their arrival.
  let queue = Promise.resolve();
  await serve(site, (message) => {
    queue = queue.then(() => hear(message)).catch((error) => finish(`failed ${error.message}`));
  });
  async function hear(message) {
    const [kind, ...rest] = message.split(' ');
    const detail = rest.join(' ');
    heard.push(kind);
    log('  ', kind, kind === 'identity' ? '' : detail.slice(0, 300));
    if (kind === 'identity') {
      startAgent(agent, JSON.parse(detail));
    } else if (kind === 'permission') {
      await browser.allowNotifications();
    } else if (kind === 'notification') {
      const [shown] = JSON.parse(detail);
      check('the notification is decrypted', shown.title === 'Forge session completed', shown.title);
      check('it has a summary', typeof shown.body === 'string' && shown.body.length > 0 &&
        shown.body !== 'Encrypted notification', shown.body);
      check('it names the session to open', Boolean(shown.data && shown.data.agentId && shown.data.sessionId));
    } else if (kind === 'tap') {
      log('  ', await browser.tapNotification());
    } else if (kind === 'done') {
      const shown = await browser.pushUnreadable();
      check('a push that cannot be read is shown without content',
        shown.some((notification) => notification.title === 'Forge' && notification.body === 'Encrypted notification'),
        JSON.stringify(shown));
      finish(message);
    } else if (kind === 'failed') {
      finish(message);
    }
  }

  log('browser');
  browser = await Browser.open();
  await browser.openPage(ORIGIN);
  const result = await Promise.race([finished, sleep(15 * 60 * 1000).then(() => 'failed the run took too long')]);

  for (const step of ['identity', 'platform', 'connected', 'permission', 'pairing', 'subscribed', 'prompted', 'notification', 'tap', 'tapped']) {
    check(`step: ${step}`, heard.includes(step));
  }
  const devices = rows(state, "SELECT platform FROM devices");
  check('the relay knows one device, a browser', devices.length === 1 && devices[0].platform === 'web');
  const endpoints = rows(state, "SELECT provider, disabled_at FROM push_endpoints");
  check('it holds one subscription', endpoints.length === 1 && endpoints[0].provider === 'webpush' && endpoints[0].disabled_at === null);
  const events = rows(state, "SELECT event_type, session_id, delivery_status, provider_status FROM push_events");
  check('the push service accepted the event', events.length > 0 && events.every((event) => event.delivery_status === 'delivered'),
    JSON.stringify(events));
  check('the relay recorded nothing of its content', events.every((event) => event.event_type === 'encrypted' && event.session_id === 'encrypted'));
  check('the agent reports no failure', !/push notification:/.test(tail('agent', 200)));

  const passed = result === 'done' && checks.every((entry) => entry.passed);
  if (!passed) {
    log(`\n${result}`);
    try { await browser.picture('failure.png'); } catch {}
    for (const name of ['agent', 'relay', 'browser']) log(`\n--- ${name}.log\n${tail(name)}`);
  }
  log(passed ? '\nNotifications in a browser work.' : `\nThe test failed. Its files are in ${HOME}`);
  return passed;
}

let status = 1;
main()
  .then((passed) => { status = passed ? 0 : 1; })
  .catch((error) => { console.error(error.message); })
  .finally(async () => {
    stopAll();
    // Chrome writes to its profile until it has ended.
    await sleep(1500);
    if (!options.keep && status === 0) {
      try {
        fs.rmSync(HOME, { recursive: true, force: true, maxRetries: 10, retryDelay: 300 });
      } catch (error) {
        console.error(`${HOME} was not removed: ${error.message}`);
      }
    }
    process.exit(status);
  });
