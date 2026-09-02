#!/usr/bin/env node

const fs = require('fs');
const http = require('http');
const os = require('os');
const path = require('path');
const { spawn } = require('child_process');

const repoRoot = path.resolve(__dirname, '..');
const chromePath = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const publicRoutes = ['about', 'manifesto', 'otto', 'circle', 'olivewolf', 'ai-club'];

function contentType(file) {
  if (file.endsWith('.html')) return 'text/html; charset=utf-8';
  if (file.endsWith('.js')) return 'text/javascript; charset=utf-8';
  if (file.endsWith('.css')) return 'text/css; charset=utf-8';
  if (file.endsWith('.svg')) return 'image/svg+xml';
  if (file.endsWith('.png')) return 'image/png';
  return 'application/octet-stream';
}

function startServer() {
  return new Promise((resolve) => {
    const server = http.createServer((req, res) => {
      const urlPath = decodeURIComponent(new URL(req.url, 'http://localhost').pathname);
      if (!urlPath.startsWith('/miraphant/')) {
        res.writeHead(404).end();
        return;
      }
      let relative = urlPath.slice('/miraphant/'.length);
      if (!relative || relative.endsWith('/')) relative += 'index.html';
      const file = path.resolve(repoRoot, relative);
      if (!file.startsWith(repoRoot + path.sep) || !fs.existsSync(file)) {
        res.writeHead(404).end();
        return;
      }
      res.writeHead(200, { 'content-type': contentType(file) });
      fs.createReadStream(file).pipe(res);
    });
    server.listen(0, '127.0.0.1', () => resolve(server));
  });
}

function waitForFile(file, timeoutMs = 10000) {
  const started = Date.now();
  return new Promise((resolve, reject) => {
    const poll = () => {
      if (fs.existsSync(file)) return resolve();
      if (Date.now() - started > timeoutMs) return reject(new Error(`Timed out waiting for ${file}`));
      setTimeout(poll, 50);
    };
    poll();
  });
}

function connectCdp(webSocketUrl) {
  const socket = new WebSocket(webSocketUrl);
  const pending = new Map();
  const eventWaiters = new Map();
  let nextId = 1;

  socket.onmessage = ({ data }) => {
    const message = JSON.parse(data);
    if (message.id && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id);
      pending.delete(message.id);
      message.error ? reject(new Error(message.error.message)) : resolve(message.result);
      return;
    }
    const waiters = eventWaiters.get(message.method);
    if (waiters?.length) waiters.shift()(message.params);
  };

  return new Promise((resolve, reject) => {
    socket.onerror = () => reject(new Error('Chrome DevTools connection failed'));
    socket.onopen = () => resolve({
      close: () => socket.close(),
      send(method, params = {}) {
        const id = nextId++;
        socket.send(JSON.stringify({ id, method, params }));
        return new Promise((resolveCommand, rejectCommand) => {
          pending.set(id, { resolve: resolveCommand, reject: rejectCommand });
        });
      },
      waitFor(method) {
        return new Promise((resolveEvent) => {
          const waiters = eventWaiters.get(method) || [];
          waiters.push(resolveEvent);
          eventWaiters.set(method, waiters);
        });
      },
    });
  });
}

