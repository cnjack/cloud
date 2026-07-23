/**
 * The provider picker used by jcode Desktop, mirrored for Cloud.
 *
 * IDs, visible names, order, and endpoints come from Desktop's curated model
 * registry (`internal/model/registry_generated.go` + `staticProviders`). Cloud
 * stores the Desktop id as `kind`, so model references and provider icons stay
 * identical between the two products.
 */
export interface DesktopProviderPreset {
  id: string;
  name: string;
  baseUrl: string;
}

export const DESKTOP_PROVIDERS: readonly DesktopProviderPreset[] = [
  { id: 'openai', name: 'OpenAI', baseUrl: 'https://api.openai.com/v1' },
  { id: 'anthropic', name: 'Anthropic', baseUrl: 'https://api.anthropic.com/v1' },
  { id: 'google', name: 'Google', baseUrl: 'https://generativelanguage.googleapis.com/v1beta/openai' },
  { id: 'deepseek', name: 'DeepSeek', baseUrl: 'https://api.deepseek.com' },
  { id: 'zhipuai', name: 'Zhipu AI', baseUrl: 'https://open.bigmodel.cn/api/paas/v4' },
  { id: 'zhipuai-coding-plan', name: 'Zhipu AI Coding Plan', baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4' },
  { id: 'mistral', name: 'Mistral', baseUrl: 'https://api.mistral.ai/v1' },
  { id: 'openrouter', name: 'OpenRouter', baseUrl: 'https://openrouter.ai/api/v1' },
  { id: 'groq', name: 'Groq', baseUrl: 'https://api.groq.com/openai/v1' },
  { id: 'togetherai', name: 'Together AI', baseUrl: 'https://api.together.xyz/v1' },
  { id: 'alibaba-cn', name: 'Alibaba (China)', baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1' },
  { id: 'alibaba-coding-plan-cn', name: 'Alibaba Coding Plan (China)', baseUrl: 'https://coding.dashscope.aliyuncs.com/v1' },
  { id: 'alibaba-token-plan-cn', name: 'Alibaba Token Plan (China)', baseUrl: 'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1' },
  { id: 'alibaba-token-plan', name: 'Alibaba Token Plan', baseUrl: 'https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1' },
  { id: 'moonshotai', name: 'Moonshot AI', baseUrl: 'https://api.moonshot.ai/v1' },
  { id: 'minimax', name: 'MiniMax (minimax.io)', baseUrl: 'https://api.minimax.io/v1' },
  { id: 'minimax-coding-plan', name: 'MiniMax Token Plan (minimax.io)', baseUrl: 'https://api.minimax.io/v1' },
  { id: 'siliconflow', name: 'SiliconFlow', baseUrl: 'https://api.siliconflow.com/v1' },
  { id: 'tencent-coding-plan', name: 'Tencent Coding Plan (China)', baseUrl: 'https://api.lkeap.cloud.tencent.com/coding/v3' },
  { id: 'tencent-tokenhub', name: 'Tencent TokenHub', baseUrl: 'https://tokenhub.tencentmaas.com/v1' },
  { id: 'zai', name: 'Z.AI', baseUrl: 'https://api.z.ai/api/paas/v4' },
  { id: 'zai-coding-plan', name: 'Z.AI Coding Plan', baseUrl: 'https://api.z.ai/api/coding/paas/v4' },
  { id: 'xiaomi', name: 'Xiaomi', baseUrl: 'https://api.xiaomimimo.com/v1' },
  { id: 'xiaomi-token-plan-cn', name: 'Xiaomi Token Plan (China)', baseUrl: 'https://token-plan-cn.xiaomimimo.com/v1' },
  { id: 'ollama-cloud', name: 'Ollama Cloud', baseUrl: 'https://ollama.com/v1' },
  { id: 'kimi-for-coding', name: 'Kimi For Coding', baseUrl: 'https://api.kimi.com/coding/v1' },
  { id: 'tencent-tokenhub-ep', name: 'Tencent TokenHub Enterprise', baseUrl: 'https://tokenhub.tencentmaas.com/plan/v3' },
] as const;

export function desktopProvider(id: string): DesktopProviderPreset | undefined {
  return DESKTOP_PROVIDERS.find((provider) => provider.id === id);
}
