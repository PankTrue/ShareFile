// Real-browser acceptance against an isolated Go process and disposable storage.
import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { env } from 'node:process';
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
async function downloadArchive(page, filename, expected) {
  const pending = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Скачать всё (ZIP)', exact: true }).click();
  const download = await pending;
  assert.equal(download.suggestedFilename(), filename);
  assert.equal(await download.failure(), null);
  const result = spawnSync(
    'python3',
    [
      '-c',
      'import hashlib,json,sys,zipfile\nwith zipfile.ZipFile(sys.argv[1]) as z:\n assert z.testzip() is None\n print(json.dumps({i.filename: hashlib.sha256(z.read(i)).hexdigest() for i in z.infolist()}))',
      await download.path(),
    ],
    { encoding: 'utf8' },
  );
  assert.equal(result.status, 0, result.stderr);
  const hashes = Object.fromEntries(
    Object.entries(expected).map(([name, data]) => [
      name,
      createHash('sha256').update(data).digest('hex'),
    ]),
  );
  assert.deepEqual(JSON.parse(result.stdout), hashes);
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
  browser = await chromium.launch({ headless: true, channel: env.PLAYWRIGHT_CHANNEL || undefined });
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
  // Share a live folder through the UI; guests can navigate and preview without an account.
  await user.goto(publicURL + `/#/files/${projects}`);
  await user.getByRole('button', { name: 'Новая папка', exact: true }).click();
  await user.getByRole('dialog').getByLabel('Название', { exact: true }).fill('Для друзей');
  await user.getByRole('dialog').getByRole('button', { name: 'Сохранить' }).click();
  await user.getByRole('button', { name: 'Для друзей', exact: true }).click();
  const sharedFolderID = new URL(user.url()).hash.split('/').at(-1);
  await user.getByRole('button', { name: 'Поделиться папкой', exact: true }).click();
  await user.getByRole('dialog').getByLabel('Пароль', { exact: false }).fill(password);
  await user.getByRole('dialog').getByRole('button', { name: 'Создать ссылку' }).click();
  await visible(user.getByLabel('Созданная публичная ссылка'));
  const folderURL = await user.getByLabel('Созданная публичная ссылка').inputValue();
  await user.getByRole('dialog').getByRole('button', { name: 'Закрыть', exact: true }).click();
  const guest = await anonymous.newPage();
  observe(guest);
  assert.equal((await guest.goto(folderURL)).status(), 401);
  assert.equal((await anonymous.request.get(folderURL + '/archive')).status(), 401);
  await guest.getByLabel('Пароль', { exact: true }).fill(password);
  const unlockResponse = guest.waitForResponse(
    (response) => response.url() === folderURL + '/unlock',
  );
  await guest.getByRole('button', { name: 'Открыть', exact: true }).click();
  const unlocked = await unlockResponse;
  assert.equal(
    unlocked.status(),
    303,
    `Unlock failed with Origin=${unlocked.request().headers().origin}`,
  );
  await visible(guest.getByRole('heading', { name: 'В этой папке пока нет файлов' }));
  await downloadArchive(guest, 'Для друзей.zip', { 'Для друзей/': '' });
  const session = await (await userContext.request.get(publicURL + '/api/v1/me')).json();
  const mutationHeaders = { 'X-CSRF-Token': session.csrf_token };
  const folderResponse = await userContext.request.post(publicURL + '/api/v1/folders', {
    data: { parent_id: sharedFolderID, name: 'Фото и текст' },
    headers: mutationHeaders,
  });
  assert.equal(folderResponse.status(), 201);
  const nestedFolder = await folderResponse.json();
  const emptyFolderResponse = await userContext.request.post(publicURL + '/api/v1/folders', {
    data: { parent_id: nestedFolder.id, name: 'Пустая' },
    headers: mutationHeaders,
  });
  assert.equal(emptyFolderResponse.status(), 201);
  async function uploadFixture(name, data) {
    const created = await userContext.request.post(publicURL + '/api/v1/uploads', {
      headers: {
        ...mutationHeaders,
        'Tus-Resumable': '1.0.0',
        'Upload-Length': String(data.length),
        'Upload-Metadata': `filename ${Buffer.from(name).toString('base64')},folder_id ${Buffer.from(nestedFolder.id).toString('base64')}`,
      },
    });
    assert.equal(created.status(), 201);
    const accepted = await userContext.request.patch(created.headers().location, {
      data,
      headers: {
        ...mutationHeaders,
        'Tus-Resumable': '1.0.0',
        'Content-Type': 'application/offset+octet-stream',
        'Upload-Offset': '0',
      },
    });
    assert.equal(accepted.status(), 204);
    return created.headers()['sharefile-file-id'];
  }
  const note =
    '# Заметки к поездке\n\nПривет, друзья!\n\n<script>window.__previewExecuted = true</script>\n';
  const noteID = await uploadFixture('заметки.html', Buffer.from(note));
  const imageData = Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLbtAAAAABJRU5ErkJggg==',
    'base64',
  );
  const photoID = await uploadFixture('фото.png', imageData);
  const binaryData = Buffer.from([0, 1, 2, 3, 4]);
  await uploadFixture('архив.bin', binaryData);
  // Search from personal home must also find nested files in other granted folders.
  await user.goto(publicURL + `/#/files/${session.user.home_id}`);
  const fileSearch = user.getByRole('form', { name: 'Поиск файлов' });
  await fileSearch.getByLabel('Поиск по всем доступным папкам').fill('ФОТО');
  await fileSearch.locator('summary').click();
  await fileSearch.getByLabel('Тип', { exact: true }).selectOption('image');
  const added = new Date().toLocaleDateString('en-CA');
  await fileSearch.getByLabel('Добавлено с', { exact: true }).fill(added);
  await fileSearch.getByLabel('Добавлено по', { exact: true }).fill(added);
  await fileSearch.getByLabel('Единица размера').selectOption('1');
  await fileSearch.getByLabel('Размер от', { exact: true }).fill(String(imageData.length));
  await fileSearch.getByLabel('Размер до', { exact: true }).fill(String(imageData.length));
  await fileSearch.getByRole('button', { name: 'Найти', exact: true }).click();
  await visible(user.getByRole('link', { name: 'фото.png', exact: true }));
  assert.equal(await user.locator('.file-table tbody tr').count(), 1);
  const location = user.getByRole('button', { name: 'Открыть папку для фото.png', exact: true });
  assert.equal(await location.textContent(), 'Проекты / Для друзей / Фото и текст');
  await user.screenshot({ path: join(screenshots, 'search-desktop.png'), fullPage: true });
  await fileSearch.getByLabel('Размер до', { exact: true }).fill('0');
  await fileSearch.getByRole('button', { name: 'Найти', exact: true }).click();
  await visible(fileSearch.getByRole('alert'));
  await fileSearch.getByLabel('Размер от', { exact: true }).fill('0');
  await fileSearch.getByRole('button', { name: 'Найти', exact: true }).click();
  await visible(user.getByRole('heading', { name: 'Ничего не найдено' }));
  await fileSearch.getByLabel('Размер до', { exact: true }).fill(String(imageData.length));
  await fileSearch.getByRole('button', { name: 'Найти', exact: true }).click();
  await visible(location);
  await user.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await user.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    true,
  );
  await user.screenshot({ path: join(screenshots, 'search-mobile.png'), fullPage: true });
  await location.click();
  await visible(user.getByRole('heading', { name: 'Фото и текст', exact: true }));
  assert.equal(await fileSearch.getByRole('button', { name: 'Сбросить поиск' }).count(), 0);
  await user.setViewportSize({ width: 1440, height: 960 });
  const pagesResponse = await userContext.request.post(publicURL + '/api/v1/folders', {
    headers: mutationHeaders,
    data: { parent_id: session.user.home_id, name: 'Поиск страниц' },
  });
  assert.equal(pagesResponse.status(), 201);
  const searchPages = await pagesResponse.json();
  for (let i = 0; i < 105; i++) {
    const created = await userContext.request.post(publicURL + '/api/v1/folders', {
      headers: mutationHeaders,
      data: {
        parent_id: searchPages.id,
        name: `Глобальный результат ${String(i).padStart(3, '0')}`,
      },
    });
    assert.equal(created.status(), 201);
  }
  await fileSearch.getByLabel('Поиск по всем доступным папкам').fill('ГЛОБАЛЬНЫЙ РЕЗУЛЬТАТ');
  await fileSearch.getByRole('button', { name: 'Найти', exact: true }).click();
  await visible(user.getByRole('button', { name: 'Глобальный результат 000', exact: true }));
  assert.equal(await user.locator('.file-table tbody tr').count(), 100);
  await user
    .locator('.file-card .pager')
    .getByRole('button', { name: 'Далее', exact: true })
    .click();
  await visible(user.getByRole('button', { name: 'Глобальный результат 104', exact: true }));
  assert.equal(await user.locator('.file-table tbody tr').count(), 5);
  assert.equal(
    await fileSearch.getByLabel('Поиск по всем доступным папкам').inputValue(),
    'ГЛОБАЛЬНЫЙ РЕЗУЛЬТАТ',
  );
  assert.equal(
    await user
      .locator('.file-card .pager')
      .getByRole('button', { name: 'Далее', exact: true })
      .isDisabled(),
    true,
  );
  await fileSearch.getByRole('button', { name: 'Сбросить поиск' }).click();
  await visible(user.getByRole('link', { name: 'фото.png', exact: true }));
  await user.goto(publicURL + `/#/files/${sharedFolderID}`);
  const nestedArchive = {
    'Фото и текст/': '',
    'Фото и текст/Пустая/': '',
    'Фото и текст/заметки.html': note,
    'Фото и текст/фото.png': imageData,
    'Фото и текст/архив.bin': binaryData,
  };
  const rootArchive = {
    'Для друзей/': '',
    ...Object.fromEntries(
      Object.entries(nestedArchive).map(([name, data]) => ['Для друзей/' + name, data]),
    ),
  };
  await guest.reload();
  await downloadArchive(guest, 'Для друзей.zip', rootArchive);
  await downloadArchive(user, 'Для друзей.zip', rootArchive);
  await guest.getByRole('link', { name: 'Фото и текст', exact: false }).click();
  await visible(guest.getByRole('heading', { name: 'Фото и текст', exact: true }));
  await downloadArchive(guest, 'Фото и текст.zip', nestedArchive);
  assert.equal(
    await guest
      .getByRole('navigation', { name: 'Путь в общей папке' })
      .getByText('alice', { exact: true })
      .count(),
    0,
  );
  await guest.screenshot({ path: join(screenshots, 'public-folder-desktop.png'), fullPage: true });
  await guest.getByRole('link', { name: 'заметки.html Открыть предпросмотр', exact: true }).click();
  assert.equal(await guest.locator('.text-preview').textContent(), note);
  assert.equal(await guest.evaluate(() => window.__previewExecuted), undefined);
  await guest.screenshot({ path: join(screenshots, 'public-text-preview.png'), fullPage: true });
  const noteDownload = await anonymous.request.get(folderURL + `/nodes/${noteID}`);
  assert.equal(await noteDownload.text(), note);
  assert.match(noteDownload.headers()['content-disposition'], /^attachment/);
  await guest.getByRole('link', { name: 'Вернуться в папку', exact: false }).click();
  await guest.getByRole('link', { name: 'фото.png Открыть предпросмотр', exact: true }).click();
  await guest.waitForFunction(() => {
    const img = document.querySelector('.image-preview img');
    return img?.complete && img.naturalWidth > 0;
  });
  const imagePreviewURL = folderURL + `/nodes/${photoID}/preview`;
  const imagePreview = await anonymous.request.get(imagePreviewURL);
  assert.equal(imagePreview.headers()['content-type'], 'image/png');
  assert.deepEqual(await imagePreview.body(), imageData);
  await guest.screenshot({ path: join(screenshots, 'public-photo-preview.png'), fullPage: true });
  await guest.getByRole('link', { name: 'Вернуться в папку', exact: false }).click();
  await guest.getByRole('link', { name: 'архив.bin Открыть предпросмотр', exact: true }).click();
  await visible(guest.getByRole('heading', { name: 'Предпросмотр недоступен' }));
  await guest.getByRole('link', { name: 'Вернуться в папку', exact: false }).click();
  await guest.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await guest.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    true,
  );
  await guest.screenshot({ path: join(screenshots, 'public-folder-mobile.png'), fullPage: true });
  await guest.getByRole('link', { name: 'заметки.html Открыть предпросмотр', exact: true }).click();
  assert.equal(
    await guest.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    true,
  );
  await guest.screenshot({ path: join(screenshots, 'public-text-mobile.png'), fullPage: true });
  const shareRecords = await (
    await userContext.request.get(publicURL + `/api/v1/shares?node_id=${sharedFolderID}`)
  ).json();
  const revoked = await userContext.request.delete(
    publicURL + `/api/v1/shares/${shareRecords[0].id}`,
    { headers: mutationHeaders },
  );
  assert.equal(revoked.status(), 204);
  assert.equal((await anonymous.request.get(folderURL + '/archive')).status(), 404);
  assert.equal(
    (await anonymous.request.get(folderURL + `/nodes/${nestedFolder.id}/archive`)).status(),
    404,
  );
  assert.equal((await anonymous.request.get(imagePreviewURL)).status(), 404);
  assert.equal((await guest.reload()).status(), 404);
  // A genuine browser directory drop (CDP file drag), including empty directories.
  await user.goto(publicURL + `/#/files/${projects}`);
  await visible(user.getByRole('button', { name: 'Загрузить папку', exact: true }));
  const directorySource = join(fixture, 'Папка 日本語');
  await mkdir(join(directorySource, 'a'), { recursive: true });
  await mkdir(join(directorySource, 'b'), { recursive: true });
  await mkdir(join(directorySource, 'empty/nested'), { recursive: true });
  await writeFile(join(directorySource, 'a/report.txt'), 'first folder');
  await writeFile(join(directorySource, 'b/report.txt'), 'second folder');
  await writeFile(join(directorySource, 'zero.txt'), '');
  const drag = await userContext.newCDPSession(user);
  const dropBox = await user.locator('.file-card').boundingBox();
  for (const type of ['dragEnter', 'dragOver', 'drop']) {
    await drag.send('Input.dispatchDragEvent', {
      type,
      x: dropBox.x + 30,
      y: dropBox.y + 70,
      data: { items: [], files: [directorySource], dragOperationsMask: 1 },
    });
  }
  const wholeFolderLink = user.getByLabel('Ссылка на папку Папка 日本語', { exact: true });
  await visible(wholeFolderLink);
  const wholeURL = await wholeFolderLink.inputValue();
  const projectsChildren = await (
    await userContext.request.get(publicURL + `/api/v1/folders?parent_id=${projects}`)
  ).json();
  const uploadedTree = projectsChildren.find((node) => node.name === 'Папка 日本語');
  assert(uploadedTree);
  const treeNodes = await (
    await userContext.request.get(publicURL + `/api/v1/folders?parent_id=${uploadedTree.id}`)
  ).json();
  assert.deepEqual(treeNodes.map((node) => node.name).sort(), ['a', 'b', 'empty', 'zero.txt']);
  for (const [folder, contents] of [
    ['a', 'first folder'],
    ['b', 'second folder'],
  ]) {
    const nested = treeNodes.find((node) => node.name === folder);
    const leaves = await (
      await userContext.request.get(publicURL + `/api/v1/folders?parent_id=${nested.id}`)
    ).json();
    assert.equal(leaves[0].name, 'report.txt');
    assert.equal(
      await (await anonymous.request.get(wholeURL + `/nodes/${leaves[0].id}`)).text(),
      contents,
    );
  }
  const emptyTree = treeNodes.find((node) => node.name === 'empty');
  const emptyChildren = await (
    await userContext.request.get(publicURL + `/api/v1/folders?parent_id=${emptyTree.id}`)
  ).json();
  assert.equal(emptyChildren[0].name, 'nested');
  const treeShares = await (
    await userContext.request.get(publicURL + `/api/v1/shares?node_id=${uploadedTree.id}`)
  ).json();
  assert.equal(treeShares.length, 1);
  await guest.goto(wholeURL);
  await guest.getByRole('link', { name: 'a Папка', exact: true }).click();
  await visible(guest.getByRole('link', { name: 'report.txt Открыть предпросмотр', exact: true }));
  await user.screenshot({
    path: join(screenshots, 'directory-upload-desktop.png'),
    fullPage: true,
  });
  // Directory picker preserves relative paths too (FileList does not expose empty folders).
  const pickerSource = join(fixture, 'Через выбор папки');
  await mkdir(join(pickerSource, 'nested'), { recursive: true });
  await writeFile(join(pickerSource, 'nested/file.txt'), 'picker upload');
  await user.getByLabel('Выбрать папку для загрузки', { exact: true }).setInputFiles(pickerSource);
  await visible(user.getByLabel('Ссылка на папку Через выбор папки', { exact: true }));
  await user.getByRole('button', { name: 'Убрать Папка 日本語 из списка', exact: true }).click();
  await user
    .getByRole('button', { name: 'Убрать Через выбор папки из списка', exact: true })
    .click();
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
    'Browser acceptance passed: auth, private admin, users/groups/ACL, reader/manager UI, chunked upload and SHA-256, sharing, rename/move/replace/revoke/trash/restore, lost-response resume, device pairing/revocation, settings/import, password-protected live folder, previews, global Unicode search with combined filters, safe folder paths, search pagination beyond 100 results, streaming ZIP downloads with nested and empty folders, real directory drag/drop with empty folders and duplicate basenames, one folder link, directory picker, mobile layout, logout.',
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
