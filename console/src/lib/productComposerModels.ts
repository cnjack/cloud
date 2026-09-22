import type { ModelRef, ProviderInfo } from 'jcode-ui/product';
import type { ProjectModel } from '../api/types';

export function projectModelRef(model: ProjectModel): ModelRef {
  // Upstream names are not unique across personal and granted providers.
  // The composer must round-trip the catalog identity used by task authorization.
  const slash = model.model_name.indexOf('/');
  return { provider: model.provider_id || model.id, model: slash > 0 ? model.model_name.slice(slash + 1) : model.model_name };
}

export function projectModelKey(model: ProjectModel): string {
  const ref = projectModelRef(model);
  return `${ref.provider}/${ref.model}`;
}

export function buildProjectModelProviders(models: readonly ProjectModel[]): ProviderInfo[] {
  const grouped = new Map<string, ProviderInfo>();
  for (const model of models) {
    const ref = projectModelRef(model);
    const kind = model.provider_kind || (model.model_name.includes('/') ? model.model_name.split('/')[0]! : 'cloud');
    const provider = grouped.get(ref.provider) ?? {
      id: ref.provider,
      name: model.provider_name || (kind === 'cloud' ? 'Cloud' : kind),
      kind,
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
