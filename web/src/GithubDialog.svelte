<script>
  import { onMount, tick } from 'svelte';
  import {
    X,
    GitBranch as Github,
    Plus,
    LoaderCircle,
    CheckCircle2,
    ExternalLink,
    RefreshCw,
    Settings2,
    ChevronDown,
  } from '@lucide/svelte';
  import { modal } from './format.js';
  import GithubRepositoryPicker from './GithubRepositoryPicker.svelte';
  import GithubPushSetup from './GithubPushSetup.svelte';
  export let servers;
  export let sourceId = null;
  export let updatesOnly = false;
  export let api;
  export let onClose;
  export let onStarted;
  let connection = null,
    token = '',
    error = '',
    notice = '',
    pending = false,
    loading = true;
  let deployments = [],
    repositories = [],
    page = 1,
    hasMore = false,
    runs = {},
    runErrors = {};
  let repositoriesLoading = false,
    repositoriesError = '',
    repositoriesVersion = 0,
    repositoriesAt = 0;
  let updatingToken = false,
    checkingRepository = false,
    connectionVersion = 0;
  let creating = false,
    serverId = sourceId || servers[0]?.id || '',
    name = '',
    repository = '',
    branch = 'main',
    buildPath = '.',
    composeFile = '';
  let service = '',
    automaticPush = true,
    port = '',
    hostPort = 8080,
    access = 'auto',
    domain = '',
    secure = true;
  let codeOpen = false,
    runtimeOpen = false,
    optionsLoading = false,
    lastGeneratedName = '',
    preparedId = '',
    reviewPanel;
  let environment = [],
    options = null,
    optionsKey = '',
    optionsVersion = 0;
  let plan = null,
    job = null,
    jobId = '',
    alive = false,
    polling = false;
  $: if (alive && creating && String(serverId) !== optionsKey) loadOptions();
  $: canReview = !!repository && !!serverId && !pending && !checkingRepository && !optionsLoading;

  async function load() {
    loading = true;
    try {
      const [state, items] = await Promise.all([
        api('/github/connection'),
        api('/github/deployments'),
      ]);
      if (!alive) return;
      connection = state;
      deployments = items;
      if (!updatesOnly && state.connected && !items.length) creating = true;
      if (!updatesOnly && state.connected) await loadRepositories();
    } catch (e) {
      if (alive) error = e.message;
    } finally {
      if (alive) loading = false;
    }
  }
  async function loadRepositories(more = false) {
    if (repositoriesLoading) return;
    const version = ++repositoriesVersion;
    const next = more ? page + 1 : 1;
    repositoriesLoading = true;
    repositoriesAt = Date.now();
    repositoriesError = '';
    try {
      const data = await api(`/github/repositories?page=${next}`);
      if (!alive || version !== repositoriesVersion) return;
      const selected = repositories.find((item) => item.full_name === repository);
      const rows = more ? [...repositories, ...data.items] : [...data.items];
      if (selected && !rows.some((item) => item.full_name === repository)) rows.push(selected);
      repositories = [...new Map(rows.map((item) => [item.full_name, item])).values()];
      hasMore = data.hasMore;
      page = next;
      repositoriesAt = Date.now();
    } catch (e) {
      if (alive && version === repositoriesVersion) repositoriesError = e.message;
    } finally {
      if (version === repositoriesVersion) repositoriesLoading = false;
    }
  }
  function newApplication() {
    creating = true;
    job = null;
    notice = '';
    loadRepositories();
  }
  function refreshOnReturn() {
    if (
      alive &&
      connection?.connected &&
      creating &&
      !document.hidden &&
      !pending &&
      !plan &&
      Date.now() - repositoriesAt > 10000
    )
      loadRepositories();
  }
  async function checkRepository(input = '') {
    const version = connectionVersion;
    checkingRepository = true;
    repositoriesError = '';
    try {
      const name = input
        .trim()
        .replace(/^https:\/\/github\.com\//i, '')
        .replace(/\/$/, '')
        .replace(/\.git$/, '');
      const item = await api(`/github/repository?name=${encodeURIComponent(name)}`);
      if (!alive || version !== connectionVersion) return;
      repositories = [item, ...repositories.filter((repo) => repo.full_name !== item.full_name)];
      selectRepository(item.full_name);
      notice = 'Repositorio añadido.';
    } catch (e) {
      if (alive && version === connectionVersion) repositoriesError = e.message;
    } finally {
      if (alive && version === connectionVersion) checkingRepository = false;
    }
  }
  async function connect(event) {
    event.preventDefault();
    pending = true;
    error = '';
    try {
      connection = await api('/github/connection', {
        method: 'POST',
        body: JSON.stringify({ token }),
      });
      connectionVersion++;
      checkingRepository = false;
      notice = '';
      repositoriesVersion++;
      repositoriesLoading = false;
      repositories = [];
      repository = '';
      composeFile = '';
      if (!updatesOnly && !deployments.length) creating = true;
      updatingToken = false;
      token = '';
      if (!updatesOnly) await loadRepositories();
    } catch (e) {
      error = e.message;
    } finally {
      token = '';
      pending = false;
    }
  }
  async function disconnect() {
    pending = true;
    error = '';
    try {
      await api('/github/connection', { method: 'DELETE' });
      connectionVersion++;
      checkingRepository = false;
      repositoriesVersion++;
      repositoriesLoading = false;
      repositoriesError = '';
      connection = { connected: false };
      repositories = [];
      notice =
        'Token retirado de Nodes. Los workflows por push continúan en GitHub; puedes desactivarlos desde Actions.';
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  function selectRepository(next = repository) {
    if (next !== repository) composeFile = '';
    repository = next;
    const item = repositories.find((r) => r.full_name === repository);
    branch = item?.default_branch || 'main';
    if (!name || name === lastGeneratedName) {
      let generated =
        repository
          .split('/')[1]
          ?.toLowerCase()
          .replace(/[^a-z0-9-]/g, '-')
          .slice(0, 40) || '';
      if (!/^[a-z]/.test(generated)) generated = 'app-' + generated;
      if (generated.length < 2) generated += '-app';
      name = generated.slice(0, 40);
      lastGeneratedName = name;
    }
  }
  async function loadOptions() {
    optionsKey = String(serverId);
    options = null;
    optionsLoading = true;
    const version = ++optionsVersion;
    try {
      const data = await api(`/servers/${serverId}/deploy/options`);
      if (alive && version === optionsVersion) {
        options = data;
        if (!data.available) access = 'port';
        secure = data.https;
      }
    } catch (e) {
      if (alive && version === optionsVersion) {
        error = e.message;
        access = 'port';
      }
    } finally {
      if (alive && version === optionsVersion) optionsLoading = false;
    }
  }
  async function review(event) {
    event.preventDefault();
    if (!canReview) return;
    if (!/^[a-z][a-z0-9-]{1,39}$/.test(name) || !branch.trim() || !buildPath.trim()) {
      codeOpen = true;
      error = 'Completa el nombre, la rama y la carpeta en Código y nombre.';
      return;
    }
    pending = true;
    error = '';
    notice = '';
    job = null;
    try {
      plan = await api(`/servers/${serverId}/github/plan`, {
        method: 'POST',
        signal: AbortSignal.timeout(150000),
        body: JSON.stringify({
          name,
          repository,
          branch,
          buildPath,
          composeFile: composeFile.trim(),
          service,
          port: Number(port),
          hostPort: Number(hostPort),
          automaticPush,
          access,
          domain,
          https: secure,
          environment: environment
            .filter((e) => e.name || e.value)
            .map((e) => ({ name: e.name.trim(), value: e.value })),
        }),
      });
      await tick();
      reviewPanel?.scrollIntoView({ block: 'start' });
    } catch (e) {
      error = e.message;
      if (/servicio público|puerto interno|puertos/i.test(e.message)) runtimeOpen = true;
      if (/nombre|rama|carpeta|Dockerfile|Compose/i.test(e.message)) codeOpen = true;
    } finally {
      pending = false;
    }
  }
  async function discard() {
    if (plan && !jobId) await api(`/operations/${plan.id}/plan`, { method: 'DELETE' });
    plan = null;
    error = '';
  }
  async function execute() {
    pending = true;
    error = '';
    try {
      const data = await api(`/operations/${plan.id}/execute`, {
        method: 'POST',
        body: JSON.stringify({ name: plan.name, acknowledged: true }),
      });
      jobId = data.id;
      preparedId = plan.kind === 'github-setup' ? plan.id : '';
      job = { status: 'running', message: 'Comprobando la configuración…' };
      environment = [];
      onStarted();
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function poll() {
    if (!alive || polling || job?.status !== 'running') return;
    polling = true;
    try {
      const items = await api('/operations');
      if (!alive) return;
      job = items.find((i) => i.id === jobId) || job;
      if (job.status !== 'running') {
        deployments = await api('/github/deployments');
        creating = false;
        plan = null;
        jobId = '';
        onStarted();
      }
    } catch {
      if (alive) error = 'No se pudo leer el progreso. Revisa Actividad antes de reintentar.';
    } finally {
      polling = false;
    }
  }
  async function deploy(item) {
    pending = true;
    error = '';
    job = null;
    notice = '';
    try {
      plan = await api(`/github/deployments/${item.id}/deploy`, {
        method: 'POST',
        signal: AbortSignal.timeout(150000),
      });
      await tick();
      reviewPanel?.scrollIntoView({ block: 'start' });
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function activatePush(item) {
    pending = true;
    error = '';
    notice = '';
    job = null;
    try {
      plan = await api(`/github/deployments/${item.id}/push/plan`, {
        method: 'POST',
        signal: AbortSignal.timeout(150000),
      });
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function publish(item) {
    pending = true;
    error = '';
    try {
      await api(`/github/deployments/${item.id}/publish`, { method: 'POST' });
      notice =
        'Workflow publicado. Completa los secretos SSH para permitir los despliegues por push.';
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function refreshRuns(item) {
    runErrors = { ...runErrors, [item.id]: '' };
    try {
      const data = await api(`/github/deployments/${item.id}/runs`);
      if (alive) runs = { ...runs, [item.id]: data };
    } catch (e) {
      if (alive) runErrors = { ...runErrors, [item.id]: e.message };
    }
  }
  async function close(event) {
    event?.preventDefault();
    if (pending) return;
    if (!jobId) {
      try {
        await discard();
      } catch {
        /* Review expires automatically. */
      }
    }
    token = '';
    environment = [];
    onClose();
  }
  onMount(() => {
    alive = true;
    load();
    const timer = setInterval(poll, 2500);
    window.addEventListener('focus', refreshOnReturn);
    document.addEventListener('visibilitychange', refreshOnReturn);
    return () => {
      alive = false;
      optionsVersion++;
      connectionVersion++;
      repositoriesVersion++;
      token = '';
      environment = [];
      clearInterval(timer);
      window.removeEventListener('focus', refreshOnReturn);
      document.removeEventListener('visibilitychange', refreshOnReturn);
    };
  });
</script>

<dialog
  use:modal
  class="form-dialog deployment-dialog github-dialog github-flow"
  aria-labelledby="github-title"
  on:cancel={close}
>
  <header class="gh-flow-header">
    <div>
      <h2 id="github-title">
        <Github size={20} />{plan
          ? plan.kind === 'github-push'
            ? 'Activar despliegue automático'
            : 'Revisar despliegue'
          : creating
            ? 'Desplegar desde GitHub'
            : updatesOnly ? 'Actualizaciones de GitHub' : 'GitHub'}
      </h2>
    </div>
    <button class="icon-button" disabled={pending} on:click={close} aria-label="Cerrar GitHub"
      ><X size={20} /></button
    >
  </header>
  {#if loading}<p class="small muted" role="status">Cargando GitHub…</p>
  {:else if !connection?.connected || updatingToken}
    <form on:submit={connect} class="gh-connect-form">
      <p class="small muted">
        {updatingToken
          ? 'Reemplaza el token sin perder tus aplicaciones.'
          : 'Conecta tu cuenta una sola vez.'}
      </p>
      <label class="field"
        >Token personal de GitHub<input
          type="password"
          bind:value={token}
          autocomplete="off"
          required
          minlength="16"
          maxlength="2048"
        /></label
      >
      <details class="gh-inline-help">
        <summary>Qué permisos necesita el token</summary>
        <p>
          Incluye los repositorios que usarás. Contents y Workflows: lectura y escritura. Actions:
          lectura. Secrets: lectura y escritura para configurar los pushes automáticamente. Si tu organización lo exige, autoriza el token allí. Nodes lo guarda cifrado.
        </p>
      </details>
      <div class="button-row">
        <button class="primary" disabled={pending}
          >{pending ? 'Conectando…' : updatingToken ? 'Guardar token' : 'Conectar GitHub'}</button
        >{#if updatingToken}<button
            type="button"
            class="secondary"
            disabled={pending}
            on:click={() => {
              updatingToken = false;
              token = '';
              error = '';
            }}>Cancelar</button
          >{/if}<a
          class="text-button"
          href="https://github.com/settings/personal-access-tokens/new"
          target="_blank"
          rel="noopener noreferrer">Crear token<ExternalLink size={14} /></a
        >
      </div>
    </form>
  {:else}
    <div class="gh-flow-context">
      <span><i></i>{connection.login}</span>
      <details class="gh-account-settings">
        <summary><Settings2 size={15} />Conexión<ChevronDown size={12} /></summary>
        <div>
          <button
            type="button"
            class="text-button"
            disabled={pending || !!plan || job?.status === 'running'}
            on:click={() => {
              updatingToken = true;
              token = '';
              error = '';
              notice = '';
            }}>Actualizar token</button
          ><button
            type="button"
            class="text-button"
            disabled={pending || !!plan || job?.status === 'running'}
            on:click={disconnect}>Desconectar</button
          >
        </div>
      </details>
    </div>
    {#if job}<div
        class="gh-job-status"
        class:failed={job.status === 'failed' || job.status === 'interrupted'}
        role="status"
      >
        {#if job.status === 'running'}<LoaderCircle
            class="spinner"
            size={18}
          />{:else if job.status === 'succeeded'}<CheckCircle2 size={18} />{/if}
        <div>
          <strong
            >{job.status === 'running'
              ? 'Preparando despliegue…'
              : job.status === 'succeeded'
                ? 'Operación completada'
                : 'Revisa el resultado'}</strong
          >
          <p>{job.message}</p>
          {#if job.status === 'running'}<small
              >Mantén Nodes abierto. Puedes seguir el progreso en Actividad.</small
            >{/if}
        </div>
      </div>{/if}

    {#if !creating && !plan && job?.status !== 'running'}
      <div class="gh-applications-heading">
        <h3>Aplicaciones</h3>
        <button class="primary" disabled={pending} on:click={newApplication}
          ><Plus size={15} />Nueva aplicación</button
        >
      </div>
      {#each deployments as item (item.id)}
        <article class="github-application">
          <div class="gh-app-heading">
            <div>
              <h3>{item.name}</h3>
              <p>{item.repository} · {item.branch}</p>
              <small
                >{servers.find((server) => server.id === item.serverId)?.name || 'Servidor'}</small
              >
            </div>
            {#if item.url}<a
                class="icon-button"
                href={item.url}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={`Abrir ${item.name}`}
                title="Abrir aplicación"><ExternalLink size={17} /></a
              >{/if}
          </div>
          <div class="button-row">
            <button class="secondary" disabled={pending} on:click={() => deploy(item)}
              >{updatesOnly ? 'Revisar actualización' : 'Desplegar ahora'}</button
            ><button class="text-button" disabled={pending} on:click={() => refreshRuns(item)}
              ><RefreshCw size={14} />Ver actividad</button
            >
          </div>
          {#if runErrors[item.id]}<p class="notice error" role="alert">{runErrors[item.id]}</p>{/if}
          {#if runs[item.id]}<div class="github-runs">
              {#each runs[item.id] as run}<a
                  href={run.html_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  ><code>{run.head_sha.slice(0, 8)}</code><span>{run.conclusion || run.status}</span
                  ><time>{new Date(run.created_at).toLocaleString('es-PE')}</time><ExternalLink
                    size={13}
                  /></a
                >{:else}<p class="small muted">
                  Aún no hay ejecuciones. Completa la configuración del push y envía un commit.
                </p>{/each}
            </div>{/if}
          {#if !updatesOnly}<GithubPushSetup
            deployment={item}
            server={servers.find((server) => server.id === item.serverId)}
            open={item.id === preparedId}
            {pending}
            onPublish={publish}
            onActivate={activatePush}
          />{/if}
        </article>
      {/each}
      {#if !deployments.length}<p class="small muted">
          {updatesOnly
            ? 'Aún no hay aplicaciones vinculadas a GitHub. Usa Nueva aplicación para preparar la primera.'
            : 'Conecta un repositorio para tu primera aplicación.'}
        </p>{/if}
      {#if repositoriesError}<p class="notice error" role="alert">{repositoriesError}</p>{/if}
    {/if}

    {#if creating && !plan && job?.status !== 'running'}
      <form on:submit={review} class="gh-deploy-form">
        <fieldset disabled={pending}>
          <GithubRepositoryPicker
            {repositories}
            value={repository}
            loading={repositoriesLoading || checkingRepository}
            error={repositoriesError}
            {hasMore}
            onSelect={selectRepository}
            onRefresh={() => loadRepositories()}
            onMore={() => loadRepositories(true)}
            onLookup={checkRepository}
          />
          <label class="field"
            >Servidor de destino<select bind:value={serverId} required
              >{#each servers as server}<option value={server.id}>{server.name}</option
                >{/each}</select
            ></label
          >
          <section class="gh-access">
            <div class="gh-field-heading">
              <span>Acceso público</span>{#if optionsLoading}<small role="status"
                  >Comprobando servidor…</small
                >{/if}
            </div>
            <div class="access-choices" role="group" aria-label="Acceso de la aplicación">
              {#each [{ value: 'auto', label: 'Enlace automático' }, { value: 'custom', label: 'Mi dominio' }, { value: 'port', label: 'IP y puerto' }] as choice}<button
                  type="button"
                  class:active={access === choice.value}
                  disabled={optionsLoading || (choice.value !== 'port' && !options?.available)}
                  on:click={() => (access = choice.value)}>{choice.label}</button
                >{/each}
            </div>
            {#if options && !options.available}<p class="gh-field-hint">
                Sin proxy compatible detectado. Disponible: IP y puerto.
              </p>{/if}
            {#if access === 'custom'}<label class="field"
                >Dominio<input
                  bind:value={domain}
                  placeholder="app.midominio.com"
                  required
                /></label
              >
              <p class="gh-field-hint">
                DNS A → {options?.publicIp || 'IP de la VPS'}
              </p>{:else if access === 'port'}<label class="field"
                >Puerto del servidor<input
                  type="number"
                  bind:value={hostPort}
                  min="1"
                  max="65535"
                  required
                /></label
              >
              <p class="gh-field-hint">
                Se usa con Dockerfile. Compose conserva los puertos de su archivo.
              </p>{:else}<p class="gh-field-hint">Se generará un enlace para la aplicación.</p>{/if}
            {#if access !== 'port'}<label class="deploy-checkbox"
                ><input
                  type="checkbox"
                  bind:checked={secure}
                  disabled={!options?.https}
                />HTTPS</label
              >{/if}
          </section>
          <details class="gh-form-options">
            <summary
              >Variables de entorno <span
                >{environment.length ? environment.length : 'Opcional'}</span
              ></summary
            >
            <div>
              {#each environment as variable, index}<div class="environment-row">
                  <input
                    aria-label={`Nombre de variable ${index + 1}`}
                    bind:value={variable.name}
                    placeholder="API_KEY"
                    autocomplete="off"
                  /><input
                    type="password"
                    aria-label={`Valor de variable ${index + 1}`}
                    bind:value={variable.value}
                    placeholder="Valor"
                    autocomplete="off"
                  /><button
                    type="button"
                    class="icon-button"
                    aria-label="Quitar variable"
                    on:click={() => (environment = environment.filter((_, i) => i !== index))}
                    ><X size={14} /></button
                  >
                </div>{/each}<button
                type="button"
                class="text-button"
                on:click={() => (environment = [...environment, { name: '', value: '' }])}
                ><Plus size={14} />Añadir variable</button
              >
              <p class="gh-field-hint">
                Se guardan fuera del código y del contexto de construcción.
              </p>
            </div>
          </details>
          <details class="gh-form-options" bind:open={codeOpen}>
            <summary
              >Código y nombre <span>{branch} · {buildPath === '.' ? 'Raíz' : buildPath}</span
              ></summary
            >
            <div>
              <label class="field"
                >Nombre de la aplicación<input
                  bind:value={name}
                  minlength="2"
                  maxlength="40"
                  on:invalid={() => (codeOpen = true)}
                /></label
              >
              <div class="form-pair">
                <label class="field">Rama<input bind:value={branch} maxlength="200" /></label><label
                  class="field"
                  >Carpeta del proyecto<input
                    bind:value={buildPath}
                    placeholder=". o apps/web"
                  /></label
                >
              </div>
              <label class="field">
                Archivo Compose
                <input bind:value={composeFile} maxlength="120" placeholder="Detección automática" />
              </label>
              <p class="gh-field-hint">
                Si el repositorio tiene varias configuraciones, indica el nombre del archivo de
                despliegue en la carpeta elegida. Usa un Compose completo con redes propias del proyecto.
              </p>
            </div>
          </details>
          <details class="gh-form-options" bind:open={runtimeOpen}>
            <summary>Ajustes avanzados <span>Servicio y puerto</span></summary>
            <div>
              <div class="form-pair">
                <label class="field"
                  >Servicio público<input
                    bind:value={service}
                    placeholder="Detectar del Compose"
                  /></label
                ><label class="field"
                  >Puerto interno<input
                    type="number"
                    bind:value={port}
                    on:invalid={() => (runtimeOpen = true)}
                    min="1"
                    max="65535"
                    placeholder="Automático desde Compose o Dockerfile"
                  /></label
                >
              </div>
              <p class="gh-field-hint">
                Déjalos vacíos para detectar el servicio y su puerto interno desde Compose o
                Dockerfile. Si hay varios puertos posibles, tendrás que indicar el correcto.
              </p>
              <details class="gh-inline-help">
                <summary>Compatibilidad y datos</summary>
                <p>
                  Incluye un Compose principal o Dockerfile. No se admiten submódulos, Git LFS ni
                  enlaces simbólicos. Los datos persistentes deben ir en volúmenes Docker; las
                  carpetas relativas pertenecen a cada versión del código.
                </p>
              </details>
              <label class="deploy-checkbox"
                ><input type="checkbox" bind:checked={automaticPush} /><span
                  >Configurar automáticamente el despliegue por push</span
                ></label
              >
              <p class="small muted">
                Se creará una clave exclusiva para Actions. El token necesita permiso Secrets:
                lectura y escritura.
              </p>
            </div>
          </details>
        </fieldset>
        {#if error}<p class="notice error" role="alert">{error}</p>{/if}
        <footer class="gh-flow-footer">
          <span>Primero revisarás los cambios.</span>
          <div>
            <button class="primary" disabled={!canReview}
              >{pending ? 'Revisando…' : 'Revisar despliegue'}</button
            ><button
              type="button"
              class="text-button"
              disabled={pending}
              on:click={() => {
                creating = false;
                error = '';
              }}>Volver</button
            >
          </div>
        </footer>
      </form>
    {/if}

    {#if plan && !jobId}<section class="gh-review" aria-live="polite" bind:this={reviewPanel}>
        <p class="gh-field-hint">
          {plan.kind === 'github-setup' ? 'Preparación inicial · 1 de 2' : 'Despliegue manual'}
        </p>
        <h3>{plan.name}</h3>
        <dl class="gh-review-facts">
          <div>
            <dt>Servidor</dt>
            <dd>{plan.source}</dd>
          </div>
          {#if plan.kind === 'github-setup'}<div>
              <dt>Repositorio</dt>
              <dd>{repository}</dd>
            </div>
            <div>
              <dt>Rama</dt>
              <dd>{branch}</dd>
            </div>
            <div>
              <dt>Acceso</dt>
              <dd>{plan.url || 'IP y puertos del proyecto'}</dd>
            </div>{/if}
          {#if plan.environmentKeys?.length}<div>
              <dt>Variables</dt>
              <dd>{plan.environmentKeys.join(', ')}</dd>
            </div>{/if}
        </dl>
        <ul class="gh-review-summary">
          {#each plan.review?.summary || plan.summary as line}<li>{line}</li>{/each}
        </ul>
        {#if plan.kind === 'github-setup'}<p class="gh-next-step">
            {automaticPush
              ? 'Los secretos SSH se configurarán automáticamente antes de publicar el workflow.'
              : 'Después: añade los secretos SSH en GitHub para activar los pushes.'}
          </p>{/if}
        {#if plan.dns}<p class="small" class:warning={!plan.dns.ready}>{plan.dns.message}</p>{/if}
        {#each plan.blockers as blocker}<p class="notice error" role="alert">{blocker}</p>{/each}
        <ul class="gh-review-warnings">
          {#each plan.review?.warnings || plan.warnings as warning}<li>{warning}</li>{/each}
        </ul>
        <details class="gh-form-options">
          <summary>Detalles técnicos y workflow</summary>
          <div>
            <ul>
              {#each plan.summary as line}<li>{line}</li>{/each}
            </ul>
            {#each plan.warnings as warning}<p>
                {warning}
              </p>{/each}{#if plan.workflow}<pre>{plan.workflow}</pre>{/if}
          </div>
        </details>
        {#if error}<p class="notice error" role="alert">{error}</p>{/if}
        <footer class="gh-flow-footer">
          <span
            >{plan.kind === 'github-setup'
              ? 'Se preparará la VPS y se publicará el workflow.'
              : plan.kind === 'github-push'
                ? 'Se configurarán los secretos y el acceso para los pushes.'
                : 'Se actualizará la aplicación en el servidor.'}</span
          >
          <div>
            <button class="primary" disabled={pending || !!plan.blockers.length} on:click={execute}
              >{pending
                ? 'Iniciando…'
                : plan.kind === 'github-setup'
                  ? 'Conectar y preparar'
                  : plan.kind === 'github-push'
                    ? 'Activar despliegue automático'
                    : 'Desplegar commit'}</button
            ><button
              type="button"
              class="text-button"
              disabled={pending}
              on:click={async () => {
                try {
                  await discard();
                } catch (e) {
                  error = e.message;
                }
              }}>Editar</button
            >
          </div>
        </footer>
      </section>{/if}
  {/if}
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}
  {#if error && (!creating || !connection?.connected || updatingToken) && !plan}<p
      class="notice error"
      role="alert"
    >
      {error}
    </p>{/if}
</dialog>
