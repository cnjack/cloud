import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { DESKTOP_PROVIDERS, desktopProvider } from './desktopProviders';

describe('Desktop provider registry parity', () => {
  it('keeps the curated Desktop ids and visible names in the same order', () => {
    expect(DESKTOP_PROVIDERS.map(({ id, name }) => [id, name])).toEqual([
      ['openai', 'OpenAI'],
      ['anthropic', 'Anthropic'],
      ['google', 'Google'],
      ['deepseek', 'DeepSeek'],
      ['zhipuai', 'Zhipu AI'],
      ['zhipuai-coding-plan', 'Zhipu AI Coding Plan'],
      ['mistral', 'Mistral'],
      ['openrouter', 'OpenRouter'],
      ['groq', 'Groq'],
      ['togetherai', 'Together AI'],
      ['alibaba-cn', 'Alibaba (China)'],
      ['alibaba-coding-plan-cn', 'Alibaba Coding Plan (China)'],
      ['moonshotai', 'Moonshot AI'],
      ['minimax', 'MiniMax (minimax.io)'],
      ['minimax-coding-plan', 'MiniMax Token Plan (minimax.io)'],
      ['siliconflow', 'SiliconFlow'],
      ['tencent-coding-plan', 'Tencent Coding Plan (China)'],
      ['tencent-tokenhub', 'Tencent TokenHub'],
      ['zai', 'Z.AI'],
      ['zai-coding-plan', 'Z.AI Coding Plan'],
      ['xiaomi', 'Xiaomi'],
      ['xiaomi-token-plan-cn', 'Xiaomi Token Plan (China)'],
      ['ollama-cloud', 'Ollama Cloud'],
      ['alibaba-token-plan-cn', 'Alibaba Token Plan (China)'],
      ['alibaba-token-plan', 'Alibaba Token Plan'],
      ['kimi-for-coding', 'Kimi For Coding'],
      ['tencent-tokenhub-ep', 'Tencent TokenHub Enterprise'],
    ]);
  });

  it('resolves a provider by its exact Desktop id', () => {
    expect(desktopProvider('zhipuai-coding-plan')).toMatchObject({
      name: 'Zhipu AI Coding Plan',
      baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
    });
  });

  it('matches the checked-out Desktop registry source', () => {
    const modelDir = resolve(process.cwd(), '../../jcode/internal/model');
    const generated = readFileSync(resolve(modelDir, 'registry_generated.go'), 'utf8');
    const maintained = readFileSync(resolve(modelDir, 'registry.go'), 'utf8');
    const generatedOrder = generated.match(/var generatedProviderOrder = \[\]string\{([\s\S]*?)\n\}/)?.[1]
      ?.match(/"[^"]+"/g)?.map((id) => id.slice(1, -1)) ?? [];
    const staticOrder = maintained.match(/var staticProviderOrder = \[\]string\{([\s\S]*?)\n\}/)?.[1]
      ?.match(/"[^"]+"/g)?.map((id) => id.slice(1, -1)) ?? [];
    const names = new Map<string, string>();
    for (const match of generated.matchAll(/^\s*"([^"]+)": \{\n\s*ID:\s*"[^"]+",\n\s*Name:\s*"([^"]+)"/gm)) names.set(match[1]!, match[2]!);
    const staticBlock = maintained.match(/var staticProviders = map\[string\]\*RegistryProvider\{([\s\S]*?)\n\}\n\n\/\/ staticProviderOrder/)?.[1] ?? '';
    for (const match of staticBlock.matchAll(/^\s*"([^"]+)": \{\n\s*ID:\s*"[^"]+",\n\s*Name:\s*"([^"]+)"/gm)) names.set(match[1]!, match[2]!);

    expect(DESKTOP_PROVIDERS.map(({ id, name }) => [id, name])).toEqual(
      [...generatedOrder, ...staticOrder].map((id) => [id, names.get(id)]),
    );
  });
});
