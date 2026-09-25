// Only orchestration lives here. Mermaid owns parsing, layout, and SVG output;
// Puppeteer owns rasterization. stdout is reserved for JSON-lines responses.
import { createRequire } from 'node:module';
import { createServer } from 'node:http';
import { readFile, realpath } from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import readline from 'node:readline';

const runtimeDir = process.argv[2];
let browser, page, server, initialization;
let closing = false;
let sequence = 0;
let backend;

class BackendError extends Error {
  constructor(kind, message) { super(message); this.kind = kind; }
}

async function cleanup() {
  if (closing) return;
  closing = true;
  if (browser) {
    // Chromium is normally in its own process group. If its control connection
    // stalls, kill that owned group before the Go host gives up on this worker.
    const child = browser.process();
    const timer = setTimeout(() => {
      if (!child?.pid) return;
      try { process.kill(-child.pid, 'SIGKILL'); }
      catch { try { child.kill('SIGKILL'); } catch {} }
    }, 500).unref();
    try { await browser.close(); } catch {}
    finally { clearTimeout(timer); }
  }
  if (server) await new Promise(resolve => server.close(resolve));
}

for (const signal of ['SIGTERM', 'SIGINT']) {
  process.on(signal, () => {
    const timer = setTimeout(() => process.exit(1), 1500).unref();
    cleanup().finally(() => { clearTimeout(timer); process.exit(0); });
  });
}
process.stdout.on('error', () => { cleanup().finally(() => process.exit(0)); });

async function engine() {
  if (!initialization) initialization = initialize();
  return initialization;
}

