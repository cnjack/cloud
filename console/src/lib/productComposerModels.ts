import type { ModelRef, ProviderInfo } from 'jcode-ui/product';
import type { ProjectModel } from '../api/types';

export function projectModelRef(model: ProjectModel): ModelRef {
  const slash = model.model_name.indexOf('/');
  return slash > 0
    ? { provider: model.model_name.slice(0, slash), model: model.model_name.slice(slash + 1) }
    : { provider: 'cloud', model: model.model_name };
}

export function projectModelKey(model: ProjectModel): string {
  const ref = projectModelRef(model);
  return `${ref.provider}/${ref.model}`;
}

export function buildProjectModelProviders(models: readonly ProjectModel[]): ProviderInfo[] {
  const grouped = new Map<string, ProviderInfo>();
  for (const model of models) {
    const ref = projectModelRef(model);
    const provider = grouped.get(ref.provider) ?? {
      id: ref.provider,
      name: ref.provider === 'cloud' ? 'Cloud' : ref.provider,
      kind: ref.provider,
      source: 'cloud' as const,
      models: [],
    };
    provider.models.push({
      id: ref.model,
      name: model.name,
      enabled: true,
      tool_call: model.capabilities.tools,
      reasoning: model.capabilities.reasoning,
      image_support: model.capabilities.image,
      input_modalities: model.capabilities.image ? ['text', 'image'] : ['text'],
      output_modalities: ['text'],
      reasoning_options: model.capabilities.reasoning
        ? [{ type: 'effort', values: ['low', 'medium', 'high'] }]
        : [],
    });
    grouped.set(ref.provider, provider);
  }
  return [...grouped.values()];
}
