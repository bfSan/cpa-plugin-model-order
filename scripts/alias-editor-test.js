// Interaction test for the alias editor in the model-registry panel.
//
// The Go tests render the HTML but never run it, so the editor's behaviour is
// untested by the normal suite. This drives the real page against a stub of CPA's
// management API, so nothing here can touch a live alias table.
//
// What it checks, in the order a user would hit it:
//   1. the panel loads the table and renders one row per entry
//   2. editing a value marks only that channel dirty
//   3. saving sends the channel's FULL list, because CPA PATCH replaces it
//   4. channels that were never touched are never sent
//   5. revert restores the baseline
//   6. adding a duplicate name is refused
const http = require('http');
const fs = require('fs');
const path = require('path');

// playwright is not a dependency of this repo. Resolve it from PW_DIR,
// NODE_PATH, a local node_modules, or the cwd, and skip cleanly rather than
// failing with a confusing MODULE_NOT_FOUND.
function loadPlaywright() {
  const roots = [process.env.PW_DIR, process.env.NODE_PATH, path.join(__dirname, '..', 'node_modules'), process.cwd()].filter(Boolean);
  for (const root of roots) {
    try { return require(require.resolve('playwright', { paths: [root] })); } catch (e) { /* try the next root */ }
  }
  try { return require('playwright'); } catch (e) {
    console.error('SKIP: playwright not found. Set PW_DIR to a node_modules path that has it.');
    process.exit(77);
  }
}
const { chromium } = loadPlaywright();

const REPO = path.resolve(__dirname, '..');
const PANEL = fs.readFileSync(path.join(REPO, 'panel.html'), 'utf8');
const MGMT = '/v0/management';
const API = '/v0/management/plugins/model-registry';
const MGMT_KEY = 'test-key';

// The table as CPA currently holds it in this deployment.
const INITIAL = {
  'qoder': [{ name: 'auto', alias: 'qoder-auto' }, { name: 'fast', alias: 'qoder-fast' }],
  'workbuddy': [{ name: 'space-bunny', alias: 'workbuddy-space-bunny' }],
};

let table = null;      // what the stub "serves"
let patchLog = [];     // every PATCH body the page sent

function reset() { table = JSON.parse(JSON.stringify(INITIAL)); patchLog = []; }

function bodyJson(req) {
  return new Promise(resolve => {
    let b = '';
    req.on('data', c => (b += c));
    req.on('end', () => { try { resolve(b ? JSON.parse(b) : null); } catch (e) { resolve(null); } });
  });
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://x');
  if (url.pathname === API + '/alias-report' && req.method === 'POST') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    return res.end(JSON.stringify({ reports: [] }));
  }
  if (url.pathname.startsWith(API + '/')) {
    if (url.pathname === API + '/status') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ configured: true, strategy: 'grouped', order: [], catalogs: [], policies: [], policy_count: 0 }));
    }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    return res.end(JSON.stringify({ configured: true, strategy: 'grouped', order: [], catalogs: [], policies: [], policy_count: 0 }));
  }
  if (url.pathname === MGMT + '/oauth-model-alias') {
    if (req.method === 'GET') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ 'oauth-model-alias': table }));
    }
    if (req.method === 'PATCH') {
      const b = await bodyJson(req);
      patchLog.push(b);
      // Mirror CPA: PATCH replaces the whole channel.
      if (!b || typeof b.channel !== 'string') {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        return res.end(JSON.stringify({ error: 'invalid channel' }));
      }
      table[b.channel] = b.aliases || [];
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ status: 'ok' }));
    }
  }
  if (url.pathname === '/panel') {
    // The host stamps MANAGEMENT_BASE_PATH; reproduce that exactly.
    const html = PANEL.replace('__MO_MANAGEMENT_BASE_PATH_JSON__', `"${MGMT}"`)
      .replace(/__MO_RESOURCE_BASE_PATH_JSON__|__MO_/g, '"' + API + '"');
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    return res.end(html);
  }
  res.writeHead(404); res.end('{}');
});

const fails = [];
function check(name, ok, extra) {
  console.log((ok ? '  ok   ' : '  FAIL ') + name + (extra ? '  ' + extra : ''));
  if (!ok) fails.push(name);
}

