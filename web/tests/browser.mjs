// Real-browser acceptance against an isolated Go process and disposable storage.
import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const fixture = await mkdtemp(join(tmpdir(), 'sharefile-browser-'));
const screenshots = join(root, 'web/test-results');
await mkdir(screenshots, { recursive: true });
const password = `browser-test-${randomBytes(12).toString('hex')}`; // Disposable, never a real account.
async function port() {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}
const publicPort = await port(),
  adminPort = await port();
const publicURL = `http://127.0.0.1:${publicPort}`,
  adminURL = `http://127.0.0.1:${adminPort}`;
const config = join(fixture, 'config.json'),
  binary = join(root, 'backend/bin/sharefile');
const importDir = join(fixture, 'import');
await mkdir(importDir);
await writeFile(join(importDir, 'example.txt'), 'imported file');
await writeFile(
  config,
  JSON.stringify({
    data_dir: join(fixture, 'storage'),
    volume_id: 'browser-test-volume',
    public_addr: `127.0.0.1:${publicPort}`,
    admin_addr: `127.0.0.1:${adminPort}`,
    public_url: publicURL,
    admin_url: adminURL,
    insecure_local: true,
    web_dir: join(root, 'web/dist'),
    import_dirs: [importDir],
    reserve_bytes: 0,
    upload_slots: 2,
    download_slots: 4,
    chunk_bytes: 1048576,
    log_file: join(fixture, 'server.jsonl'),
  }),
);
for (const [command, extra] of [
  ['init-storage', []],
  ['admin-create', ['-username', 'admin', '-password-stdin']],
]) {
  const result = spawnSync(binary, [command, '-config', config, ...extra], {
    input: password + '\n',
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stderr);
}
let browser;
const process = spawn(binary, ['serve', '-config', config], {
  stdio: ['ignore', 'ignore', 'pipe'],
});
let serverError = '';
process.stderr.on('data', (data) => (serverError += data.toString()));
const errors = [];
async function visible(locator) {
  await locator.waitFor({ state: 'visible', timeout: 10000 });
}
async function login(page, url, username) {
  await page.goto(url);
  await page.getByLabel('Имя пользователя', { exact: true }).fill(username);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Войти в хранилище', exact: true }).click();
  await visible(page.getByRole('navigation', { name: 'Основное меню' }));
}
function observe(page) {
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => {
    if (/Refused|violat.*Content Security/i.test(message.text())) errors.push(message.text());
  });
}
try {
  for (let i = 0; i < 100; i++) {
    if (process.exitCode !== null) throw new Error('Backend exited: ' + serverError);
    try {
      if ((await fetch(publicURL + '/readyz')).ok) break;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 50));
    if (i === 99) throw new Error('Backend did not start');
  }
  browser = await chromium.launch({ headless: true });
  const adminContext = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const admin = await adminContext.newPage();
  observe(admin);
  await admin.goto(adminURL);
  await visible(admin.getByRole('heading', { name: 'Управление хранилищем' }));
  await admin.screenshot({ path: join(screenshots, 'login-desktop.png'), fullPage: true });
  await login(admin, adminURL, 'admin');
  await admin.getByRole('button', { name: 'Пользователи', exact: true }).click();
  await admin.getByRole('button', { name: 'Добавить пользователя' }).click();
  await admin.getByRole('dialog').getByLabel('Имя пользователя').fill('alice');
  await admin.getByRole('dialog').getByLabel('Пароль', { exact: true }).fill(password);
  await admin.getByRole('dialog').getByRole('button', { name: 'Сохранить', exact: true }).click();
  await visible(admin.getByText('alice', { exact: true }));
  await admin.getByRole('button', { name: 'Группы', exact: true }).click();
  await admin.getByRole('button', { name: 'Создать группу' }).click();
  await admin.getByLabel('Название группы').fill('Семья');
  await admin.getByRole('dialog').getByRole('button', { name: 'Создать', exact: true }).click();
  await visible(admin.getByText('Семья', { exact: true }));
  await admin.getByRole('button', { name: 'Участники', exact: true }).click();
  await admin
    .getByRole('dialog')
    .getByText('alice', { exact: true })
    .locator('..')
    .getByRole('checkbox')
    .check();
  await visible(admin.getByText('Состав группы обновлён', { exact: true }));
  await admin.getByRole('dialog').getByRole('button', { name: 'Готово' }).click();
  await admin.getByRole('button', { name: 'Мои файлы', exact: true }).click();
  await admin.getByRole('button', { name: 'Новая папка', exact: true }).click();
  await admin.getByRole('dialog').getByLabel('Название', { exact: true }).fill('Проекты');
  await admin.getByRole('dialog').getByRole('button', { name: 'Сохранить' }).click();
  await admin.getByRole('button', { name: 'Проекты', exact: true }).click();
  const projects = new URL(admin.url()).hash.split('/').at(-1);
  await admin.getByRole('button', { name: 'Доступ к текущей папке' }).click();
  const acl = admin.getByRole('dialog');
  await acl.getByLabel('Кому', { exact: true }).selectOption({ label: 'alice' });
  await acl.getByRole('button', { name: 'Добавить доступ' }).click();
  await visible(acl.getByLabel('Роль alice', { exact: true }));
  await acl.getByRole('button', { name: 'Готово' }).click();
  const userContext = await browser.newContext({ viewport: { width: 1440, height: 960 } });
  const user = await userContext.newPage();
  observe(user);
  await login(user, publicURL, 'alice');
  assert.equal(await user.getByRole('navigation', { name: 'Администрирование' }).count(), 0);
  assert.equal((await userContext.request.get(publicURL + '/api/v1/admin/users')).status(), 404);
  await user.getByRole('button', { name: 'Доступные папки', exact: true }).click();
  await user.getByRole('button', { name: 'Проекты', exact: true }).click();
  await visible(user.getByText('Читатель', { exact: true }));
  assert.equal(await user.getByRole('button', { name: 'Загрузить файлы', exact: true }).count(), 0);
  await admin.getByRole('button', { name: 'Доступ к текущей папке' }).click();
  await acl.getByLabel('Роль alice', { exact: true }).selectOption('3');
  await acl.getByRole('button', { name: 'Готово' }).click();
  await user.reload();
  await visible(user.getByRole('button', { name: 'Загрузить файлы', exact: true }));
  const payload = Buffer.alloc(2300000, 'browser-test-content\n');
  await user
    .getByLabel('Выбрать файлы для загрузки')
    .setInputFiles({ name: 'отчёт.txt', mimeType: 'text/plain', buffer: payload });
  await visible(user.getByRole('link', { name: 'отчёт.txt', exact: true }));
  await visible(user.getByText('Сохранён в хранилище', { exact: true }));
  const href = await user
    .getByRole('link', { name: 'отчёт.txt', exact: true })
    .getAttribute('href');
  const downloaded = await userContext.request.get(publicURL + href);
  assert.equal(
    createHash('sha256')
      .update(await downloaded.body())
      .digest('hex'),
    createHash('sha256').update(payload).digest('hex'),
  );
  await user.getByRole('button', { name: 'Поделиться отчёт.txt', exact: true }).click();
  await user.getByRole('dialog').getByRole('button', { name: 'Создать ссылку' }).click();
  const linkInput = user.getByLabel('Созданная публичная ссылка');
  await visible(linkInput);
  const shareURL = await linkInput.inputValue();
  const anonymous = await browser.newContext();
  const shared = await anonymous.request.get(shareURL);
  assert.equal(shared.status(), 200);
  assert.equal((await shared.body()).length, payload.length);
  await user.screenshot({ path: join(screenshots, 'sharing-desktop.png'), fullPage: true });
  await user.getByRole('dialog').getByRole('button', { name: 'Закрыть', exact: true }).click();
  await user.getByLabel('Действия отчёт.txt', { exact: true }).click();
  await user.getByRole('button', { name: 'Переименовать', exact: true }).click();
  await user.getByRole('dialog').getByLabel('Название', { exact: true }).fill('Документ.txt');
  await user.getByRole('dialog').getByRole('button', { name: 'Сохранить' }).click();
  await visible(user.getByRole('link', { name: 'Документ.txt', exact: true }));
  await user.getByLabel('Действия Документ.txt', { exact: true }).click();
  await user.getByRole('button', { name: 'Заменить файлом' }).click();
  await user
    .getByRole('dialog')
    .getByLabel('Новый файл')
    .setInputFiles({
      name: 'different-source.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from('replacement'),
    });
  await user.getByRole('dialog').getByRole('button', { name: 'Загрузить замену' }).click();
  await visible(user.getByText('Сохранён в хранилище', { exact: true }).nth(1));
  await user.getByRole('button', { name: 'Обновить файлы' }).click();
  const replacement = await user
    .getByRole('link', { name: 'Документ.txt', exact: true })
    .getAttribute('href');
  assert.notEqual(replacement, href);
  assert.equal(
    await (await userContext.request.get(publicURL + replacement)).text(),
    'replacement',
  );
  assert.equal((await anonymous.request.get(shareURL)).status(), 404);
  await user.getByRole('button', { name: 'Новая папка', exact: true }).click();
  await user.getByRole('dialog').getByLabel('Название', { exact: true }).fill('Архив');
  await user.getByRole('dialog').getByRole('button', { name: 'Сохранить' }).click();
  await user.getByLabel('Действия Документ.txt', { exact: true }).click();
  await user.getByRole('button', { name: 'Переместить', exact: true }).click();
  await user.locator('.folder-picker').getByRole('button', { name: 'Архив', exact: true }).click();
  await user.getByRole('dialog').getByRole('button', { name: 'Сохранить' }).click();
  await user.getByRole('button', { name: 'Архив', exact: true }).click();
  await visible(user.getByRole('link', { name: 'Документ.txt', exact: true }));
  await user.getByLabel('Действия Документ.txt', { exact: true }).click();
  await user.getByRole('button', { name: 'Переместить', exact: true }).click();
  await user
    .locator('.folder-picker')
    .getByRole('button', { name: 'В начало', exact: true })
    .click();
  await user
    .locator('.folder-picker')
    .getByRole('button', { name: 'Проекты', exact: true })
    .click();
  await user.getByRole('dialog').getByRole('button', { name: 'Сохранить' }).click();
  await user.getByRole('button', { name: 'Проекты', exact: true }).click();
  await visible(user.getByRole('link', { name: 'Документ.txt', exact: true }));
  await user.getByLabel('Действия Документ.txt', { exact: true }).click();
  await user.getByRole('button', { name: 'В корзину', exact: true }).click();
  await user.getByRole('dialog').getByRole('button', { name: 'В корзину', exact: true }).click();
  await user.getByRole('button', { name: 'Корзина', exact: true }).click();
  await visible(user.getByText('Документ.txt', { exact: true }).first());
  await user
    .locator('.item-row')
    .filter({ hasText: 'Документ.txt' })
    .filter({ hasText: '11 Б' })
    .getByRole('button', { name: 'Восстановить', exact: true })
    .click();
  await user.getByRole('dialog').getByRole('button', { name: 'Восстановить', exact: true }).click();
  await user.goto(publicURL + `/#/files/${projects}`);
  await visible(user.getByRole('link', { name: 'Документ.txt', exact: true }));
  await user.screenshot({ path: join(screenshots, 'files-desktop.png'), fullPage: true });
  // Lose a real response after the server durably accepts a chunk, then reload and reselect.
  const resumeFile = join(fixture, 'resume.txt');
  await writeFile(resumeFile, payload);
  let lost = false;
  await user.route('**/api/v1/uploads/*', async (route) => {
    if (!lost && route.request().headers()['x-http-method-override'] === 'PATCH') {
      lost = true;
      const accepted = await route.fetch();
      assert.equal(accepted.status(), 204);
      await route.abort('failed');
    } else await route.continue();
  });
  await user.getByLabel('Выбрать файлы для загрузки').setInputFiles(resumeFile);
  await visible(
    user
      .locator('.upload-job')
      .filter({ hasText: 'resume.txt' })
      .getByText('Нет связи с сервером.', { exact: false }),
  );
  await user.reload();
  const resumed = user.locator('.upload-job').filter({ hasText: 'resume.txt' });
  await visible(resumed.getByText('Выберите исходный файл, чтобы продолжить', { exact: true }));
  await resumed.locator('input[type=file]').setInputFiles(resumeFile);
  await visible(user.getByRole('link', { name: 'resume.txt', exact: true }));
  await user.unroute('**/api/v1/uploads/*');
  const resumedHref = await user
    .getByRole('link', { name: 'resume.txt', exact: true })
    .getAttribute('href');
  const resumedBody = await (await userContext.request.get(publicURL + resumedHref)).body();
  assert.equal(
    createHash('sha256').update(resumedBody).digest('hex'),
    createHash('sha256').update(payload).digest('hex'),
  );
  // Browser pairing must explicitly approve; raw device tokens never enter UI storage.
  const request = await (
    await anonymous.request.post(publicURL + '/api/v1/device-requests', {
      data: { name: 'Test desktop' },
    })
  ).json();
  await user.goto(request.verification_url);
  await visible(user.getByRole('heading', { name: 'Подключение устройства' }));
  await user.getByRole('button', { name: 'Разрешить доступ', exact: true }).click();
  await visible(user.getByText('Подключение разрешено. Вернитесь в приложение.'));
  const token = await anonymous.request.post(
    publicURL + `/api/v1/device-requests/${request.request.id}/token`,
    { data: { secret: request.request.secret } },
  );
  assert.equal(token.status(), 200);
  await user.getByRole('button', { name: 'Обновить', exact: true }).click();
  await visible(user.getByText('Test desktop', { exact: true }));
  await user.getByRole('button', { name: 'Отозвать доступ', exact: true }).click();
  await user.getByRole('dialog').getByRole('button', { name: 'Отключить', exact: true }).click();
  await visible(user.getByText('Отозвано', { exact: true }));
  await admin.getByRole('button', { name: 'Настройки', exact: true }).click();
  await admin.getByLabel('Срок хранения, часов').fill('48');
  await admin.getByRole('button', { name: 'Сохранить настройки' }).click();
  await visible(admin.getByText('Настройки сохранены', { exact: true }));
  await admin.getByRole('button', { name: 'Импортировать файл' }).click();
  await admin.getByLabel('Путь относительно источника').fill('example.txt');
  await admin.getByRole('dialog').getByRole('button', { name: 'Скопировать файл' }).click();
  await visible(admin.getByText('Файл скопирован в хранилище', { exact: true }));
  await admin.screenshot({ path: join(screenshots, 'settings-desktop.png'), fullPage: true });
  // Responsive layout and horizontal overflow, including real loaded rows.
  await user.goto(publicURL + `/#/files/${projects}`);
  await visible(user.getByRole('link', { name: 'Документ.txt', exact: true }));
  await user.setViewportSize({ width: 390, height: 844 });
  await user.waitForFunction(
    () => document.querySelector('.sidebar').getBoundingClientRect().right <= 1,
  );
  assert.equal(
    await user.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    true,
  );
  await user.screenshot({ path: join(screenshots, 'files-mobile.png'), fullPage: true });
  await user.getByRole('button', { name: 'Открыть меню' }).click();
  await user.getByRole('button', { name: 'Мои файлы', exact: true }).click();
  await visible(user.getByRole('heading', { name: 'alice', exact: true }));
  await user.setViewportSize({ width: 1440, height: 960 });
  await user.waitForFunction(
    () => getComputedStyle(document.querySelector('.sidebar')).visibility === 'visible',
  );
  await user.getByRole('button', { name: 'Выйти', exact: true }).click();
  await visible(user.getByRole('heading', { name: 'Войти в ShareFile' }));
  const publicAdmin = await browser.newContext();
  const publicAdminPage = await publicAdmin.newPage();
  observe(publicAdminPage);
  await login(publicAdminPage, publicURL, 'admin');
  assert.equal(
    await publicAdminPage.getByRole('navigation', { name: 'Администрирование' }).count(),
    0,
  );
  assert.equal((await publicAdmin.request.get(publicURL + '/api/v1/admin/users')).status(), 404);
  assert.deepEqual(errors, [], 'Browser errors/CSP violations');
  console.log(
    'Browser acceptance passed: auth, private admin, users/groups/ACL, reader/manager UI, chunked upload and SHA-256, sharing, rename/move/replace/revoke/trash/restore, real lost-response upload resume after reload with SHA-256, device pairing/revocation, settings/import, mobile layout, logout.',
  );
} catch (error) {
  if (browser)
    for (const [index, context] of browser.contexts().entries())
      for (const [pageIndex, page] of context.pages().entries())
        await page
          .screenshot({
            path: join(screenshots, `failure-${index}-${pageIndex}.png`),
            fullPage: true,
          })
          .catch(() => {});
  throw error;
} finally {
  if (process.exitCode === null) {
    const exited = new Promise((resolve) => process.once('exit', resolve));
    process.kill('SIGTERM');
    await exited;
  }
  await browser?.close();
  await rm(fixture, { recursive: true, force: true });
}
