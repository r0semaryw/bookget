const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(require('node:path').join(__dirname, 'shidian-export.js'), 'utf8');

function reader(options = {}) {
  const calls = [];
  let output;
  let downloads = 0;
  const total = 43; // Two batches, including a short final batch.
  const context = {
    location: { hostname: 'www.shidianguji.com', origin: 'https://www.shidianguji.com', pathname: '/book/B1/chapter/C1' },
    window: {
      _ROUTER_DATA: { loaderData: { reader: { bookInfo: { bookId: 'B1', bookName: '测试书', totalPage: total, version: 2 } } } },
      byted_psdk: {
        getPtokenStatus: async () => options.hungToken ? new Promise(() => {}) : options.slowToken ? new Promise(resolve => setTimeout(() => resolve('session-token'), 30)) : 'session-token',
        decrypt: async (url) => options.decryptFail ? { decrypt: false } : { decrypt: true, data: url.replace('encrypted:', 'https://images.example/') },
      },
    },
    fetch: async (url, init) => {
      const body = JSON.parse(init.body);
      calls.push({ url, ...body });
      if (options.hungFetch) return new Promise(() => {});
      if (options.apiError) return { ok: true, json: async () => ({ errorCode: options.apiError === true ? 40001 : options.apiError }) };
      const start = (body.pageIndex - 1) * body.pageSize + 1;
      const end = Math.min(body.pageIndex * body.pageSize, total);
      const pages = Array.from({ length: end - start + 1 }, (_, i) => ({
        pageNum: options.duplicate ? 1 : start + i,
        pageId: String(start + i), picUrl: `encrypted:${start + i}.png`, urlEncryptType: 1,
        thumbUrl: 'https://images.example/thumbnail.png',
      }));
      if (options.missing && body.pageIndex === 2) pages.pop();
      return { ok: true, json: async () => ({ errorCode: 0, data: { pages } }) };
    },
    URL: { createObjectURL: (blob) => { output = blob; return 'blob:export'; }, revokeObjectURL() {} },
    Blob,
    document: { createElement: () => ({ click: () => { downloads++; } }) },
    AbortController,
    setTimeout: (fn, ms) => ms === 10000 ? fn() : setTimeout(fn, ms === 500 ? 0 : ms),
    clearTimeout,
    console: { info() {} },
  };
  if (options.staleBook) context.window._ROUTER_DATA.loaderData.reader.bookInfo.bookId = 'OLD';
  return { run: (settings) => vm.runInNewContext(settings ? source.replace('/* BOOKGET_OPTIONS */', JSON.stringify(settings)) : source, context), calls, output: () => output, downloads: () => downloads };
}

test('automatic mode returns the complete manifest without downloading a blob', async () => {
  const r = reader();
  const m = await r.run({ download: false, bookId: 'B1' });
  assert.equal(m.pages.length, 43);
  assert.equal(m.pages[42].pageNum, 43);
  assert.equal(r.downloads(), 0);
  assert.doesNotMatch(JSON.stringify(m), /session-token|ptoken|thumbnail/);
});

test('automatic mode propagates API codes for browser fallback', async () => {
  await assert.rejects(reader({ apiError: 31000 }).run({ download: false }), e => e.code === 31000);
});

test('rejects stale SSR and mismatched requested book before fetching', async () => {
  for (const [r, bookId] of [[reader({ staleBook: true }), 'B1'], [reader(), 'OTHER']]) {
    await assert.rejects(r.run({ download: false, bookId }));
    assert.equal(r.calls.length, 0);
  }
});

test('a late SDK response cannot export a manifest after timeout', async () => {
  const r = reader({ slowToken: true });
  await assert.rejects(r.run({ timeout: 10 }), /超时/);
  await new Promise(resolve => setTimeout(resolve, 40));
  assert.equal(r.calls.length, 0);
  assert.equal(r.downloads(), 0);
});

for (const field of ['hungToken', 'hungFetch']) {
  test(`times out when ${field} never completes`, async () => {
    const r = reader({ [field]: true });
    await assert.rejects(r.run({ download: false, timeout: 20 }), /超时/);
    assert.equal(r.downloads(), 0);
  });
}

test('exports all pages with original URLs, without tokens or thumbnails', async () => {
  const r = reader();
  await r.run();
  assert.equal(r.downloads(), 1);
  assert.equal(r.calls.length, 2);
  for (const req of r.calls) {
    assert.equal(req.startPageNum, 1);
    assert.equal(req.endPageNum, 44);
    assert.equal(req.isWebpSupported, false);
    assert.equal(req.imageResizeWidth, undefined);
    assert.equal(req.imageQvalue, undefined);
  }
  const text = await r.output().text();
  const m = JSON.parse(text);
  assert.equal(m.format, 'bookget-shidian-v1');
  assert.equal(m.pages.length, 43);
  assert.equal(m.pages[42].pageNum, 43);
  assert.equal(m.pages[42].url, 'https://images.example/43.png');
  assert.doesNotMatch(text, /session-token|encrypted:|thumbnail|ptoken|Cookie/);
});

for (const failure of ['apiError', 'decryptFail', 'duplicate', 'missing']) {
  test(`does not export an incomplete manifest on ${failure}`, async () => {
    const r = reader({ [failure]: true });
    await assert.rejects(r.run());
    assert.equal(r.downloads(), 0);
  });
}
