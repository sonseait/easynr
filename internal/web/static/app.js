'use strict';
const app = document.querySelector('#app');
const nav = document.querySelector('#navigation');
const status = document.querySelector('#status');
let csrf = '', page = 'Packages', generation = 0;
function el(tag, text, attrs = {}) {
 const n = document.createElement(tag); if (text !== undefined && text !== null) n.textContent = text;
 for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
 return n;
}
function message(text, error = false) { status.textContent = text; status.className = error ? 'error' : 'success'; }
function button(text, action, kind = '') {
 const b = el('button', text, {type: 'button', class: kind});
 b.addEventListener('click', () => perform(action, b)); return b;
}
async function perform(action, b) {
 if (b) b.disabled = true;
 try { await action(); } catch (e) { message(e.message, true); }
 finally { if (b) b.disabled = false; }
}
async function api(path, method = 'GET', body) {
 const r = await fetch('/api/' + path, {method, headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf}, body: body === undefined ? undefined : JSON.stringify(body)});
 if (r.status === 401 && path !== 'login') { csrf = ''; login(); }
 if (r.status === 204) return null;
 const data = await r.json();
 if (!r.ok) throw new Error(data.error?.message || 'Request failed (' + r.status + ')');
 return data;
}
function field(form, title, name, options = {}) {
 const label = el('label', title); const input = el(options.multiline ? 'textarea' : 'input', null, {name, ...(options.type ? {type: options.type} : {}), ...(options.required === false ? {} : {required: ''})});
 if (options.placeholder) input.placeholder = options.placeholder;
 if (options.value) input.value = options.value;
 if (options.type === 'password') input.autocomplete = 'current-password';
 label.append(input); form.append(label); return input;
}
function submit(form, text, action) {
 const b = el('button', text, {type: 'submit', class: 'primary'}); form.append(b);
 form.addEventListener('submit', e => { e.preventDefault(); perform(action, b); });
}
function heading(title, description) { app.replaceChildren(el('h1', title), el('p', description)); }
function list(items, empty, render, parent = app) {
 const box = el('div', null, {class: 'list'}); parent.append(box);
 if (!items.length) box.append(el('p', empty)); else for (const item of items) { const row = el('div', null, {class: 'row'}); render(row, item); box.append(row); }
}
function bytes(n) { if (!n) return '0 B'; const units = ['B', 'KiB', 'MiB', 'GiB']; const i = Math.min(3, Math.floor(Math.log(n) / Math.log(1024))); return (n / 1024 ** i).toFixed(i ? 1 : 0) + ' ' + units[i]; }
function dialog(title, build) {
 const d = el('dialog'); d.append(el('h2', title)); const content = el('div'); const actions = el('div', null, {class: 'dialog-actions'}); d.append(content); build(content, actions);
 actions.append(button('Close', () => { d.close(); d.replaceChildren(); d.remove(); })); d.append(actions); d.addEventListener('close', () => { d.replaceChildren(); d.remove(); }); document.body.append(d); d.showModal(); return d;
}
function login() {
 generation++; document.querySelectorAll('dialog').forEach(d => { d.close(); d.replaceChildren(); d.remove(); }); nav.replaceChildren(); app.replaceChildren();
 const box = el('section', null, {class: 'login'}); box.append(el('h1', 'Sign in to EasyNR'), el('p', 'Manage packages, access tokens and upstream registries.'));
 const form = el('form'); const password = field(form, 'Admin password', 'password', {type: 'password'});
 submit(form, 'Sign in', async () => { const data = await api('login', 'POST', {password: password.value}); password.value = ''; csrf = data.csrfToken; message(''); await show('Packages'); });
 box.append(form, el('p', 'The initial password is printed in the first startup log. Recover offline with: easynr admin reset-password', {class: 'recovery'})); app.append(box);
}
function navigation() {
 nav.replaceChildren(); for (const label of ['Packages', 'Tokens', 'Upstreams', 'Routes']) { const b = button(label, () => show(label)); b.setAttribute('aria-pressed', String(page === label)); nav.append(b); }
 nav.append(button('npm setup', () => setup()), button('Sign out', async () => { await api('logout', 'POST'); csrf = ''; message(''); login(); }));
}
function setup() {
 dialog('Connect npm', box => {
  box.append(el('p', 'Create a token in Tokens. Add these lines to your project or user .npmrc, then set EASYNR_TOKEN through your secret manager:'));
  const origin = location.origin; box.append(el('pre', `registry=${origin}/\n//${location.host}/:_authToken=\${EASYNR_TOKEN}`));
  box.append(el('pre', 'npm publish\nnpm install package-name\nnpm ci'), el('p', 'All downloads require a token. A write token includes read permission.'));
 });
}
async function show(label = page) {
 page = label; const gen = ++generation; navigation(); app.replaceChildren(el('p', 'Loading…'));
 try {
  if (label === 'Packages') { const [data, cache] = await Promise.all([api('packages'), api('cache')]); if (gen !== generation) return; packages(data.items, cache); }
  else if (label === 'Tokens') { const data = await api('tokens'); if (gen !== generation) return; tokens(data.items); }
  else if (label === 'Upstreams') { const data = await api('upstreams'); if (gen !== generation) return; upstreams(data.items); }
  else if (label === 'Routes') { const [u, r] = await Promise.all([api('upstreams'), api('routes')]); if (gen !== generation) return; routes(u.items, r.items); }
 } catch (e) { if (gen === generation) { app.replaceChildren(el('p', 'Unable to load this page.'), button('Retry', () => show(label))); message(e.message, true); } }
}
function packages(items, cache) {
 heading('Packages', 'Packages published to this registry.');
 const summary = el('section', null, {class: 'cache-summary', 'aria-labelledby': 'cache-heading'});
 summary.append(el('h2', 'Cache statistics', {id: 'cache-heading'}));
 const metrics = el('dl', null, {class: 'cache-metrics'});
 for (const [label, value] of [['Cached packages', String(cache.packages)], ['Cache size', bytes(cache.bytes)]]) {
  const metric = el('div'); metric.append(el('dt', label), el('dd', value)); metrics.append(metric);
 }
 summary.append(metrics, el('p', 'Across all upstreams. Size includes downloaded tarballs; local packages are excluded.', {class: 'muted'}));
 summary.append(button('Clear cache', async () => { if (!confirm('Clear all remote metadata and tarball references? Local packages are preserved. Run offline GC later to reclaim disk space.')) return; await api('cache', 'DELETE'); message('Cache cleared.'); await show('Packages'); }, 'danger'));
 app.append(summary);
 const form = el('form'); const name = field(form, 'Inspect or fetch a package', 'name', {placeholder: '@team/package or lodash'}); submit(form, 'Inspect', () => details(name.value.trim())); app.append(form);
 list(items, 'No published packages yet. Publish your first package with npm publish.', (row, p) => {
  const text = el('div'); text.append(button(p.name, () => details(p.name)), el('p', `${p.localVersions} published versions · ${bytes(p.localBytes)} stored`)); row.append(text);
 });
}
async function details(name) {
 const data = await api('package?name=' + encodeURIComponent(name));
 dialog(name, box => {
  box.append(el('pre', `npm install ${name} --registry=${location.origin}/`));
  const tags = el('p', 'Dist-tags: ' + Object.entries(data['dist-tags'] || {}).map(([t, v]) => t + '=' + v).join(', ')); box.append(tags);
  const table = el('table'); const head = el('tr'); for (const label of ['Version', 'Source', 'Stored', 'Actions']) head.append(el('th', label)); const thead = el('thead'); thead.append(head); table.append(thead); const body = el('tbody'); table.append(body);
  for (const [version, meta] of Object.entries(data.versions || {}).reverse()) {
   const source = data._easynr?.[version] || {}; const tr = el('tr'); tr.append(el('td', version), el('td', source.source || 'remote'), el('td', source.cached ? bytes(source.bytes) : 'Not downloaded'));
   const actions = el('td'); const buttons = el('div', null, {class: 'button-group'}); actions.append(buttons);
   buttons.append(button('Metadata', () => dialog(version, target => target.append(el('pre', JSON.stringify(meta, null, 2))))));
   if (source.source === 'local') buttons.append(button('Delete', async () => { if (!confirm(`Delete local ${name}@${version}? An upstream version with the same number may become available. Disk space is reclaimed by offline GC.`)) return; await api('package?name=' + encodeURIComponent(name) + '&version=' + encodeURIComponent(version), 'DELETE'); document.querySelectorAll('dialog').forEach(d => d.close()); message('Local version deleted.'); await show('Packages'); }, 'danger'));
   tr.append(actions); body.append(tr);
  }
  const scroll = el('div', null, {class: 'table-scroll'}); scroll.append(table); box.append(scroll);
  const form = el('form'); const tag = field(form, 'Local dist-tag', 'tag', {placeholder: 'latest'}); const version = field(form, 'Local version', 'version', {placeholder: '1.0.0'});
  submit(form, 'Set tag', async () => { await api('tag', 'PUT', {name, tag: tag.value, version: version.value}); tags.textContent = 'Tag saved. Reopen to refresh metadata.'; message('Tag saved.'); }); box.append(form);
  const tagActions = el('div', null, {class: 'button-group package-actions'});
  for (const tagName of Object.keys(data['dist-tags'] || {})) tagActions.append(button('Remove local tag ' + tagName, async () => { if (!confirm(`Remove local tag ${tagName}? A remote tag with the same name may reappear.`)) return; await api('tag?name=' + encodeURIComponent(name) + '&tag=' + encodeURIComponent(tagName), 'DELETE'); message('Local tag removed.'); }));
  if (tagActions.childElementCount) box.append(tagActions);
  const packageActions = el('div', null, {class: 'button-group package-actions'});
  packageActions.append(button('Clear remote cache', async () => { if (!confirm('Clear remote cache for ' + name + '?')) return; await api('cache?name=' + encodeURIComponent(name), 'DELETE'); document.querySelectorAll('dialog').forEach(d => d.close()); await show('Packages'); }));
  packageActions.append(button('Delete local package', async () => { if (prompt('Delete all local versions? Remote versions may reappear. Type the exact package name:') !== name) return; await api('package?name=' + encodeURIComponent(name), 'DELETE'); document.querySelectorAll('dialog').forEach(d => d.close()); await show('Packages'); }, 'danger'));
  box.append(packageActions);
 });
}
function tokens(items) {
 heading('Tokens', 'Secrets are shown once. Grant read or write access to exact packages, @scope/* or *.');
 const form = el('form'); const name = field(form, 'Token name', 'name'); const grants = field(form, 'Grants: one package and permission per line', 'grants', {multiline: true, placeholder: '@team/* write\n* read'});
 submit(form, 'Create token', async () => {
  const parsed = grants.value.trim().split('\n').map(line => { const [pkg, permission, extra] = line.trim().split(/\s+/); if (!pkg || !['read', 'write'].includes(permission) || extra) throw new Error('Each grant must be: package read|write'); return {package: pkg, permission}; });
  const data = await api('tokens', 'POST', {name: name.value, grants: parsed});
  dialog('Save your token', (box, actions) => {
   box.append(el('p', 'This secret cannot be retrieved after closing this dialog.'));
   const label = el('label', 'Token secret'); const secret = el('textarea', null, {readonly: '', rows: '3', spellcheck: 'false', class: 'token-secret'}); secret.value = data.secret;
   label.append(secret); box.append(label);
   const feedback = el('div', '', {class: 'dialog-status', role: 'status'}); box.append(feedback);
   actions.append(button('Copy token', async () => {
    try {
     if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
     await navigator.clipboard.writeText(data.secret); feedback.className = 'dialog-status success'; feedback.textContent = 'Token copied.';
    } catch {
     secret.focus(); secret.select(); feedback.className = 'dialog-status'; feedback.textContent = 'Copy unavailable here. The token is selected; copy it manually.';
    }
   }));
  });
  await show('Tokens');
 }); app.append(form);
 list(items, 'No access tokens.', (row, t) => { const text = el('div'); text.append(el('strong', t.name), el('p', t.grants.map(g => g.package + ' ' + g.permission).join(', '))); row.append(text, button('Revoke', async () => { if (!confirm('Revoke token ' + t.name + '?')) return; await api('tokens/' + t.id, 'DELETE'); message('Token revoked.'); await show(); }, 'danger')); });
}
function upstreams(items) {
 heading('Upstreams', 'Manage remote registries and their access credentials. Assign registries to package scopes in Routes.');
 list(items, 'No upstreams.', (row, u) => {
  const text = el('div'); const result = el('p', '', {'aria-live': 'polite'});
  text.append(el('strong', u.name + ' (' + u.id + ')'), el('p', u.url + (u.hasToken ? ' · Token configured' : '')), result);
  const actions = el('div', null, {class: 'actions'});
  actions.append(button('Test', async () => {
   result.className = ''; result.textContent = 'Testing connection…';
   try {
    const data = await api('upstreams/' + encodeURIComponent(u.id) + '/test', 'POST');
    result.className = data.ok ? 'success' : 'error'; result.textContent = data.message + ' (' + data.durationMs + ' ms)';
   } catch (e) { result.className = 'error'; result.textContent = e.message; }
  }), button('Edit', () => upstreamForm(u)));
  if (u.id !== 'npmjs') actions.append(button('Delete', async () => { if (!confirm('Delete this upstream? Remove its routes first. Cached tarballs remain until cache clear/GC.')) return; await api('upstreams/' + encodeURIComponent(u.id), 'DELETE'); await show(); }, 'danger'));
  row.append(text, actions);
 });
 app.append(button('Add upstream', () => upstreamForm()));
}
function routes(upstreams, items) {
 heading('Routes', 'Choose a remote registry for each scope, or use * to set the default. Every package checks local and cache first.');
 list(items, 'No scope routes. All packages use npmjs by default.', (row, r) => { row.append(el('code', r.scope + ' → ' + r.upstream), button('Remove', async () => { if (!confirm('Remove route ' + r.scope + '?')) return; await api('routes?scope=' + encodeURIComponent(r.scope), 'DELETE'); await show(); })); });
 const form = el('form'); const scope = field(form, 'Scope (@team) or * for default', 'scope', {placeholder: '@team'});
 const label = el('label', 'Upstream'); const select = el('select', null, {name: 'upstream', 'aria-label': 'Upstream'}); for (const u of upstreams) select.append(el('option', u.name, {value: u.id})); label.append(select); form.append(label);
 submit(form, 'Save route', async () => { await api('routes', 'PUT', {scope: scope.value, upstream: select.value}); message('Route saved.'); await show(); }); app.append(form);
}
function upstreamForm(existing) {
 let d; d = dialog(existing ? 'Edit upstream' : 'Add upstream', box => {
  const form = el('form'); const id = field(form, 'ID', 'id', {value: existing?.id}); if (existing) id.readOnly = true;
  const name = field(form, 'Display name', 'name', {value: existing?.name}); const url = field(form, 'Registry URL', 'url', {type: 'url', value: existing?.url}); if (existing) url.readOnly = true;
  const token = field(form, 'Bearer token (empty keeps existing, - clears)', 'token', {type: 'password', required: false}); token.autocomplete = 'new-password';
  submit(form, 'Save upstream', async () => { await api('upstreams', 'PUT', {id: id.value, name: name.value, url: url.value, token: token.value}); token.value = ''; d.close(); message('Upstream saved.'); await show('Upstreams'); }); box.append(form);
 });
}
perform(async () => { try { const data = await api('session'); csrf = data.csrfToken; await show(); } catch { login(); } });