(async () => {
  await new Promise(r => server.listen(0, '127.0.0.1', r));
  const port = server.address().port;
  reset();
  const browser = await chromium.launch();
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', e => errors.push(String(e)));
  await page.goto(`http://127.0.0.1:${port}/panel`);
  // The panel gates on a management key held in sessionStorage, and it also
  // accepts one via ?key= which it then moves into sessionStorage itself.
  await page.evaluate(k => sessionStorage.setItem('model-registry-mgmt-key', k), MGMT_KEY);
  await page.reload();
  await page.waitForSelector('#aliasRows tr td input', { timeout: 8000 });

  // 1. load + render
  const chans = await page.$$eval('.chan-tab', els => els.map(e => e.childNodes[0].textContent));
  check('channels render from CPA', chans.includes('qoder') && chans.includes('workbuddy'), JSON.stringify(chans));
  // Default channel is the alphabetically first one.
  // Two entries, each contributing a name input and an alias input.
  const firstRows = await page.$$eval('#aliasRows input', els => els.map(e => e.value));
  check('default channel rows render', firstRows.length === 4, JSON.stringify(firstRows));
  check('rows show name then alias', firstRows[0] === 'auto' && firstRows[1] === 'qoder-auto', JSON.stringify(firstRows));

  // 2. editing a value marks only that channel dirty
  await page.click('.chan-tab:nth-child(2)'); // workbuddy, one entry
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length === 2);
  const saveBtn = await page.$('#btnAliasSave');
  check('save disabled before any edit', await saveBtn.isDisabled());
  const inputs = await page.$$('#aliasRows input');
  await inputs[1].fill('workbuddy-space-bunny-v2');
  await page.waitForFunction(() => !document.getElementById('btnAliasSave').disabled);
  check('save enabled after edit', true);
  // The "+ channel" tab has no count span, so filter on it being present.
  const dirtyTabs = (await page.$$eval('.chan-tab', els => els.map(e => {
    const c = e.querySelector('.chan-count');
    return c && c.textContent.includes('*') ? e.childNodes[0].textContent : null;
  }))).filter(Boolean);
  check('only the edited channel is dirty', dirtyTabs.length === 1 && dirtyTabs[0] === 'workbuddy', JSON.stringify(dirtyTabs));

  // 3. saving sends the channel's FULL list and nothing else
  page.once('dialog', d => d.accept());
  await page.click('#btnAliasSave');
  await page.waitForFunction(() => document.querySelectorAll('.chan-tab').length > 0 && document.getElementById('btnAliasSave').disabled, null, { timeout: 8000 });
  check('exactly one PATCH was sent', patchLog.length === 1, JSON.stringify(patchLog));
  check('PATCH carries channel name', patchLog[0] && patchLog[0].channel === 'workbuddy', JSON.stringify(patchLog[0] && patchLog[0].channel));
  check('PATCH carries the full channel list, not a merge',
    patchLog[0] && Array.isArray(patchLog[0].aliases) && patchLog[0].aliases.length === 1 && patchLog[0].aliases[0].alias === 'workbuddy-space-bunny-v2',
    JSON.stringify(patchLog[0] && patchLog[0].aliases));
  check('untouched channel was not sent', patchLog.every(p => p.channel !== 'qoder'));
  check('the stub now serves the edited value', table['workbuddy'][0].alias === 'workbuddy-space-bunny-v2', JSON.stringify(table['workbuddy']));

  // 4. revert restores the baseline after a fresh load
  await page.click('.chan-tab:nth-child(1)');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length === 4);
  const qinputs = await page.$$('#aliasRows input');
  await qinputs[0].fill('changed-upstream');
  await page.waitForFunction(() => !document.getElementById('btnAliasSave').disabled);
  page.once('dialog', d => d.accept());
  await page.click('#btnAliasRevert');
  await page.waitForFunction(() => document.getElementById('btnAliasSave').disabled, null, { timeout: 5000 });
  const reverted = await page.$$eval('#aliasRows input', els => els.map(e => e.value));
  check('revert restores the loaded baseline', reverted[0] === 'auto', JSON.stringify(reverted));

  // 5. adding a row, and refusing a duplicate name
  await page.fill('#aliasNewName', 'hy4-preview-x');
  await page.fill('#aliasNewValue', 'qoder-hy4-preview-x');
  await page.click('#btnAliasAdd');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows tr').length === 3, null, { timeout: 5000 });
  const afterAdd = await page.$$eval('#aliasRows input', els => els.map(e => e.value));
  check('add appends a row', afterAdd.includes('hy4-preview-x'), JSON.stringify(afterAdd));
  await page.fill('#aliasNewName', 'auto');
  await page.fill('#aliasNewValue', 'qoder-dup');
  await page.click('#btnAliasAdd');
  await page.waitForTimeout(300);
  const afterDup = await page.$$eval('#aliasRows input', els => els.map(e => e.value));
  check('duplicate name is refused', afterDup.filter(v => v === 'auto').length === 1, JSON.stringify(afterDup));

  // 6. deleting the last row of a channel is representable
  await page.click('#aliasChannels .chan-tab:nth-child(2)'); // workbuddy
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length === 2);
  await page.click('#aliasRows tr:first-child button.mini');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length === 0, null, { timeout: 5000 });
  check('deleting the only row empties the channel', (await page.$$('#aliasRows input')).length === 0);

  check('no uncaught page errors', errors.length === 0, errors.join(' | '));

  await browser.close();
  server.close();
  console.log(fails.length ? `\n${fails.length} FAILED: ${fails.join(', ')}` : '\nall alias editor checks passed');
  process.exit(fails.length ? 1 : 0);
})().catch(e => { console.error('harness error:', e); server.close(); process.exit(2); });