// Run npm run screenshots from the repository root after npm ci.
// Uses the real console assets with explicit demo API fixtures, never ./data.
'use strict';
const {chromium, expect} = require('@playwright/test');
const {readFileSync, mkdirSync} = require('node:fs');
const {resolve} = require('node:path');
const {createHash} = require('node:crypto');
const root = resolve(__dirname, '../..');
const KiB = 1024, MiB = KiB * 1024;
const packages = [
 {name: '@acme/ui', localVersions: 2, cachedVersions: 2, localBytes: 2.2 * MiB, cachedBytes: 512 * KiB},
 {name: '@acme/utils', localVersions: 3, cachedVersions: 0, localBytes: 180 * KiB, cachedBytes: 0},
 {name: 'lodash', localVersions: 0, cachedVersions: 114, localBytes: 0, cachedBytes: 320 * KiB},
 {name: 'typescript', localVersions: 0, cachedVersions: 152, localBytes: 0, cachedBytes: 14 * MiB},
];
const upstreams = [
 {id: 'npmjs', name: 'npmjs', url: 'https://registry.npmjs.org', hasToken: false},
 {id: 'acme', name: 'Acme packages', url: 'https://npm.acme.example.com', hasToken: true},
 {id: 'github', name: 'GitHub Packages', url: 'https://npm.pkg.github.com', hasToken: true},
];
const sources = {}, versions = {};
for (const [version, source] of [['2.1.0', 'acme'], ['2.2.0', 'acme'], ['2.3.0', 'local'], ['2.4.0', 'local']]) {
 const checksum = createHash('sha512').update(`demo-${version}`).digest('base64');
 versions[version] = {name: '@acme/ui', version, description: 'Shared components for Acme applications', license: 'MIT', dist: {tarball: `/-/tarballs/demo-${version}.tgz`, integrity: 'sha512-' + checksum}};
 sources[version] = {source, cached: true, bytes: source === 'local' ? 1.1 * MiB : 256 * KiB};
}
const fixtures = {
 '/api/session': {authenticated: true, csrfToken: 'demo-session'},
 '/api/packages': {items: packages.filter(p => p.localVersions > 0)},
 '/api/cache': {packages: 3, bytes: packages.reduce((sum, p) => sum + p.cachedBytes, 0)},
 '/api/package': {name: '@acme/ui', versions, 'dist-tags': {latest: '2.4.0', stable: '2.3.0'}, _easynr: sources},
 '/api/upstreams': {items: upstreams},
 '/api/routes': {items: [{scope: '*', upstream: 'npmjs'}, {scope: '@acme', upstream: 'acme'}, {scope: '@octo', upstream: 'github'}]},
 '/api/tokens': {items: []},
};
async function main() {
 mkdirSync(__dirname, {recursive: true});
 const browser = await chromium.launch();
 try {
  const page = await browser.newPage({viewport: {width: 1280, height: 820}, deviceScaleFactor: 2, colorScheme: 'light', locale: 'en-GB', timezoneId: 'UTC'});
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  await page.route('http://npm.example.test/**', route => {
   const req = route.request(), path = new URL(req.url()).pathname;
   if (path === '/api/upstreams/npmjs/test' && req.method() === 'POST') return route.fulfill({json: {ok: true, statusCode: 200, durationMs: 84, message: 'Upstream reachable (HTTP 200).'}});
   if (path.startsWith('/api/')) {
    if (req.method() !== 'GET' || !fixtures[path]) throw new Error('Unexpected screenshot API request: ' + req.method() + ' ' + path);
    return route.fulfill({json: fixtures[path]});
   }
   const files = {'/': ['index.html', 'text/html'], '/-/ui/app.js': ['app.js', 'text/javascript'], '/-/ui/style.css': ['style.css', 'text/css']};
   if (!files[path]) return route.fulfill({status: 404});
   const [name, contentType] = files[path];
   return route.fulfill({contentType, body: readFileSync(resolve(root, 'internal/web/static', name))});
  });
  async function screenshot(name) {
   await expect(page.locator('#status')).toBeEmpty();
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
   await page.screenshot({path: resolve(__dirname, name + '.png'), animations: 'disabled'});
  }
  await page.goto('http://npm.example.test/');
  await expect(page.getByRole('region', {name: 'Cache statistics'})).toBeVisible();
  await expect(page.getByRole('button', {name: '@acme/utils', exact: true})).toBeVisible();
  await screenshot('packages');

  await page.getByRole('button', {name: '@acme/ui', exact: true}).click();
  await expect(page.getByRole('dialog').getByRole('cell', {name: '2.4.0', exact: true})).toBeVisible();
  await page.emulateMedia({colorScheme: 'dark'});
  await screenshot('package-details');
  await page.getByRole('dialog').getByRole('button', {name: 'Close', exact: true}).click();

  await page.setViewportSize({width: 1280, height: 580});
  await page.emulateMedia({colorScheme: 'light'});
  await page.getByRole('button', {name: 'Upstreams', exact: true}).click();
  const npmjs = page.locator('.row').filter({has: page.getByText('npmjs (npmjs)', {exact: true})});
  await npmjs.getByRole('button', {name: 'Test', exact: true}).click();
  await expect(npmjs).toContainText('Upstream reachable (HTTP 200).');
  await screenshot('upstreams');

  await page.getByRole('button', {name: 'Routes', exact: true}).click();
  await expect(page.getByText('@acme → acme', {exact: true})).toBeVisible();
  await page.getByLabel('Scope (@team) or * for default').fill('@design');
  await page.getByLabel('Upstream', {exact: true}).selectOption('acme');
  await screenshot('routes');
  expect(errors).toEqual([]);
  console.log('Saved packages.png, package-details.png, upstreams.png and routes.png (demo data).');
 } finally { await browser.close(); }
}
main().catch(error => {console.error(error); process.exitCode = 1;});
