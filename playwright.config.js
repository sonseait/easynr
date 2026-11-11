const {defineConfig, devices} = require('@playwright/test');
module.exports = defineConfig({
 testDir: './tests/ui', timeout: 30000, fullyParallel: false, workers: 1,
 use: {baseURL: 'http://127.0.0.1:14873', trace: 'retain-on-failure'},
 projects: [
  {name: 'desktop', use: {...devices['Desktop Chrome']}},
  {name: 'mobile', use: {...devices['Pixel 7'], defaultBrowserType: 'chromium'}}
 ],
 webServer: {command: 'go run ./tests/ui/fixture', url: 'http://127.0.0.1:14873/healthz', reuseExistingServer: false, timeout: 60000}
});
