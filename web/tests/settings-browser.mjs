// Real UI configuration, offline storage migration, rollback and endpoint changes.
import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { env } from 'node:process';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const fixture = await mkdtemp(join(tmpdir(), 'sharefile-settings-browser-'));
const screenshots = join(root, 'web/test-results');
await mkdir(screenshots, { recursive: true });
const password = 'test-' + randomBytes(16).toString('hex');
async function port() {
  const s = createServer();
  await new Promise((resolve) => s.listen(0, '127.0.0.1', resolve));
  const p = s.address().port;
  await new Promise((resolve) => s.close(resolve));
  return p;
}
const publicPort = await port(),
  adminPort = await port();
const publicURL = `http://127.0.0.1:${publicPort}`,
  adminURL = `http://127.0.0.1:${adminPort}`;
const configPath = join(fixture, 'config.json'),
  binary = join(root, 'backend/bin/sharefile');
const original = join(fixture, 'storage'),
  destination = join(fixture, 'new-storage');
const importDir = join(fixture, 'imports');
await mkdir(importDir);
await writeFile(join(importDir, 'existing.txt'), 'keep');
const config = {
  data_dir: original,
  volume_id: 'settings-test',
  public_addr: `127.0.0.1:${publicPort}`,
  public_url: publicURL,
  admin_addr: `127.0.0.1:${adminPort}`,
  admin_url: adminURL,
  insecure_local: true,
  web_dir: join(root, 'web/dist'),
  import_dirs: [importDir],
  upload_slots: 2,
  download_slots: 4,
  chunk_bytes: 1048576,
  reserve_bytes: 0,
  log_file: join(fixture, 'server.jsonl'),
};
await writeFile(configPath, JSON.stringify(config));
for (const [cmd, extra] of [
  ['init-storage', []],
  ['admin-create', ['-username', 'admin', '-password-stdin']],
]) {
  const result = spawnSync(binary, [cmd, '-config', configPath, ...extra], {
    input: password + '\n',
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stderr);
}
const server = spawn(binary, ['serve', '-config', configPath], {
  stdio: ['ignore', 'ignore', 'pipe'],
});
let stderr = '';
server.stderr.on('data', (data) => (stderr += data.toString()));
let browser, page;
const errors = [];
const visible = (locator) => locator.waitFor({ state: 'visible', timeout: 15000 });
async function waitFor(check) {
  for (let i = 0; i < 150; i++) {
    if (server.exitCode !== null) throw new Error('Server exited: ' + stderr);
    try {
      if (await check()) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error('Timed out waiting for configuration');
}
try {
  await waitFor(async () => (await fetch(adminURL + '/readyz')).ok);
  browser = await chromium.launch({ headless: true, channel: env.PLAYWRIGHT_CHANNEL || undefined });
  const admin = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  page = await admin.newPage();
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => {
    if (/Refused|violat.*Content Security/i.test(m.text())) errors.push(m.text());
  });
  await page.goto(adminURL);
  await page.getByLabel('Имя пользователя', { exact: true }).fill('admin');
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Войти в хранилище', exact: true }).click();
  await visible(page.getByRole('navigation', { name: 'Основное меню' }));
  const me = await (await admin.request.get(adminURL + '/api/v1/me')).json();
  const headers = { 'X-CSRF-Token': me.csrf_token };
  const createdUser = await admin.request.post(adminURL + '/api/v1/admin/users', {
    headers,
    data: { username: 'alice', password, admin: false },
  });
  assert.equal(createdUser.status(), 201);
  const alice = await createdUser.json();
  const user = await browser.newContext();
  assert.equal(
    (
      await user.request.post(publicURL + '/api/v1/auth/login', {
        data: { username: 'alice', password },
      })
    ).status(),
    200,
  );
  const userMe = await (await user.request.get(publicURL + '/api/v1/me')).json();
  const userHeaders = { 'X-CSRF-Token': userMe.csrf_token };
  assert.equal((await user.request.get(publicURL + '/api/v1/admin/config')).status(), 404);
  const tusHeaders = { ...userHeaders, 'Tus-Resumable': '1.0.0' };
  async function upload(name, data, partial = false) {
    const response = await user.request.post(publicURL + '/api/v1/uploads', {
      headers: {
        ...tusHeaders,
        'Upload-Length': String(data.length),
        'Upload-Metadata': `filename ${Buffer.from(name).toString('base64')},folder_id ${Buffer.from(alice.home_id).toString('base64')}`,
      },
    });
    assert.equal(response.status(), 201);
    const url = response.headers().location;
    assert.equal(
      (
        await user.request.patch(url, {
          data: partial ? data.subarray(0, 3) : data,
          headers: {
            ...tusHeaders,
            'Content-Type': 'application/offset+octet-stream',
            'Upload-Offset': '0',
          },
        })
      ).status(),
      204,
    );
    return { url, id: response.headers()['sharefile-file-id'] };
  }
  const payload = Buffer.alloc(512000, 'migration-content');
  const file = await upload('preserved.txt', payload);
  const partial = await upload('partial.txt', Buffer.from('abcdef'), true);
  const shareResponse = await user.request.post(publicURL + '/api/v1/shares', {
    headers: userHeaders,
    data: { node_id: file.id, never_expires: true },
  });
  assert.equal(shareResponse.status(), 201);
  const shareURL = (await shareResponse.json()).url;
  await page.getByRole('button', { name: 'Настройки', exact: true }).click();
  await visible(page.getByLabel('Директория хранения', { exact: false }));
  const volumes = await (await admin.request.get(adminURL + '/api/v1/admin/volumes')).json();
  assert.ok(volumes.length > 0);
  const volumeSelect = page.getByLabel('Подключённый диск', { exact: true });
  await volumeSelect.selectOption(volumes.find((v) => !v.read_only).path);
  assert.match(
    await page.getByLabel('Директория хранения', { exact: false }).inputValue(),
    /\/sharefile$/,
  );
  await page.getByLabel('Директория хранения', { exact: false }).fill(original);
  await page.getByLabel('Срок хранения, часов').fill('48');
  await page.getByRole('button', { name: 'Сохранить настройки', exact: true }).click();
  await visible(page.getByText('Настройки сохранены', { exact: true }));
  await page.getByLabel('Одновременные загрузки', { exact: true }).fill('3');
  await page.getByRole('button', { name: 'Сохранить черновик', exact: true }).click();
  await visible(page.getByText('Черновик настроек сохранён', { exact: true }));
  assert.equal(JSON.parse(await readFile(configPath, 'utf8')).upload_slots, 2);
  await page.reload();
  await visible(page.getByLabel('Одновременные загрузки', { exact: true }));
  assert.equal(await page.getByLabel('Одновременные загрузки', { exact: true }).inputValue(), '3');
  await page.getByRole('button', { name: 'Удалить черновик', exact: true }).click();
  await visible(page.getByText('Черновик удалён', { exact: true }));
  assert.equal(await page.getByLabel('Одновременные загрузки', { exact: true }).inputValue(), '2');
  await page.getByLabel('Директория хранения', { exact: false }).fill(importDir);
  await page.getByRole('button', { name: 'Сохранить черновик', exact: true }).click();
  await visible(page.getByRole('alert').filter({ hasText: 'должна быть пустой' }));
  await page.getByLabel('Директория хранения', { exact: false }).fill(destination);
  await page.getByLabel('Метка хранилища', { exact: false }).fill('new-volume');
  await page.getByLabel('Одновременные загрузки', { exact: true }).fill('3');
  await page.getByLabel('Одновременные скачивания', { exact: true }).fill('5');
  await page.getByLabel('Размер части загрузки, байт', { exact: false }).fill('524288');
  await page.getByLabel('Резерв свободного места, байт', { exact: false }).fill('1048576');
  await page.getByLabel('Файл журнала', { exact: false }).fill(join(fixture, 'new-log.jsonl'));
  await page.getByLabel('Директории импорта', { exact: true }).fill(importDir + '\n' + fixture);
  await page.getByRole('button', { name: 'Сохранить черновик', exact: true }).click();
  await visible(page.getByText('Черновик настроек сохранён', { exact: true }));
  await page.screenshot({ path: join(screenshots, 'server-settings-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    true,
  );
  await page.screenshot({ path: join(screenshots, 'server-settings-mobile.png') });
  await page
    .getByRole('button', { name: 'Применить и перезапустить', exact: true })
    .scrollIntoViewIfNeeded();
  await visible(page.getByRole('button', { name: 'Применить и перезапустить', exact: true }));
  await page.screenshot({ path: join(screenshots, 'server-settings-mobile-actions.png') });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('button', { name: 'Применить и перезапустить', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Применить', exact: true }).click();
  await waitFor(async () => {
    const s = await (await admin.request.get(adminURL + '/api/v1/admin/config')).json();
    return s.active.data_dir === destination && !s.applying;
  });
  await page.reload();
  await visible(page.getByLabel('Директория хранения', { exact: false }));
  assert.equal(
    await page.getByLabel('Директория хранения', { exact: false }).inputValue(),
    destination,
  );
  assert.equal(await readFile(join(original, '.sharefile-volume'), 'utf8'), 'settings-test\n');
  assert.equal(await readFile(join(destination, '.sharefile-volume'), 'utf8'), 'new-volume\n');
  const stored = JSON.parse(await readFile(configPath, 'utf8'));
  assert.equal(stored.upload_slots, 3);
  assert.equal(stored.download_slots, 5);
  assert.deepEqual(stored.import_dirs, [importDir, fixture]);
  assert.equal(
    (await (await admin.request.get(adminURL + '/api/v1/admin/settings')).json())
      .upload_ttl_seconds,
    48 * 3600,
  );
  assert.equal(
    (await (await user.request.get(publicURL + '/api/v1/ui')).json()).chunk_bytes,
    524288,
  );
  assert.deepEqual(
    await (await user.request.get(publicURL + '/api/v1/nodes/' + file.id + '/content')).body(),
    payload,
  );
  assert.deepEqual(Buffer.from(await (await fetch(shareURL)).arrayBuffer()), payload);
  const offset = await user.request.head(partial.url, { headers: { 'Tus-Resumable': '1.0.0' } });
  assert.equal(offset.status(), 200);
  assert.equal(offset.headers()['upload-offset'], '3');
  assert.equal(
    (
      await user.request.patch(partial.url, {
        data: Buffer.from('def'),
        headers: {
          ...tusHeaders,
          'Content-Type': 'application/offset+octet-stream',
          'Upload-Offset': '3',
        },
      })
    ).status(),
    204,
  );
  assert.equal(
    await (await user.request.get(publicURL + '/api/v1/nodes/' + partial.id + '/content')).text(),
    'abcdef',
  );
  const occupied = createServer();
  await new Promise((resolve) => occupied.listen(0, '127.0.0.1', resolve));
  try {
    await page
      .getByLabel('Адрес публичного сервера', { exact: false })
      .fill(`127.0.0.1:${occupied.address().port}`);
    await page.getByRole('button', { name: 'Сохранить черновик', exact: true }).click();
    await visible(page.getByText('Черновик настроек сохранён', { exact: true }));
    await page.getByRole('button', { name: 'Применить и перезапустить', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Применить', exact: true }).click();
    await visible(page.getByRole('alert').filter({ hasText: 'Настройки не применены' }));
    assert.equal(JSON.parse(await readFile(configPath, 'utf8')).public_addr, config.public_addr);
    assert.equal((await user.request.get(publicURL + '/readyz')).status(), 200);
    await page.getByRole('button', { name: 'Удалить черновик', exact: true }).click();
    await visible(page.getByText('Черновик удалён', { exact: true }));
  } finally {
    await new Promise((resolve) => occupied.close(resolve));
  }
  // Change both administrative address and URL; navigate without forwarding CSRF.
  const newAdminPort = await port(),
    newAdminURL = `http://127.0.0.1:${newAdminPort}`;
  await page
    .getByLabel('Адрес административного сервера', { exact: false })
    .fill(`127.0.0.1:${newAdminPort}`);
  await page.getByLabel('URL администрирования', { exact: false }).fill(newAdminURL);
  await page.getByRole('button', { name: 'Сохранить черновик', exact: true }).click();
  await visible(page.getByText('Черновик настроек сохранён', { exact: true }));
  await page.getByRole('button', { name: 'Применить и перезапустить', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Применить', exact: true }).click();
  await visible(page.getByRole('link', { name: 'Открыть новый адрес' }));
  await waitFor(async () => (await fetch(newAdminURL + '/readyz')).ok);
  await page.getByRole('link', { name: 'Открыть новый адрес' }).click();
  await visible(page.getByLabel('URL администрирования', { exact: false }));
  assert.equal(new URL(page.url()).origin, newAdminURL);
  assert.equal(
    await page.getByLabel('URL администрирования', { exact: false }).inputValue(),
    newAdminURL,
  );
  assert.deepEqual(errors, []);
  console.log(
    'Settings browser acceptance passed: mounted disks, all-field form, drafts/reload/discard, unsafe target validation, storage migration, preserved users/sessions/links/partial uploads/TTL, applied limits and paths, rollback on occupied port, new admin URL and mobile layout.',
  );
} catch (e) {
  await page
    ?.screenshot({ path: join(screenshots, 'settings-failure.png'), fullPage: true })
    .catch(() => {});
  throw e;
} finally {
  if (server.exitCode === null) {
    const exited = new Promise((resolve) => server.once('exit', resolve));
    server.kill('SIGTERM');
    await exited;
  }
  await browser?.close();
  await rm(fixture, { recursive: true, force: true });
}