async function run() {
  if (!fs.existsSync(chromePath)) throw new Error(`Chrome not found at ${chromePath}`);
  const server = await startServer();
  const { port } = server.address();
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'miraphant-language-test-'));
  const chrome = spawn(chromePath, [
    '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
    '--disable-background-networking', '--remote-debugging-port=0', `--user-data-dir=${profile}`,
    'about:blank',
  ], { stdio: 'ignore' });

  let cdp;
  try {
    const activePortFile = path.join(profile, 'DevToolsActivePort');
    await waitForFile(activePortFile);
    const [debugPort] = fs.readFileSync(activePortFile, 'utf8').trim().split('\n');
    const targets = await (await fetch(`http://127.0.0.1:${debugPort}/json/list`)).json();
    cdp = await connectCdp(targets.find((target) => target.type === 'page').webSocketDebuggerUrl);
    await cdp.send('Page.enable');
    await cdp.send('Runtime.enable');

    async function navigate(route) {
      const loaded = cdp.waitFor('Page.loadEventFired');
      await cdp.send('Page.navigate', { url: `http://127.0.0.1:${port}/miraphant/${route}` });
      await loaded;
    }

    async function evaluate(expression) {
      const result = await cdp.send('Runtime.evaluate', { expression, returnByValue: true });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.text);
      return result.result.value;
    }

    await navigate('');
    const homeControls = await evaluate(`({nav:document.querySelectorAll('nav .language-menu, nav .lang-btn').length,footer:document.querySelectorAll('footer .language-menu').length,options:document.querySelectorAll('footer .language-popover .lang-btn').length})`);
    if (homeControls.nav !== 0 || homeControls.footer !== 1 || homeControls.options !== 2) {
      throw new Error(`language placement: expected 0 nav controls and one 2-option footer menu, received nav=${homeControls.nav}/footer=${homeControls.footer}/options=${homeControls.options}`);
    }
    await evaluate(`document.querySelector('[data-lang="zh"]').click()`);

    for (const route of publicRoutes) {
      await navigate(`${route}/`);
      const state = await evaluate(`({lang:document.documentElement.lang,stored:localStorage.getItem('miraphant-lang'),pending:document.documentElement.classList.contains('miraphant-language-pending'),nav:document.querySelector('[data-nav="about"]')?.textContent.trim()})`);
      if (!state.lang.startsWith('zh') || state.stored !== 'zh' || state.pending || state.nav !== '关于我们') {
        throw new Error(`${route}: expected visible zh/zh/关于我们, received ${state.lang}/${state.stored}/${state.nav}/pending=${state.pending}`);
      }
    }

    const reloaded = cdp.waitFor('Page.loadEventFired');
    await cdp.send('Page.reload');
    await reloaded;
    const refreshed = await evaluate(`({lang:document.documentElement.lang,pending:document.documentElement.classList.contains('miraphant-language-pending')})`);
    if (!refreshed.lang.startsWith('zh') || refreshed.pending) {
      throw new Error(`refresh: expected visible zh, received ${refreshed.lang}/pending=${refreshed.pending}`);
    }

    await navigate('about/');
    await evaluate(`history.back()`);
    for (let attempt = 0; attempt < 20; attempt += 1) {
      if ((await evaluate(`location.pathname`)) !== '/miraphant/about/') break;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    const historyLanguage = await evaluate(`document.documentElement.lang`);
    if (!historyLanguage.startsWith('zh')) {
      throw new Error(`browser history: expected zh, received ${historyLanguage}`);
    }

    await evaluate(`document.querySelector('[data-lang="en"]').click()`);
    await navigate('about/');
    const english = await evaluate(`({lang:document.documentElement.lang,stored:localStorage.getItem('miraphant-lang'),nav:document.querySelector('[data-nav="about"]')?.textContent.trim()})`);
    if (english.lang !== 'en' || english.stored !== 'en' || english.nav !== 'About') {
      throw new Error(`English propagation: expected en/en/About, received ${english.lang}/${english.stored}/${english.nav}`);
    }

    await evaluate(`localStorage.removeItem('miraphant-lang');localStorage.setItem('about-lang','zh')`);
    await navigate('about/');
    const migrated = await evaluate(`({lang:document.documentElement.lang,stored:localStorage.getItem('miraphant-lang'),legacy:localStorage.getItem('about-lang')})`);
    if (!migrated.lang.startsWith('zh') || migrated.stored !== 'zh' || migrated.legacy !== null) {
      throw new Error(`legacy migration: expected zh/zh/null, received ${migrated.lang}/${migrated.stored}/${migrated.legacy}`);
    }

    console.log(`PASS: footer language menu and persistence across ${publicRoutes.length} routes, refresh, history, reverse switch, and legacy migration`);
  } finally {
    cdp?.close();
    server.close();
    if (chrome.exitCode === null) {
      const exited = new Promise((resolve) => chrome.once('exit', resolve));
      chrome.kill('SIGTERM');
      await Promise.race([exited, new Promise((resolve) => setTimeout(resolve, 1000))]);
    }
    try {
      fs.rmSync(profile, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 });
    } catch {}
  }
}

run().catch((error) => {
  console.error(`FAIL: ${error.message}`);
  process.exitCode = 1;
});
