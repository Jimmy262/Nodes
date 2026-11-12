<script>
  import { onMount } from 'svelte';
  import {
    Layers,
    Server,
    Plus,
    Search,
    ChevronDown,
    ChevronRight,
    X,
    LogOut,
    ShieldCheck,
    ArrowRight,
    AlertCircle,
    Trash2,
    Upload,
    History,
    SquareTerminal,
  } from '@lucide/svelte';
  import AppTable from './AppTable.svelte';
  import ApplicationDrawer from './ApplicationDrawer.svelte';
  import ApplicationEditor from './ApplicationEditor.svelte';
  import HistoryChart from './HistoryChart.svelte';
  import AuthScreen from './AuthScreen.svelte';
  import BrandLogo from './BrandLogo.svelte';
  import ThemeControl from './ThemeControl.svelte';
  import SecurityDialog from './SecurityDialog.svelte';
  import ServerNameEditor from './ServerNameEditor.svelte';
  import OperationDialog from './OperationDialog.svelte';
  import DeploymentDialog from './DeploymentDialog.svelte';
  import GithubDialog from './GithubDialog.svelte';
  import ComposeLibrary from './ComposeLibrary.svelte';
  import OperationsHistory from './OperationsHistory.svelte';
  import { createApiClient } from './lib/api.js';
  import { bytes, percent, age, stale, storageRead, storageWrite, modal } from './format.js';
  const designPreview =
    import.meta.env.DEV && new URLSearchParams(location.search).get('preview') === 'design';
  const client = createApiClient({ preview: designPreview, onUnauthorized: signedOut });
  const api = client.api;
  let lastOverview = null;
  const saved = storageRead('nodo.view.v2', {});
  let authenticated = false,
    ready = false,
    loaded = false;
  let authState = { configured: true, setupAuthorized: false },
    authError = '',
    securityOpen = false;
  let servers = [],
    error = '',
    histories = {},
    historyErrors = {},
    now = Date.now();
  let view = saved.view === 'servers' ? 'servers' : 'apps';
  let selection = Array.isArray(saved.selection) ? saved.selection.filter(Number.isFinite) : null;
  let query = typeof saved.query === 'string' ? saved.query : '';
  let favorites = storageRead('nodo.favorites', []);
  if (!Array.isArray(favorites)) favorites = [];
  let filter = ['all', 'favorites', 'incidents'].includes(saved.filter) ? saved.filter : 'all';
  let sort = ['name', 'cpu', 'memory'].includes(saved.sort) ? saved.sort : 'name';
  let filterOpen = false,
    showInternal = false,
    selectedAppID = '',
    connectionID = null;
  let adding = false,
    saving = false,
    formError = '',
    removing = false,
    confirmRemoval = false;
  let form = { name: '', host: '', port: 22, user: 'root', identity: '' };
  let editingApp = null;
  let terminalView = null;
  function openTerminal(server, kind) {
    terminalView = { server, kind };
  }
  let operation = null,
    operations = [],
    activityOpen = false;
  let refreshInFlight = false,
    historyVersion = 0,
    lastHistoryRead = 0,
    authVersion = 0;
  $: applicationServers =
    selection === null ? servers : servers.filter((server) => selection.includes(server.id));
  $: chosen = view === 'servers' ? servers : applicationServers;
  $: if (view !== 'apps') filterOpen = false;
  $: allSelected = servers.length > 0 && chosen.length === servers.length;
  $: pickerLabel = allSelected
    ? 'Todos los servidores'
    : chosen.length === 1
      ? chosen[0].name
      : `${chosen.length} servidores`;
  $: apps = servers.flatMap((server) =>
    (server.docker.applications || []).map((app) => ({
      ...app,
      server,
      stale:
        !!server.docker.error ||
        server.state.status === 'offline' ||
        stale(server.docker.at, staleNow),
    })),
  );
  $: staleNow = Math.floor(now / 5000) * 5000;
  $: visible = apps
    .filter((app) => chosen.some((server) => server.id === app.serverId))
    .filter((app) => {
      const search = query.trim().toLocaleLowerCase();
      return (
        (!search ||
          `${app.name} ${app.server.name} ${app.links.map((link) => link.url).join(' ')} ${app.services.map((service) => service.Names).join(' ')}`
            .toLocaleLowerCase()
            .includes(search)) &&
        (filter !== 'favorites' || favorites.includes(app.id)) &&
        (filter !== 'incidents' || app.status !== 'running' || app.stale || app.partial)
      );
    })
    .sort((a, b) =>
      sort === 'name'
        ? a.name.localeCompare(b.name, 'es') || a.serverId - b.serverId
        : (b[sort] ?? -1) - (a[sort] ?? -1) || a.name.localeCompare(b.name, 'es'),
    );
  $: publicApps = visible.filter((app) => !app.internal);
  $: internalApps = visible.filter((app) => app.internal);
  $: if (query.trim() || filter !== 'all') showInternal = true;
  $: selectedApp = apps.find((app) => app.id === selectedAppID);
  $: connection = servers.find((server) => server.id === connectionID);
  $: issues = chosen.filter(
    (server) =>
      server.state.error ||
      server.docker.error ||
      server.docker.warnings?.length ||
      (server.docker.at && stale(server.docker.at, now)),
  );
  $: loadingServers = chosen.filter((server) => !server.docker.at && !server.docker.error);
  $: times = chosen
    .map((server) => (view === 'apps' ? server.docker.at : server.state.sample?.at))
    .filter(Boolean);
  $: updated = times.length ? new Date(Math.min(...times.map(Date.parse))).toISOString() : '';
  $: historyKey =
    authenticated && view === 'servers' ? chosen.map((server) => server.id).join(',') : '';
  $: if (historyKey) refreshHistories(historyKey);
  $: storageWrite('nodo.view.v2', { view, selection, query, filter, sort });
  $: storageWrite('nodo.favorites', favorites);
  async function refresh() {
    if (refreshInFlight || designPreview) return;
    refreshInFlight = true;
    const version = authVersion;
    try {
      const [data, jobs] = await Promise.all([api('/overview'), api('/operations')]);
      if (version !== authVersion) return;
      if (operations !== jobs) operations = jobs;
      if (lastOverview !== data) {
        lastOverview = data;
        servers = data.servers.map((server) => ({
          ...server,
          docker: {
            ...server.docker,
            applications: server.docker.applications || [],
            warnings: server.docker.warnings || [],
          },
        }));
      }
      loaded = true;
      error = '';
      if (view === 'servers' && Date.now() - lastHistoryRead > 15000)
        await refreshHistories(servers.map((s) => s.id).join(','));
    } catch (e) {
      if (version === authVersion) error = e.message;
    } finally {
      refreshInFlight = false;
    }
  }
  async function refreshHistories(key) {
    if (!key || designPreview) return;
    const version = ++historyVersion;
    lastHistoryRead = Date.now();
    const ids = key.split(',').map(Number);
    const results = await Promise.allSettled(ids.map((id) => api(`/servers/${id}/history`)));
    if (version !== historyVersion) return;
    const next = { ...histories },
      failures = {};
    results.forEach((result, index) => {
      if (result.status === 'fulfilled') next[ids[index]] = result.value;
      else failures[ids[index]] = result.reason.message;
    });
    histories = next;
    historyErrors = failures;
  }
  onMount(() => {
    let active = true;
    if (designPreview) {
      authenticated = true;
      ready = true;
      loaded = true;
    } else {
      const fragment = new URLSearchParams(location.hash.slice(1));
      const setupToken = fragment.get('setup');
      if (setupToken) history.replaceState(null, '', location.pathname + location.search);
      const prepare = setupToken
        ? api('/auth/prepare', { method: 'POST', body: JSON.stringify({ setupToken }) }).catch(
            (e) => {
              authError = e.message;
            },
          )
        : Promise.resolve();
      prepare
        .then(() => api('/auth/status'))
        .then(async (state) => {
          if (active) {
            authState = state;
            authenticated = state.authenticated;
            if (authenticated) await refresh();
          }
        })
        .catch((e) => {
          authError = e.message;
        })
        .finally(() => {
          if (active) ready = true;
        });
    }
    const timer = setInterval(() => {
      if (authenticated && !document.hidden) refresh();
    }, 5000);
    const clock = setInterval(() => (now = Date.now()), 1000);
    return () => {
      active = false;
      clearInterval(timer);
      clearInterval(clock);
    };
  });
  async function signedIn() {
    client.reset();
    lastOverview = null;
    authVersion++;
    authState = { configured: true, setupAuthorized: false };
    authError = '';
    authenticated = true;
    await refresh();
  }
  function signedOut() {
    client.reset();
    lastOverview = null;
    authVersion++;
    historyVersion++;
    authenticated = false;
    securityOpen = false;
    servers = [];
    histories = {};
    historyErrors = {};
    selectedAppID = '';
    editingApp = null;
    adding = false;
    operation = null;
    operations = [];
    activityOpen = false;
    terminalView = null;
    connectionID = null;
    loaded = false;
    error = '';
  }
  async function logout() {
    try {
      await api('/logout', { method: 'POST' });
      signedOut();
      terminalView = null;
    } catch (e) {
      error = e.message;
    }
  }
  function toggleServer(id) {
    const current = selection === null ? servers.map((server) => server.id) : selection;
    const next = current.includes(id) ? current.filter((item) => item !== id) : [...current, id];
    selection =
      next.length === servers.length && servers.every((server) => next.includes(server.id))
        ? null
        : next;
  }
  function indeterminate(node, value) {
    node.indeterminate = value;
    return {
      update(next) {
        node.indeterminate = next;
      },
    };
  }
  function closePicker(event) {
    if (event.target instanceof Element && !event.target.closest('.server-picker'))
      filterOpen = false;
  }
  function favorite(id) {
    favorites = favorites.includes(id)
      ? favorites.filter((item) => item !== id)
      : [...favorites, id];
  }
  function serverApps(id) {
    selection = [id];
    view = 'apps';
    connectionID = null;
  }
  async function save(event) {
    event.preventDefault();
    saving = true;
    formError = '';
    try {
      const server = await api('/servers', {
        method: 'POST',
        body: JSON.stringify({ ...form, port: Number(form.port) }),
      });
      if (selection !== null) selection = [...selection, server.id];
      form = { name: '', host: '', port: 22, user: 'root', identity: '' };
      adding = false;
      await refresh();
    } catch (e) {
      formError = e.message;
    } finally {
      saving = false;
    }
  }
  async function removeServer() {
    if (!connection) return;
    removing = true;
    formError = '';
    try {
      await api(`/servers/${connection.id}`, { method: 'DELETE' });
      if (selection) selection = selection.filter((id) => id !== connection.id);
      connectionID = null;
      confirmRemoval = false;
      await refresh();
    } catch (e) {
      formError = e.message;
    } finally {
      removing = false;
    }
  }
