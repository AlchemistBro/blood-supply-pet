// Экспорт исходников документации; приложение, тесты и Docker здесь не запускаются.
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import puppeteer from 'puppeteer';

const root = fileURLToPath(new URL('../../', import.meta.url));
const output = join(root, 'dist/diagrams');
const manifest = JSON.parse(readFileSync(join(output, 'manifest.json'), 'utf8'));
const launch = process.env.CI === 'true' ? { args: ['--no-sandbox'] } : {};
const browserConfig = join(output, 'puppeteer.json');
writeFileSync(browserConfig, JSON.stringify(launch));
const cli = join(dirname(fileURLToPath(import.meta.resolve('@mermaid-js/mermaid-cli'))), 'cli.js');

for (const item of manifest.filter(item => item.kind === 'mermaid')) {
  const result = spawnSync(process.execPath, [cli, '-p', browserConfig, '-i', join(output, item.input), '-o', join(output, item.svg)], { stdio: 'inherit' });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Не удалось экспортировать ${item.source}`);
}

const browser = await puppeteer.launch({ headless: true, ...launch });
try {
  const page = await browser.newPage();
  for (const item of manifest.filter(item => item.kind === 'bpmn')) {
    await page.setContent('<!doctype html><html><head><meta charset="UTF-8"></head><body><div id="canvas" style="width:2400px;height:1600px"></div></body></html>');
    await page.addScriptTag({ path: fileURLToPath(import.meta.resolve('bpmn-js/dist/bpmn-viewer.production.min.js')) });
    const xml = readFileSync(join(root, item.source), 'utf8');
    const svg = await page.evaluate(async xml => {
      const viewer = new window.BpmnJS({ container: '#canvas' });
      try {
        const { warnings } = await viewer.importXML(xml);
        if (warnings.length) throw new Error(warnings.map(warning => warning.message).join('; '));
        return (await viewer.saveSVG()).svg;
      } finally {
        viewer.destroy();
      }
    }, xml);
    writeFileSync(join(output, item.svg), svg);
    console.log(`Экспортирован ${item.source}`);
  }
} finally {
  await browser.close();
}
