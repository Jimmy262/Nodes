const snapshotPaths = new Set(['/overview', '/operations']);

// Only inventory snapshots are retained, in memory, for this login generation.
// Never cache environment values, credentials, logs, or setup responses.
export function createApiClient({
  preview = false,
  onUnauthorized = () => {},
  fetcher = fetch,
} = {}) {
  const snapshots = new Map();
  let generation = 0;
  function reset() {
    generation += 1;
    snapshots.clear();
  }
  async function api(path, options = {}) {
    if (preview) throw new Error('La vista de diseño no conecta con servidores.');
    const epoch = generation;
    const { retainSessionOn401 = false, ...requestOptions } = options;
    const headers = new Headers(options.headers);
    if (!(options.body instanceof FormData) && !headers.has('Content-Type'))
      headers.set('Content-Type', 'application/json');
    const conditional =
      (options.method || 'GET').toUpperCase() === 'GET' && snapshotPaths.has(path);
    const cached = conditional ? snapshots.get(path) : null;
    if (cached) headers.set('If-None-Match', cached.etag);
    const response = await fetcher('/api' + path, {
      ...requestOptions,
      headers,
      cache: 'no-store',
      signal: options.signal || AbortSignal.timeout(35000),
    });
    if (response.status === 304 && cached) return cached.data;
    if (response.status === 401 && !retainSessionOn401 && epoch === generation) {
      reset();
      onUnauthorized();
    }
    let data;
    try {
      data = await response.json();
    } catch {
      throw new Error(
        response.ok
          ? 'El servidor devolvió una respuesta inválida.'
          : `No se pudo completar la solicitud (HTTP ${response.status}).`,
      );
    }
    if (!response.ok) {
      throw new Error(data.error || 'No se pudo completar la solicitud');
    }
    const etag = response.headers.get('ETag');
    if (conditional && etag && epoch === generation) snapshots.set(path, { etag, data });
    return data;
  }
  return { api, reset };
}
