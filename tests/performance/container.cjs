// Isolated npm install load test. Requires Go, Node.js, npm, tar and Docker (cgroup v2).
// Usage: node tests/performance/container.cjs easynr:before easynr:after
// Images must already be built; only test-owned containers/volumes are removed.
const http = require('node:http');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const {execFile, spawn} = require('node:child_process');
const {promisify} = require('node:util');
const run = promisify(execFile);
const installType = 'application/vnd.npm.install-v1+json';
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const packageCount = Number(process.env.PERF_PACKAGES || 24);
const versionCount = Number(process.env.PERF_VERSIONS || 750);

async function main() {
  const images = process.argv.slice(2);
  if (!images.length) throw new Error('Provide one or more locally built EasyNR image tags');
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'easynr-load-'));
  const fixtures = new Map();
  let upstreamFull = 0, upstreamInstall = 0;
  let upstream;
  try {
    for (let i = 0; i < packageCount; i++) {
      const name = `load-fixture-${i}`;
      const dir = path.join(temp, name);
      await fs.mkdir(path.join(dir, 'package'), {recursive: true});
      await fs.writeFile(path.join(dir, 'package/package.json'), JSON.stringify({name, version: `1.0.${versionCount - 1}`, main: 'index.js'}));
      await fs.writeFile(path.join(dir, 'package/index.js'), 'module.exports = 42;\n');
      await run('tar', ['-czf', path.join(dir, 'fixture.tgz'), '-C', dir, 'package']);
      const tarball = await fs.readFile(path.join(dir, 'fixture.tgz'));
      const dist = {tarball: `/tar/${name}`, integrity: 'sha512-' + crypto.createHash('sha512').update(tarball).digest('base64')};
      const full = {}, install = {};
      for (let j = 0; j < versionCount; j++) {
        const version = `1.0.${j}`;
        install[version] = {name, version, dist};
        full[version] = {...install[version], readme: 'fixture documentation '.repeat(100)};
      }
      const document = versions => JSON.stringify({name, 'dist-tags': {latest: `1.0.${versionCount - 1}`}, versions});
      fixtures.set(name, {tarball, full: document(full), install: document(install)});
    }
    upstream = http.createServer((req, res) => {
      const name = decodeURIComponent(req.url.replace(/^\/(tar\/)?/, ''));
      const fixture = fixtures.get(name);
      if (!fixture) { res.writeHead(404).end(); return; }
      if (req.url.startsWith('/tar/')) { res.end(fixture.tarball); return; }
      const install = req.headers.accept?.includes(installType);
      if (install) upstreamInstall++; else upstreamFull++;
      const etag = install ? '"install"' : '"full"';
      if (req.headers['if-none-match'] === etag) { res.writeHead(304).end(); return; }
      res.setHeader('ETag', etag);
      res.setHeader('Content-Type', install ? installType : 'application/json');
      res.end(install ? fixture.install : fixture.full);
    });
    await new Promise(resolve => upstream.listen(0, '0.0.0.0', resolve));
    const remote = `http://host.docker.internal:${upstream.address().port}`;
    for (const image of images) {
      upstreamFull = 0; upstreamInstall = 0;
      await measure(image, temp, remote, [...fixtures.keys()]);
      console.log(JSON.stringify({image, upstreamFull, upstreamInstall}));
    }
  } finally {
    if (upstream) await new Promise(resolve => upstream.close(resolve));
    await fs.rm(temp, {recursive: true, force: true});
  }
}

