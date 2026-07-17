/*
 * ClusterModelsPage — the cluster catalog of model providers (design/cluster-
 * models.html). The provider cards, catalog discovery, custom models, verify and
 * per-model grant management live in the shared, scope-parameterized ModelsCatalog
 * (pages/models); this page is the cluster-admin shell around it (subnav, header,
 * the page-header "Add provider" button, and the access gate).
 */
import { Plus } from '@phosphor-icons/react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useRole } from '../api/ApiProvider';
import type { ModelProvider } from '../api/types';
import { Button } from '../components/Button';
import { ClusterSubnav, PageHeader, SurfaceInner } from '../components/PageLayout';
import { ClusterAccessDenied } from './ClusterAccessDenied';
import { ModelsCatalog } from './models/ModelsCatalog';

export function ClusterModelsPage() {
  const { t } = useTranslation();
  const isAdmin = useRole() === 'cluster-admin';
  const [providerEditor, setProviderEditor] = useState<ModelProvider | 'new' | null>(null);
  if (!isAdmin) return <ClusterAccessDenied />;

  return (
    <>
      <ClusterSubnav />
      <SurfaceInner>
        <PageHeader
          eyebrow={t('cluster.models.eyebrow')}
          title={t('cluster.models.title')}
          description={t('cluster.models.description')}
          actions={<Button variant="primary" onClick={() => setProviderEditor('new')}><Plus size={14} aria-hidden="true" />{t('cluster.models.addProvider')}</Button>}
        />
        <ModelsCatalog scope={{ kind: 'cluster' }} editor={providerEditor} onEditorChange={setProviderEditor} />
      </SurfaceInner>
    </>
  );
}