async function initialize() {
  if (!runtimeDir) throw new BackendError('unavailable', 'No official runtime directory configured');
  const require = createRequire(path.join(path.resolve(runtimeDir), 'package.json'));
  let mermaidRoot, puppeteer, mermaidPackage, puppeteerPackage;
  try {
    const mermaidManifest = require.resolve('mermaid/package.json');
    mermaidRoot = path.dirname(mermaidManifest);
    mermaidPackage = JSON.parse(await readFile(mermaidManifest, 'utf8'));
    puppeteerPackage = JSON.parse(await readFile(require.resolve('puppeteer/package.json'), 'utf8'));
    const imported = await import(pathToFileURL(require.resolve('puppeteer')).href);
    puppeteer = imported.launch ? imported : imported.default;
  } catch (error) {
    throw new BackendError('unavailable', `Official renderer dependencies are unavailable in ${runtimeDir}: ${error.message}`);
  }
  const assetRoot = await realpath(path.join(mermaidRoot, 'dist'));
  server = createServer(async (request, response) => {
    try {
      const pathname = new URL(request.url, 'http://localhost').pathname;
      if (pathname === '/') {
        response.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
        response.end('<!doctype html><html><head><meta charset="utf-8"></head><body style="margin:0"><div id="output"></div></body></html>');
        return;
      }
      if (!pathname.startsWith('/mermaid/')) throw new Error('Not found');
      const file = await realpath(path.resolve(assetRoot, decodeURIComponent(pathname.slice('/mermaid/'.length))));
      if (!file.startsWith(assetRoot + path.sep)) throw new Error('Not found');
      const mime = file.endsWith('.css') ? 'text/css' : file.endsWith('.woff2') ? 'font/woff2' : 'text/javascript';
      response.writeHead(200, { 'content-type': mime });
      response.end(await readFile(file));
    } catch {
      response.writeHead(404); response.end('Not found');
    }
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const origin = `http://127.0.0.1:${server.address().port}`;
  try {
    browser = await puppeteer.launch({ headless: true });
    page = await browser.newPage();
    await page.setViewport({ width: 1600, height: 1200, deviceScaleFactor: 1 });
    await page.setRequestInterception(true);
    page.on('request', request => {
      const url = request.url();
      if (url.startsWith(origin + '/') || url.startsWith('data:') || url.startsWith('blob:')) request.continue();
      else request.abort();
    });
    await page.goto(origin, { waitUntil: 'load' });
    await page.evaluate(async () => {
      window.mermaid = (await import('/mermaid/mermaid.esm.min.mjs')).default;
    });
    backend = {
      backend: 'mermaid', version: '1', mermaidVersion: mermaidPackage.version,
      puppeteerVersion: puppeteerPackage.version, browserVersion: await browser.version(),
    };
  } catch (error) {
    throw new BackendError('unavailable', `Unable to start the official Mermaid browser: ${error.message}`);
  }
  return backend;
}

async function handle(request) {
  if (request.operation === 'shutdown') return { ok: true };
  await engine();
  if (request.operation === 'info') return { ok: true, backend };
  if (!['validate', 'render'].includes(request.operation)) throw new BackendError('runtime', 'Unknown worker operation');
  if (typeof request.source !== 'string') throw new BackendError('runtime', 'Source must be a string');
  if (request.operation === 'render' && !['svg', 'png'].includes(request.format)) throw new BackendError('runtime', 'Unsupported official output format');
  const id = `lazymermaid-${++sequence}`;
  const result = await page.evaluate(async ({ source, operation, config, theme, id }) => {
    const container = document.getElementById('output');
    container.replaceChildren();
    const options = { ...config, startOnLoad: false };
    if (theme) options.theme = theme;
    window.mermaid.initialize(options);
    let parsed;
    let stage = 'parse';
    try {
      parsed = await window.mermaid.parse(source);
      if (operation === 'validate') return { ok: true, diagramType: parsed.diagramType };
      stage = 'render';
      const rendered = await window.mermaid.render(id, source);
      container.innerHTML = rendered.svg;
      await document.fonts.ready;
      const svg = container.querySelector('svg');
      if (!svg) throw new Error('Mermaid returned no SVG element');
      return { ok: true, diagramType: rendered.diagramType, svg: new XMLSerializer().serializeToString(svg) };
    } catch (error) {
      const message = error?.message ?? error?.str ?? String(error);
      const lexer = error?.result?.lexerErrors?.[0];
      const token = error?.result?.parserErrors?.[0]?.token;
      const loc = error?.hash?.loc;
      const rawLine = lexer?.line ?? token?.startLine ?? loc?.first_line;
      const rawColumn = lexer?.column ?? token?.startColumn ?? (Number.isFinite(loc?.first_column) ? loc.first_column + 1 : undefined);
      const diagnostic = { message, reliable: false };
      // These coordinates are upstream parser coordinates, NOT promised source
      // coordinates. Mermaid may remove comments/frontmatter before parsing.
      if (Number.isFinite(rawLine) && rawLine > 0) diagnostic.line = rawLine;
      if (Number.isFinite(rawColumn) && rawColumn > 0) diagnostic.column = rawColumn;
      container.replaceChildren();
      return { ok: false, kind: stage === 'parse' ? 'invalid' : 'runtime', message, diagnostics: [diagnostic], diagramType: parsed?.diagramType };
    }
  }, { source: request.source, operation: request.operation, config: request.config ?? {}, theme: request.theme, id });
  result.backend = backend;
  if (!result.ok || request.operation === 'validate') return result;
  let data;
  if (request.format === 'svg') data = Buffer.from(result.svg, 'utf8');
  else {
    const element = await page.$('#output svg');
    try { data = Buffer.from(await element.screenshot({ type: 'png', omitBackground: true })); }
    finally { await element.dispose(); }
  }
  delete result.svg;
  return { ...result, format: request.format, data: data.toString('base64') };
}

const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
let queue = Promise.resolve();
input.on('line', line => {
  queue = queue.then(async () => {
    let request;
    try {
      request = JSON.parse(line);
      const response = await handle(request);
      process.stdout.write(JSON.stringify({ id: request.id, ...response }) + '\n');
      if (request.operation === 'shutdown') { await cleanup(); process.exit(0); }
    } catch (error) {
      process.stdout.write(JSON.stringify({ id: request?.id, ok: false, kind: error.kind ?? 'runtime', message: error.message ?? String(error), backend }) + '\n');
    }
  });
});
input.on('close', () => { queue.finally(() => cleanup()).finally(() => process.exit(0)); });