async function measure(image, temp, remote, names) {
  const name = 'easynr-load-' + crypto.randomBytes(6).toString('hex');
  const volume = name + '-data';
  const docker = async (...args) => (await run('docker', args, {maxBuffer: 4 << 20})).stdout.trim();
  let stats;
  try {
    await docker('volume', 'create', volume);
    await docker('run', '-d', '--name', name, '--cpus=2', '--memory=1g', '--add-host', 'host.docker.internal:host-gateway',
      '-p', '127.0.0.1::4873', '-v', volume + ':/data', image);
    const base = 'http://' + await docker('port', name, '4873/tcp');
    let ready = false;
    for (let i = 0; i < 100; i++) {
      if (await fetch(base + '/healthz').then(r => r.ok).catch(() => false)) { ready = true; break; }
      await pause(100);
    }
    if (!ready) throw new Error('Container did not become ready');
    const logs = await run('docker', ['logs', name]);
    const password = (logs.stdout + logs.stderr).match(/bootstrap_admin_password password=(\S+)/)?.[1];
    if (!password) throw new Error('Missing bootstrap password');
    let cookie, csrf;
    const api = async (method, endpoint, body) => {
      const response = await fetch(base + endpoint, {method, headers: {Origin: base, Cookie: cookie || '',
        'X-CSRF-Token': csrf || '', 'Content-Type': 'application/json'}, body: body && JSON.stringify(body)});
      if (!response.ok) throw new Error(`${endpoint}: ${response.status}`);
      if (response.headers.has('set-cookie')) cookie = response.headers.get('set-cookie').split(';')[0];
      return response.status === 204 ? null : response.json();
    };
    csrf = (await api('POST', '/api/login', {password})).csrfToken;
    const token = (await api('POST', '/api/tokens', {name: 'load-test', grants: [{package: '*', permission: 'read'}]})).secret;
    await api('PUT', '/api/upstreams', {id: 'fixture', name: 'fixture', url: remote});
    await api('PUT', '/api/routes', {scope: '*', upstream: 'fixture'});
    const architecture = await docker('image', 'inspect', '--format', '{{.Architecture}}', image);
    const probe = path.join(temp, 'probe-' + architecture);
    await run('go', ['build', '-trimpath', '-o', probe, './tests/performance/probe'],
      {cwd: path.join(__dirname, '../..'), env: {...process.env, CGO_ENABLED: '0', GOOS: 'linux', GOARCH: architecture}});
    await docker('cp', probe, name + ':/easynr-probe');
    const countersBefore = JSON.parse(await docker('exec', name, '/easynr-probe'));
    const samples = [];
    let pending = '';
    stats = spawn('docker', ['stats', '--format', '{{json .}}', name], {stdio: ['ignore', 'pipe', 'ignore']});
    stats.stdout.on('data', chunk => {
      pending += chunk.toString().replace(/\x1b\[[0-9;]*[A-Za-z]/g, '');
      let end;
      while ((end = pending.indexOf('\n')) >= 0) {
        const line = pending.slice(0, end); pending = pending.slice(end + 1);
        try { samples.push(JSON.parse(line)); } catch {}
      }
    });
    const work = path.join(temp, name);
    await fs.mkdir(work);
    await fs.writeFile(path.join(work, '.npmrc'), `registry=${base}/\n//${base.slice(7)}/:_authToken=${token}\n`);
    await fs.writeFile(path.join(work, 'package.json'), JSON.stringify({name: 'load-consumer', version: '1.0.0', private: true}));
    const npm = (...args) => run('npm', [...args, '--ignore-scripts', '--no-audit', '--no-fund', '--fetch-retries=0',
      '--cache', path.join(work, 'cache-' + crypto.randomBytes(4).toString('hex'))],
      {cwd: work, timeout: 180000, maxBuffer: 1 << 20,
        env: {...process.env, NPM_CONFIG_USERCONFIG: path.join(work, '.npmrc'), NPM_CONFIG_GLOBALCONFIG: path.join(work, 'empty-globalrc'), NPM_CONFIG_UPDATE_NOTIFIER: 'false'}});
    const start = performance.now();
    await npm('install', ...names);
    const coldSeconds = (performance.now() - start) / 1000;
    await fs.rm(path.join(work, 'node_modules'), {recursive: true, force: true});
    await fs.rm(path.join(work, 'package-lock.json'));
    const warmStart = performance.now();
    await npm('install'); // New npm cache forces EasyNR to serve cached metadata and archives.
    const warmSeconds = (performance.now() - warmStart) / 1000;
    const countersAfter = JSON.parse(await docker('exec', name, '/easynr-probe'));
    await pause(1500); // Allow docker stats to deliver its final sample.
    const bytes = value => {
      const [, number, unit] = value.match(/([\d.]+)(\w+)/);
      return Number(number) * ({B: 1, KiB: 1024, MiB: 1024 ** 2, GiB: 1024 ** 3}[unit] || 1);
    };
    console.log(JSON.stringify({image, packages: names.length, versionsPerPackage: versionCount,
      coldSeconds: +coldSeconds.toFixed(2), warmSeconds: +warmSeconds.toFixed(2), samples: samples.length,
      sampledPeakMemoryMiB: +(Math.max(...samples.map(s => bytes(s.MemUsage.split(' / ')[0]))) / 1024 ** 2).toFixed(1),
      processPeakRSSMiB: +countersAfter.processPeakRSSMiB.toFixed(1),
      cgroupPeakMemoryMiB: +countersAfter.cgroupPeakMemoryMiB.toFixed(1),
      processAnonMiB: +countersAfter['RssAnon:'].toFixed(1), processFileMiB: +countersAfter['RssFile:'].toFixed(1),
      databaseMiB: +countersAfter.databaseMiB.toFixed(1),
      cpuSeconds: +(countersAfter.cpuSeconds - countersBefore.cpuSeconds).toFixed(3),
      peakCPUPercent: Math.max(...samples.map(s => parseFloat(s.CPUPerc))),
      oomKilled: await docker('inspect', '--format', '{{.State.OOMKilled}}', name)}));
  } finally {
    stats?.kill();
    await docker('rm', '-f', name).catch(() => {});
    await docker('volume', 'rm', volume).catch(() => {});
  }
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });
