import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

describe('console nginx edge contract', () => {
  const config = readFileSync(
    resolve(process.cwd(), 'nginx/default.conf.template'),
    'utf8',
  );

  it('proxies provider webhooks to the orchestrator before the SPA fallback', () => {
    const webhookLocation = config.match(/location \/webhooks\/ \{([\s\S]*?)\n    \}/)?.[1];

    expect(webhookLocation).toContain('proxy_pass ${ORCHESTRATOR_URL};');
    expect(config.indexOf('location /webhooks/')).toBeLessThan(config.indexOf('location / {'));
  });

  it('serves public artifact routes with fragment-safe browser headers', () => {
    const shareLocation = config.match(/location \^~ \/s\/ \{([\s\S]*?)\n    \}/)?.[1];

    expect(shareLocation).toContain("try_files $uri /index.html;");
    expect(shareLocation).toContain('Referrer-Policy "no-referrer" always');
    expect(shareLocation).toContain('Cache-Control "no-store" always');
    expect(shareLocation).toContain('Content-Security-Policy');
    expect(shareLocation).toContain("frame-ancestors 'none'");
    expect(config.indexOf('location ^~ /s/')).toBeLessThan(config.indexOf('location / {'));
  });
});
