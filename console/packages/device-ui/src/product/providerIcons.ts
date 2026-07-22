import openai from '@lobehub/icons-static-svg/icons/openai.svg?raw';
import anthropic from '@lobehub/icons-static-svg/icons/anthropic.svg?raw';
import gemini from '@lobehub/icons-static-svg/icons/gemini-color.svg?raw';
import deepseek from '@lobehub/icons-static-svg/icons/deepseek-color.svg?raw';
import zhipu from '@lobehub/icons-static-svg/icons/zhipu-color.svg?raw';
import qwen from '@lobehub/icons-static-svg/icons/qwen-color.svg?raw';
import moonshot from '@lobehub/icons-static-svg/icons/moonshot.svg?raw';

const ICONS: ReadonlyArray<readonly [RegExp, string]> = [
  [/openai/, openai],
  [/anthropic|claude/, anthropic],
  [/gemini|google|vertex/, gemini],
  [/deepseek/, deepseek],
  [/zhipu|zai/, zhipu],
  [/alibaba|qwen|dashscope|tongyi/, qwen],
  [/moonshot|kimi/, moonshot],
];

export function iconForDeviceProvider(provider: string): string | null {
  const key = provider.toLowerCase();
  return ICONS.find(([pattern]) => pattern.test(key))?.[1] ?? null;
}
