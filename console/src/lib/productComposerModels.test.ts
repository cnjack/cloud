import { describe, expect, it } from 'vitest';
import type { ProjectModel } from '../api/types';
import { buildProjectModelProviders, projectModelKey, projectModelRef } from './productComposerModels';

describe('Cloud model identity', () => {
  it('keeps personal and granted models distinct when they use the same upstream model', () => {
    const models: ProjectModel[] = ['personal-model', 'granted-model'].map(id => ({
      id, name: id, model_name: 'openai/gpt-test', capabilities: { tools: true, reasoning: true, image: false },
    }));
    const byRef = new Map(models.map(model => [projectModelKey(model), model.id]));
    expect(byRef.size).toBe(2);
    expect(models.map(model => byRef.get(`${projectModelRef(model).provider}/${projectModelRef(model).model}`)))
      .toEqual(['personal-model', 'granted-model']);
    const providers = buildProjectModelProviders(models);
    expect(providers[0]!.kind).toBe('openai');
    expect(providers).toHaveLength(2);
    expect(providers.map(provider => provider.models[0]!.id)).toEqual(['gpt-test','gpt-test']);
  });
});
