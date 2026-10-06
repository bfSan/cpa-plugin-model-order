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
// Rows carry force-mapping, exactly as the live table does: losing it is the
// regression this suite exists to catch (it turned qoder-deepseek-v4.1-flash
// into a 503 the first time a save went through an editor that dropped it).
const INITIAL = {
  'qoder': [
    { name: 'auto', alias: 'qoder-auto', 'force-mapping': true },
    { name: 'fast', alias: 'qoder-fast', 'force-mapping': true },
  ],
  'workbuddy': [{ name: 'space-bunny', alias: 'workbuddy-space-bunny', 'force-mapping': true }],
};

let table = null;      // what the stub "serves"
let patchLog = [];     // every PATCH body the page sent
let deleteLog = [];    // every DELETE the page sent

function reset() { table = JSON.parse(JSON.stringify(INITIAL)); patchLog = []; deleteLog = []; }

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
      // Mirror CPA exactly:
      //   * a body that is not {channel, aliases} -> 400 invalid channel
      //   * an EMPTY aliases array -> 404 channel not found, and NOTHING is
      //     stored. This is the behaviour that produced the user-visible
      //     "渠道不存在" when saving a freshly created, still-empty channel.
      //     The previous stub happily stored [] and so never caught it.
      if (!b || typeof b.channel !== 'string' || !Array.isArray(b.aliases)) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        return res.end(JSON.stringify({ error: 'invalid channel' }));
      }
      // Mirror CPA's SanitizeOAuthModelAlias() as well: it drops empty rows,
      // same-name rows (EqualFold) and duplicate aliases, and then — if the
      // channel is left empty — answers 404 channel not found. Without this the
      // suite cannot tell a panel-side guard from CPA's silent loss.
      const seenAlias = new Set();
      const clean = b.aliases.filter(r => {
        const name = String(r.name == null ? '' : r.name).trim();
        const alias = String(r.alias == null ? '' : r.alias).trim();
        if (!name || !alias) return false;
        if (name.toLowerCase() === alias.toLowerCase()) return false;
        const key = alias.toLowerCase();
        if (seenAlias.has(key)) return false;
        seenAlias.add(key);
        return true;
      });
      if (clean.length === 0) {
        res.writeHead(404, { 'Content-Type': 'application/json' });
        return res.end(JSON.stringify({ error: 'channel not found' }));
      }
      // PATCH replaces the whole channel, row fields included.
      table[b.channel] = clean;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ status: 'ok' }));
    }
    if (req.method === 'DELETE') {
      const ch = url.searchParams.get('channel');
      deleteLog.push(ch);
      if (!Object.prototype.hasOwnProperty.call(table, ch)) {
        res.writeHead(404, { 'Content-Type': 'application/json' });
        return res.end(JSON.stringify({ error: 'channel not found' }));
      }
      delete table[ch];
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
  // One global dialog handler for the whole run: confirm() prompts from save /
  // revert are accepted, prompt() is answered by the page-side override where a
  // test needs a specific channel name. Handlers installed per click raced with
  // dialogs opened by earlier actions, so there is exactly one.
  page.on('dialog', d => d.accept());
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

  // ---- regressions reported from the live deployment -------------------
  // Everything below was a real defect; each check names the symptom the
  // operator saw, so a future failure maps straight back to a bug report.

  // R1 (Bug 3): saving must not strip force-mapping.
  // Symptom: "保存一次之后别名就失效 / 我加的模型名过一会儿没了".
  // Re-load first so the dirty set starts clean after the edits above.
  await page.click('#btnAliasLoad');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length > 0, null, { timeout: 8000 });
  patchLog = [];
  // Edit one alias value on qoder (alphabetically first channel), then save.
  await page.click('#aliasChannels .chan-tab:nth-child(1)'); // qoder
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length === 4);
  const r1inputs = await page.$$('#aliasRows input');
  await r1inputs[1].fill('qoder-auto-renamed');
  await page.waitForFunction(() => !document.getElementById('btnAliasSave').disabled);
  await page.click('#btnAliasSave');
  // patchLog lives in Node, not in the page, so poll it from here: wait until
  // the save either landed in the log or the button came back disabled.
  for (let i = 0; i < 80 && patchLog.length === 0; i++) await page.waitForTimeout(100);
  await page.waitForTimeout(400);
  const r1sent = patchLog.find(p => p.channel === 'qoder');
  check('R1 PATCH sent the whole qoder channel', !!r1sent && r1sent.aliases.length === 2, JSON.stringify(r1sent && r1sent.aliases));
  check('R1 force-mapping survived the save',
    !!r1sent && r1sent.aliases.every(a => a['force-mapping'] === true),
    JSON.stringify(r1sent && r1sent.aliases));
  check('R1 the stub still holds force-mapping',
    table['qoder'].every(a => a['force-mapping'] === true), JSON.stringify(table['qoder']));
  check('R1 the edited value was actually written',
    table['qoder'].some(a => a.alias === 'qoder-auto-renamed'), JSON.stringify(table['qoder']));

  // R2 (Bug 2): a brand-new channel must render immediately instead of
  // throwing mid-render. Symptom: "自定义新增渠道，点添加，列表不实时展示".
  await page.click('#btnAliasLoad');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length > 0, null, { timeout: 8000 });
  const errsBefore = errors.length;
  // newChannel() uses prompt(); answer it from the page side so no dialog
  // handler is needed at all (a stray page.once('dialog') would race the
  // confirm() dialogs below).
  await page.evaluate(() => { window.prompt = () => 'brandnew'; });
  await page.click('#aliasChannels .chan-tab:last-child'); // "+ 渠道"
  await page.waitForFunction(() => {
    const t = [...document.querySelectorAll('.chan-tab')].map(e => e.childNodes[0].textContent);
    return t.includes('brandnew');
  }, null, { timeout: 5000 });
  check('R2 the new channel appears in the picker immediately', true);
  check('R2 no uncaught error while rendering the new channel', errors.length === errsBefore, errors.slice(errsBefore).join(' | '));
  // And it must be addable straight away (the old editor stopped rendering here).
  await page.fill('#aliasNewName', 'space-bunny');
  await page.fill('#aliasNewValue', 'brandnew-space-bunny');
  await page.click('#btnAliasAdd');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length === 2, null, { timeout: 5000 });
  check('R2 a row can be added to the new channel right away', true);

  // R3 (Bug 1): saving a new channel that still has rows must work, and an
  // empty channel must NOT be sent as an empty PATCH (CPA answers 404
  // "channel not found" — the exact message the operator saw).
  patchLog = [];
  await page.click('#btnAliasSave');
  await page.waitForTimeout(600);
  const r3sent = patchLog.find(p => p.channel === 'brandnew');
  check('R3 the new channel was PATCHed with its row', !!r3sent && r3sent.aliases.length === 1, JSON.stringify(r3sent && r3sent.aliases));
  check('R3 no empty-aliases PATCH was ever sent',
    patchLog.every(p => Array.isArray(p.aliases) && p.aliases.length > 0), JSON.stringify(patchLog));
  check('R3 the stub accepted it (no 404 surfaced)', Object.prototype.hasOwnProperty.call(table, 'brandnew'), JSON.stringify(Object.keys(table)));

  // R4: an emptied channel is deleted via DELETE, never PATCHed empty.
  await page.click('#btnAliasLoad');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length > 0, null, { timeout: 8000 });
  patchLog = []; deleteLog = [];
  await page.evaluate(() => {
    const tabs = [...document.querySelectorAll('.chan-tab')];
    const t = tabs.find(e => e.childNodes[0].textContent === 'brandnew');
    t.click();
  });
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length === 2);
  await page.click('#aliasRows tr:first-child button.mini');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length === 0, null, { timeout: 5000 });
  check('R4 emptied channel renders a delete-aware notice',
    (await page.$eval('#aliasRows td.empty', e => e.textContent)).includes('删除'));
  await page.click('#btnAliasSave');
  await page.waitForTimeout(600);
  check('R4 the emptied channel was DELETEd', deleteLog.includes('brandnew'), JSON.stringify(deleteLog));
  check('R4 the emptied channel was not PATCHed empty',
    patchLog.every(p => p.channel !== 'brandnew'), JSON.stringify(patchLog));

  // R5 (Bug 4): CPA silently drops same-name and duplicate-alias rows.
  // Symptom: name=gpt-6.1-sol / alias=gpt-6.1-sol answered 404 "channel not
  // found", which looks like a channel-name problem but is really CPA's
  // SanitizeOAuthModelAlias() dropping the row and then emptying the channel.
  await page.click('#btnAliasLoad');
  await page.waitForFunction(() => document.querySelectorAll('#aliasRows input').length > 0, null, { timeout: 8000 });
  // (a) the add-row path refuses a same-name row outright.
  await page.evaluate(() => { window.prompt = () => 'dupchan'; });
  await page.click('#aliasChannels .chan-tab:last-child'); // "+ 渠道"
  await page.waitForTimeout(300);
  await page.fill('#aliasNewName', 'gpt-6.1-sol');
  await page.fill('#aliasNewValue', 'gpt-6.1-sol');
  await page.click('#btnAliasAdd');
  await page.waitForTimeout(300);
  check('R5 same-name row is refused at input time',
    (await page.$$('#aliasRows input')).length === 0,
    JSON.stringify(await page.$$eval('#aliasRows input', e => e.map(x => x.value))));
  check('R5 the refusal explains why',
    (await page.$$eval('.toast', e => e.map(x => x.textContent))).some(t => t.includes('不能相同')),
    JSON.stringify(await page.$$eval('.toast', e => e.map(x => x.textContent))));
  // (b) a row that differs only by case is refused too (CPA uses EqualFold).
  await page.fill('#aliasNewName', 'GPT-6.1-Sol');
  await page.fill('#aliasNewValue', 'gpt-6.1-sol');
  await page.click('#btnAliasAdd');
  await page.waitForTimeout(300);
  check('R5 case-only difference is refused as well',
    (await page.$$('#aliasRows input')).length === 0);
  // (c) a duplicate alias inside one channel is refused.
  await page.fill('#aliasNewName', 'alpha');
  await page.fill('#aliasNewValue', 'dup-target');
  await page.click('#btnAliasAdd');
  await page.waitForTimeout(200);
  await page.fill('#aliasNewName', 'beta');
  await page.fill('#aliasNewValue', 'dup-target');
  await page.click('#btnAliasAdd');
  await page.waitForTimeout(300);
  const r5rows = await page.$$eval('#aliasRows input', e => e.map(x => x.value));
  check('R5 duplicate alias is refused', r5rows.filter(v => v === 'dup-target').length === 1, JSON.stringify(r5rows));
  // (d) nothing that CPA would drop is ever sent: keep the channel valid and save.
  patchLog = [];
  await page.click('#btnAliasSave');
  await page.waitForTimeout(1200);
  const r5sent = patchLog.find(p => p.channel === 'dupchan');
  check('R5 a valid channel still saves normally', !!r5sent && r5sent.aliases.length === 1, JSON.stringify(r5sent && r5sent.aliases));
  // (e) a same-name row that somehow reaches the draft must be flagged before send.
  await page.evaluate(() => {
    // Simulate the edit-an-existing-row path, which the pre-flight must catch.
    aliasDraft['dupchan'].push({ name: 'same', alias: 'same' });
    aliasTouched();
  });
  await page.waitForTimeout(300);
  const preflightShown = await page.evaluate(() => {
    const before = window.confirm;
    let seen = null;
    window.confirm = msg => { seen = msg; return false; };
    return saveAliases().then(() => { window.confirm = before; return seen; });
  });
  await page.waitForTimeout(600);
  check('R5 pre-flight names the rows CPA would drop',
    !!preflightShown && preflightShown.includes('丢弃') && preflightShown.includes('same'),
    JSON.stringify(preflightShown));
  // Clean up the channel this test created.
  await page.evaluate(async () => {
    window.confirm = () => true;
    aliasDraft['dupchan'] = [];
    aliasTouched();
    await saveAliases();
  });
  await page.waitForTimeout(1200);

  check('no uncaught page errors', errors.length === 0, errors.join(' | '));

  await browser.close();
  server.close();
  console.log(fails.length ? `\n${fails.length} FAILED: ${fails.join(', ')}` : '\nall alias editor checks passed');
  process.exit(fails.length ? 1 : 0);
})().catch(e => { console.error('harness error:', e); server.close(); process.exit(2); });