</script>

<svelte:window
  on:click={closePicker}
  on:keydown={(event) => {
    if (event.key === 'Escape') filterOpen = false;
  }}
  on:focus={() => {
    if (authenticated) refresh();
  }}
/>
<svelte:head><title>Nodes · {view === 'apps' ? 'Aplicaciones' : 'Servidores'}</title></svelte:head>
{#if !ready}<div class="boot">
    <BrandLogo />
    <p>Abriendo tu infraestructura…</p>
  </div>
{:else if !authenticated}
  <AuthScreen {api} state={authState} initialError={authError} onAuthenticated={signedIn} />
{:else}
  <div class="shell">
    <aside class="sidebar">
      <a
        class="brand-link"
        href="#aplicaciones"
        on:click={() => (view = 'apps')}
        aria-label="Nodes, aplicaciones"><BrandLogo /></a
      >
      <nav aria-label="Navegación principal">
        <button
          class:active={view === 'apps'}
          on:click={() => (view = 'apps')}
          aria-current={view === 'apps' ? 'page' : undefined}
          ><Layers size={20} strokeWidth={1.6} />Aplicaciones</button
        ><button
          class:active={view === 'servers'}
          on:click={() => (view = 'servers')}
          aria-current={view === 'servers' ? 'page' : undefined}
          ><Server size={20} strokeWidth={1.6} />Servidores</button
        >
      </nav>
      <div class="sidebar-bottom">
        <ThemeControl />
        <button
          class="security-shortcut"
          on:click={() => (securityOpen = true)}
          aria-label="Acceso y seguridad"
          ><ShieldCheck size={17} /><span class="security-label">Seguridad</span></button
        ><button
          class="icon-button logout-shortcut"
          on:click={logout}
          title="Cerrar sesión"
          aria-label="Cerrar sesión"><LogOut size={18} /></button
        >
      </div>
    </aside>
    <main class="main-content">
      {#if designPreview}<p class="notice warning">
          Vista de diseño · sin conexión al backend ni datos ficticios.
        </p>{/if}
      <header class="page-heading">
        <h1>{view === 'apps' ? 'Aplicaciones' : 'Servidores'}</h1>
        <div class="page-actions">
          <button class="secondary" on:click={() => (activityOpen = true)}
            ><History size={16} />Actividad{operations.some((item) => item.status === 'running')
              ? ' · En curso'
              : ''}</button
          >
          {#if view === 'apps' && servers.length}<button
              class="secondary"
              on:click={() => (operation = { kind: 'library', sourceId: chosen[0]?.id })}
              >Biblioteca Compose</button
            ><button
              class="secondary"
              on:click={() => (operation = { kind: 'github', sourceId: chosen[0]?.id })}
              >GitHub</button
            ><button
              class="primary"
              on:click={() => (operation = { kind: 'deploy', sourceId: chosen[0]?.id })}
              ><Upload size={17} />Desplegar ZIP</button
            >{:else}
            <button
              class="primary"
              on:click={() => {
                adding = true;
                formError = '';
              }}><Plus size={17} />Conectar servidor</button
            >
          {/if}
        </div>
      </header>
      <div class="toolbar">
        {#if view === 'apps'}<label class="search"
            ><Search size={19} /><input
              aria-label="Buscar aplicación o dominio"
              bind:value={query}
              placeholder="Buscar aplicación o dominio…"
            />{#if query}<button
                class="icon-button"
                on:click={() => (query = '')}
                aria-label="Limpiar búsqueda"><X size={16} /></button
              >{/if}</label
          >{:else}<p class="toolbar-description">
            El estado de tu infraestructura, de un vistazo.
          </p>{/if}
        {#if chosen.length}<span
            class="freshness"
            title="Recolección automática: servidores cada 15 s; aplicaciones cada 30 s."
            ><i class:waiting={!updated || stale(updated, now)}></i>{updated
              ? age(updated, now)
              : 'Actualizando…'}</span
          >{/if}
        {#if view === 'apps'}<div class="server-picker">
            <button
              class="select-button"
              aria-expanded={filterOpen}
              aria-controls="server-options"
              on:click={() => (filterOpen = !filterOpen)}
              ><Server size={16} /><span>{pickerLabel}</span><ChevronDown size={16} /></button
            >
            {#if filterOpen}<div class="server-options" id="server-options">
                <label
                  ><input
                    type="checkbox"
                    checked={allSelected}
                    use:indeterminate={chosen.length > 0 && !allSelected}
                    on:change={() => (selection = allSelected ? [] : null)}
                  /><strong>Todos los servidores</strong></label
                >
                <div class="option-divider"></div>
                {#each servers as server}<label
                    ><input
                      type="checkbox"
                      checked={chosen.some((item) => item.id === server.id)}
                      on:change={() => toggleServer(server.id)}
                    /><span>{server.name}</span><i
                      class="status-dot"
                      class:offline={server.state.status !== 'online'}
                    ></i></label
                  >{:else}<p class="muted">Conecta tu primer servidor.</p>{/each}
              </div>{/if}
          </div>{/if}
      </div>
      {#if error}<p class="notice error" role="alert">
          <AlertCircle size={17} />{error}. Se reintentará automáticamente.
        </p>{/if}
      {#if issues.length}<details class="sync-notice">
          <summary
            ><AlertCircle size={16} />{issues.length}
            {issues.length === 1 ? 'servidor necesita' : 'servidores necesitan'} atención<ChevronDown
              size={15}
            /></summary
          >{#each issues as server}<div>
              <strong>{server.name}</strong>
              <p>
                {server.state.error ||
                  server.docker.error ||
                  (stale(server.docker.at, now) ? 'Los últimos datos están desactualizados.' : '')}
              </p>
              {#each server.docker.warnings as warning}<p>{warning}</p>{/each}<small
                >Última lectura Docker: {age(server.docker.at, now).toLowerCase()}</small
              >
            </div>{/each}
        </details>{/if}
      {#if !loaded}<div class="empty-state">
          <span class="loading-dot"></span>
          <h2>Cargando tu infraestructura</h2>
          <p>Los servidores y aplicaciones aparecerán automáticamente.</p>
        </div>
      {:else if !servers.length}<div class="empty-state">
          <Server size={32} strokeWidth={1.4} />
          <h2>Tu primer servidor empieza aquí</h2>
          <p>Conecta una VPS para ver sus aplicaciones y recursos.</p>
          <button
            class="primary"
            on:click={() => {
              adding = true;
              formError = '';
            }}><Plus size={16} />Conectar servidor</button
          >
        </div>
      {:else if !chosen.length}<div class="empty-state">
          <Server size={32} strokeWidth={1.4} />
          <h2>Elige qué servidores ver</h2>
          <p>Selecciona uno o varios en el selector de arriba.</p>
          <button class="secondary" on:click={() => (selection = null)}
            >Ver todos los servidores</button
          >
        </div>
      {:else if view === 'apps'}
        <div class="apps-view">
          <div class="tabs">
            <button
              class:active={filter === 'all'}
              aria-pressed={filter === 'all'}
              on:click={() => (filter = 'all')}>Todas</button
            ><button
              class:active={filter === 'favorites'}
              aria-pressed={filter === 'favorites'}
              on:click={() => (filter = 'favorites')}>Favoritas</button
            ><button
              class:active={filter === 'incidents'}
              aria-pressed={filter === 'incidents'}
              on:click={() => (filter = 'incidents')}>Con incidencias</button
            >
          </div>
          <div class="list-meta">
            <span
              >{publicApps.length}
              {publicApps.length === 1 ? 'aplicación' : 'aplicaciones'}{loadingServers.length
                ? ` · Detectando en ${loadingServers.map((s) => s.name).join(', ')}…`
                : ''}</span
            ><label class="mobile-sort"
              >Ordenar<select bind:value={sort}
                ><option value="name">Nombre</option><option value="cpu">CPU</option><option
                  value="memory">RAM</option
                ></select
              ></label
            ><span class="desktop-hint">Selecciona una aplicación para ver sus servicios</span>
          </div>
          {#if publicApps.length}<AppTable
              items={publicApps}
              {favorites}
              {sort}
              onOpen={(app) => (selectedAppID = app.id)}
              onFavorite={favorite}
              onSort={(value) => (sort = value)}
            />{/if}
          {#if internalApps.length}<section class="internal-section">
              <button
                class="disclosure"
                aria-expanded={showInternal}
                on:click={() => (showInternal = !showInternal)}
                ><ChevronRight size={17} class={showInternal ? 'rotated' : ''} />Servicios internos
                <span>{internalApps.length}</span></button
              >{#if showInternal}<AppTable
                  items={internalApps}
                  {favorites}
                  {sort}
                  onOpen={(app) => (selectedAppID = app.id)}
                  onFavorite={favorite}
                  onSort={(value) => (sort = value)}
                />{/if}
            </section>{/if}
          {#if !visible.length}<div class="empty-state">
              <Layers size={30} strokeWidth={1.5} />
              <h2>
                {loadingServers.length
                  ? 'Detectando aplicaciones…'
                  : filter === 'favorites'
                    ? 'Tus favoritas, a mano'
                    : filter === 'incidents'
                      ? 'Sin incidencias en esta vista'
                      : query
                        ? 'Sin resultados'
                        : 'Sin aplicaciones disponibles'}
              </h2>
              <p>
                {loadingServers.length
                  ? 'La primera conexión puede tardar unos segundos.'
                  : filter === 'favorites'
                    ? 'Marca la estrella de una aplicación para encontrarla aquí.'
                    : query
                      ? 'Prueba otro nombre o revisa los servidores seleccionados.'
                      : filter === 'incidents'
                        ? 'Las aplicaciones visibles no presentan incidencias detectadas.'
                        : 'Las aplicaciones aparecerán al recibir el inventario Docker.'}
              </p>
              {#if query}<button class="secondary" on:click={() => (query = '')}
                  >Limpiar búsqueda</button
                >{/if}
            </div>{/if}
          {#if visible.length}<p class="list-footnote">
              CPU y RAM suman los contenedores de cada aplicación. CPU respecto a la capacidad del
              servidor.{#if visible.some((app) => app.partial)}
                <span>* Consumo parcial.</span>{/if}
            </p>{/if}
        </div>
      {:else}
        <div class="server-table table-surface">
          <div class="server-columns table-heading">
            <span>Servidor</span><span>Estado</span><span>CPU</span><span>Memoria</span><span
              >Disco /</span
            ><span></span>
          </div>
          {#each chosen as server}
            {@const sample = server.state.sample}
            {@const connectionIP =
              sample?.connectionIP ||
              (server.state.status === 'pending' ? 'Detectando IP…' : 'IP no disponible')}
            {@const outdated = server.state.status !== 'online' || stale(sample?.at, now, 45000)}
            <div class="server-columns server-row" class:outdated>
              <button
                class="server-name"
                on:click={() => {
                  connectionID = server.id;
                  confirmRemoval = false;
                  formError = '';
                }}
                ><span class="app-icon"><Server size={19} /></span><span
                  ><strong>{server.name}</strong><small
                    >{sample?.cores
                      ? `${sample.cores} vCPU · ${connectionIP}`
                      : connectionIP}</small
                  ></span
                ></button
              >
              <span class="status" class:warning={outdated}
                ><i></i>{server.state.status === 'pending'
                  ? 'Conectando'
                  : outdated
                    ? 'Sin actualizar'
                    : 'Conectado'}<small>{age(sample?.at, now)}</small></span
              >
              <div class="server-resource">
                <span class="mobile-label">CPU</span><strong>{percent(sample?.cpu)}</strong><span
                  class="meter"><span style={`width:${sample?.cpu || 0}%`}></span></span
                >
              </div>
              <div class="server-resource">
                <span class="mobile-label">RAM</span><strong
                  >{sample?.memoryTotal
                    ? `${bytes(sample.memoryUsed)} / ${bytes(sample.memoryTotal)}`
                    : percent(sample?.memory)}</strong
                ><span class="meter"><span style={`width:${sample?.memory || 0}%`}></span></span>
              </div>
              <div class="server-resource">
                <span class="mobile-label">Disco /</span><strong
                  >{sample?.diskTotal
                    ? `${bytes(sample.diskUsed)} / ${bytes(sample.diskTotal)}`
                    : percent(sample?.disk)}</strong
                ><span class="meter" class:high={(sample?.disk || 0) > 85}
                  ><span style={`width:${sample?.disk || 0}%`}></span></span
                >
              </div>
              <div class="server-actions">
                <button
                  class="secondary codex-button"
                  on:click={() => openTerminal(server, 'codex')}
                  aria-label={`Abrir Codex en ${server.name}`}
                  title="Abrir o retomar Codex"><SquareTerminal size={17} />Codex</button
                >
                <button
                  class="secondary codex-button"
                  on:click={() => openTerminal(server, 'shell')}
                  aria-label={`Abrir terminal de ${server.name}`}
                  title="Abrir o retomar la terminal SSH"
                  ><SquareTerminal size={17} />Terminal</button
                >
                <button
                  class="icon-button"
                  on:click={() => serverApps(server.id)}
                  aria-label={`Ver aplicaciones de ${server.name}`}
                  title="Ver aplicaciones"><ArrowRight size={18} /></button
                >
              </div>
            </div>
          {/each}
        </div>
        <HistoryChart servers={chosen} {histories} {now} />
        {#if Object.keys(historyErrors).length}<p class="notice warning">
            No se pudo actualizar el histórico de {chosen
              .filter((s) => historyErrors[s.id])
              .map((s) => s.name)
              .join(', ')}. Se reintentará automáticamente.
          </p>{/if}
        <div class="servers-bottom">
          <span class="muted small"
            >Cada línea representa el porcentaje de capacidad de una VPS.</span
          ><button class="text-button" on:click={() => (view = 'apps')}
            >Ver aplicaciones de estos servidores <ArrowRight size={16} /></button
          >
        </div>
      {/if}
    </main>
  </div>
  {#if terminalView}{#await import('./TerminalDialog.svelte') then terminalModule}<svelte:component
        this={terminalModule.default}
        server={terminalView.server}
        kind={terminalView.kind}
        {api}
        onClose={() => (terminalView = null)}
      />{/await}{/if}
  {#if securityOpen}<SecurityDialog
      {api}
      onClose={() => (securityOpen = false)}
      onLogout={logout}
      onSignedOut={signedOut}
    />{/if}
  {#if selectedApp}<ApplicationDrawer
      app={selectedApp}
      {api}
      {now}
      onEdit={() => {
        editingApp = selectedApp;
        selectedAppID = '';
      }}
      onClose={() => (selectedAppID = '')}
      onManage={(kind, service) => {
        operation = { kind, service, sourceId: selectedApp.serverId };
        selectedAppID = '';
      }}
    />{/if}
  {#if editingApp}<ApplicationEditor
      app={editingApp}
      {api}
      onClose={() => {
        selectedAppID = editingApp.id;
        editingApp = null;
      }}
      onSaved={refresh}
    />{/if}
  {#if operation?.kind === 'library'}<ComposeLibrary
      sourceId={operation.sourceId}
      {api}
      {servers}
      onClose={() => (operation = null)}
      onStarted={refresh}
    />
  {:else if operation?.kind === 'github'}<GithubDialog
      sourceId={operation.sourceId}
      {api}
      {servers}
      onClose={() => (operation = null)}
      onStarted={refresh}
    />
  {:else if operation?.kind === 'deploy'}<DeploymentDialog
      sourceId={operation.sourceId}
      {api}
      {servers}
      onClose={() => (operation = null)}
      onStarted={refresh}
    />
  {:else if operation}<OperationDialog
      {...operation}
      {api}
      {servers}
      onClose={() => (operation = null)}
      onStarted={refresh}
    />{/if}
  {#if activityOpen}<OperationsHistory
      {operations}
      {servers}
      onClose={() => (activityOpen = false)}
    />{/if}
  {#if adding}<dialog
      use:modal
      class="form-dialog"
      aria-labelledby="connect-title"
      on:cancel={() => (adding = false)}
    >
      <header>
        <h2 id="connect-title">Conectar servidor</h2>
        <button class="icon-button" on:click={() => (adding = false)} aria-label="Cerrar formulario"
          ><X size={20} /></button
        >
      </header>
      <p class="muted">Usa la conexión SSH que ya tienes en este equipo.</p>
      <form on:submit={save}>
        <label class="field"
          >Nombre<input
            bind:value={form.name}
            placeholder="Producción"
            maxlength="80"
            required
          /></label
        ><label class="field"
          >Host o alias SSH<input
            bind:value={form.host}
            placeholder="mi-servidor o 203.0.113.10"
            required
          /></label
        >
        <div class="form-pair">
          <label class="field">Usuario<input bind:value={form.user} required /></label><label
            class="field"
            >Puerto<input
              type="number"
              bind:value={form.port}
              min="1"
              max="65535"
              required
            /></label
          >
        </div>
        <label class="field"
          >Ruta de clave privada <span class="muted small"
            >Opcional si está en tu configuración SSH</span
          ><input
            bind:value={form.identity}
            placeholder="C:\Users\tu-usuario\.ssh\id_ed25519"
          /></label
        >{#if formError}<p class="notice error" role="alert">{formError}</p>{/if}
        <p class="small muted">
          Las aplicaciones y métricas se cargarán automáticamente al conectar.
        </p>
        <button class="primary" disabled={saving}
          >{saving ? 'Guardando…' : 'Conectar servidor'}<ArrowRight size={16} /></button
        >
      </form>
    </dialog>{/if}
  {#if connection}<dialog
      use:modal
      class="form-dialog"
      aria-labelledby="server-title"
      on:cancel={() => (connectionID = null)}
    >
      <header>
        <h2 id="server-title">{connection.name}</h2>
        <button
          class="icon-button"
          on:click={() => (connectionID = null)}
          aria-label="Cerrar servidor"><X size={20} /></button
        >
      </header>
      {#key connection.id}<ServerNameEditor server={connection} {api} onSaved={refresh} />{/key}
      <dl class="connection-info">
        <dt>Host</dt>
        <dd>{connection.host}</dd>
        <dt>Conexión</dt>
        <dd>{connection.user} · puerto {connection.port}</dd>
        <dt>Clave SSH</dt>
        <dd>{connection.identity || 'Configuración de OpenSSH'}</dd>
        <dt>Última lectura</dt>
        <dd>{age(connection.state.sample?.at, now)}</dd>
      </dl>
      {#if connection.state.error}<p class="notice error">{connection.state.error}</p>{/if}<button
        class="primary"
        on:click={() => serverApps(connection.id)}>Ver aplicaciones<ArrowRight size={16} /></button
      >
      <div class="remove-section">
        {#if confirmRemoval}<p>
            ¿Quitar este servidor de Nodes? Se borrarán su registro e histórico locales. Sus
            aplicaciones seguirán funcionando.
          </p>
          <div class="button-row">
            <button class="danger" disabled={removing} on:click={removeServer}
              >{removing ? 'Quitando…' : 'Quitar del inventario'}</button
            ><button class="secondary" on:click={() => (confirmRemoval = false)}>Cancelar</button>
          </div>{:else}<button
            class="text-button danger-text"
            on:click={() => (confirmRemoval = true)}
            ><Trash2 size={15} />Quitar del inventario</button
          >{/if}{#if formError}<p class="notice error" role="alert">{formError}</p>{/if}
      </div>
    </dialog>{/if}
{/if}
