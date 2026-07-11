import { describe, expect, it, vi } from 'vitest';
import { JTypeApiError } from 'jtype-board-react';
import { ApiError, type ApiClient } from '../api/client';
import { makeBoardProxyClient } from './boardProxyClient';

function stubApi(overrides: Partial<ApiClient>): ApiClient {
  return overrides as ApiClient;
}

describe('makeBoardProxyClient', () => {
  it('routes listDocuments/getDocument/saveDocument through the proxy with the workspace + project', async () => {
    const boardListDocuments = vi.fn(async () => []);
    const boardGetDocument = vi.fn(async () => ({
      relativePath: 'jtype.board',
      title: 'jtype',
      isPublished: true,
      content: '{}',
      contentHash: 'h',
      versionId: 'v1',
      updatedClock: 1,
    }));
    const boardSaveDocument = vi.fn(async () => ({
      relativePath: 'jtype.board',
      contentHash: 'h2',
      updatedClock: 2,
      mergeStatus: 'accepted' as const,
    }));
    const api = stubApi({ boardListDocuments, boardGetDocument, boardSaveDocument });
    const client = makeBoardProxyClient(api, 'p1');

    await client.listDocuments('ws_1');
    expect(boardListDocuments).toHaveBeenCalledWith('p1', 'ws_1');

    await client.getDocument('ws_1', 'doc_9');
    expect(boardGetDocument).toHaveBeenCalledWith('p1', 'ws_1', 'doc_9');

    const req = { relativePath: 'jtype.board', content: '{"id":"b_1"}' };
    await client.saveDocument('ws_1', req);
    expect(boardSaveDocument).toHaveBeenCalledWith('p1', 'ws_1', req);
  });

  it('maps an ApiError from the proxy to a typed JTypeApiError (status + code)', async () => {
    const api = stubApi({
      boardListDocuments: async () => {
        throw new ApiError(403, 'forbidden', {
          error: { code: 'workspace_not_linked', message: 'nope' },
        });
      },
    });
    const client = makeBoardProxyClient(api, 'p1');
    const err = await client.listDocuments('ws_1').catch((e) => e);
    expect(err).toBeInstanceOf(JTypeApiError);
    expect(err.status).toBe(403);
    expect(err.code).toBe('workspace_not_linked');
  });

  it('falls back to http_<status> when the ApiError carries no typed code', async () => {
    const api = stubApi({
      boardGetDocument: async () => {
        throw new ApiError(503, 'unreachable');
      },
    });
    const client = makeBoardProxyClient(api, 'p1');
    const err = await client.getDocument('ws_1', 'd').catch((e) => e);
    expect(err).toBeInstanceOf(JTypeApiError);
    expect(err.status).toBe(503);
    expect(err.code).toBe('http_503');
  });

  it('maps a non-ApiError (network throw) to JTypeApiError(0, network_error)', async () => {
    const api = stubApi({
      boardSaveDocument: async () => {
        throw new TypeError('Failed to fetch https://secret?token=abc');
      },
    });
    const client = makeBoardProxyClient(api, 'p1');
    const err = await client
      .saveDocument('ws_1', { relativePath: 'x', content: '' })
      .catch((e) => e);
    expect(err).toBeInstanceOf(JTypeApiError);
    expect(err.status).toBe(0);
    expect(err.code).toBe('network_error');
    // The raw fetch message (which could embed the token) is not leaked.
    expect(err.message).not.toContain('token=abc');
  });

  it('omits subscribeBoardEvents and deleteDocument (→ visible polling, no fake-live)', () => {
    const client = makeBoardProxyClient(stubApi({}), 'p1');
    expect('subscribeBoardEvents' in client).toBe(false);
    expect('deleteDocument' in client).toBe(false);
  });
});
