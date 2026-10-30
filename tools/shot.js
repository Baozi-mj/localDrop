#!/usr/bin/env node
/* tools/shot.js — headless Chrome screenshots via the DevTools protocol.
 *
 * Unlike `chrome --screenshot --virtual-time-budget`, this waits REAL time after
 * navigation, so pages holding a never-ending SSE connection don't stall.
 *
 * Usage:
 *   node tools/shot.js <chrome> <width> <height> <waitMs> <url> <out.png> [<url> <out.png> ...]
 * Env:
 *   SHOT_UA=<user agent>   override the UA (e.g. an iPhone UA)
 *   SHOT_FULL=1            capture the full page height instead of the viewport
 */
'use strict';
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');

const [, , chrome, w, h, waitMs, ...pairs] = process.argv;
if (!chrome || !w || !h || !waitMs || pairs.length < 2 || pairs.length % 2) {
  console.error('usage: shot.js <chrome> <width> <height> <waitMs> <url> <out.png> [...]');
  process.exit(2);
}

const port = 9300 + Math.floor(Math.random() * 500);
const udd = fs.mkdtempSync('/tmp/ld-chrome-');
const proc = spawn(chrome, [
  '--headless=new', '--disable-gpu', '--hide-scrollbars', '--no-first-run',
  '--no-default-browser-check', '--force-device-scale-factor=2',
  `--window-size=${w},${h}`, `--remote-debugging-port=${port}`,
  `--user-data-dir=${udd}`, 'about:blank',
], { stdio: 'ignore' });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function httpJSON(path, method) {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, path, method: method || 'GET' }, (res) => {
      let body = '';
      res.on('data', (d) => { body += d; });
      res.on('end', () => {
        try { resolve(body ? JSON.parse(body) : null); } catch (e) { resolve(null); }
      });
    });
    req.on('error', reject);
    req.end();
  });
}

async function waitForChrome() {
  for (let i = 0; i < 150; i++) {
    try { if (await httpJSON('/json/version')) return; } catch (e) { /* not yet */ }
    await sleep(100);
  }
  throw new Error('chrome did not start');
}

function makeSender(ws) {
  let nextId = 0;
  const pending = new Map();
  ws.addEventListener('message', (ev) => {
    let m;
    try { m = JSON.parse(ev.data); } catch (e) { return; }
    if (m.id && pending.has(m.id)) {
      const p = pending.get(m.id);
      pending.delete(m.id);
      if (m.error) p.reject(new Error(JSON.stringify(m.error)));
      else p.resolve(m.result);
    }
  });
  return (method, params) => new Promise((resolve, reject) => {
    const id = ++nextId;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params: params || {} }));
  });
}

function cleanup() {
  try { proc.kill('SIGKILL'); } catch (e) { /* ignore */ }
  try { fs.rmSync(udd, { recursive: true, force: true }); } catch (e) { /* ignore */ }
}

(async () => {
  const guard = setTimeout(() => { console.error('shot.js: global timeout'); cleanup(); process.exit(3); }, 120000);
  await waitForChrome();
  for (let i = 0; i < pairs.length; i += 2) {
    const url = pairs[i];
    const out = pairs[i + 1];
    const target = await httpJSON('/json/new?about:blank', 'PUT');
    const ws = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve);
      ws.addEventListener('error', reject);
    });
    const send = makeSender(ws);
    await send('Page.enable');
    if (process.env.SHOT_UA) {
      await send('Network.setUserAgentOverride', { userAgent: process.env.SHOT_UA });
    }
    await send('Emulation.setDeviceMetricsOverride', {
      width: Number(w), height: Number(h), deviceScaleFactor: 2, mobile: Number(w) < 600,
    });
    await send('Page.navigate', { url });
    await sleep(Number(waitMs));
    const params = { format: 'png' };
    if (process.env.SHOT_FULL) {
      const metrics = await send('Page.getLayoutMetrics');
      const size = metrics.cssContentSize || metrics.contentSize;
      params.clip = { x: 0, y: 0, width: Number(w), height: Math.ceil(size.height), scale: 1 };
      params.captureBeyondViewport = true;
    }
    const shot = await send('Page.captureScreenshot', params);
    fs.writeFileSync(out, Buffer.from(shot.data, 'base64'));
    console.log('wrote ' + out);
    ws.close();
    await httpJSON('/json/close/' + target.id);
  }
  clearTimeout(guard);
  cleanup();
  process.exit(0);
})().catch((e) => {
  console.error('shot.js: ' + (e && e.message ? e.message : e));
  cleanup();
  process.exit(1);
});
