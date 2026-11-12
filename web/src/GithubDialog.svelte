<script>
  import { onMount } from 'svelte';
  import {
    X,
    GitBranch as Github,
    Plus,
    LoaderCircle,
    CheckCircle2,
    ExternalLink,
    RefreshCw,
  } from '@lucide/svelte';
  import { modal } from './format.js';
  export let servers;
  export let sourceId = null;
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
  let creating = false,
    serverId = sourceId || servers[0]?.id || '',
    name = '',
    repository = '',
    branch = 'main',
    buildPath = '.';
  let service = '',
    port = 80,
    hostPort = 8080,
    access = 'port',
    domain = '',
    secure = false;
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
      if (state.connected) await loadRepositories();
    } catch (e) {
      if (alive) error = e.message;
    } finally {
      if (alive) loading = false;
    }
  }
  async function loadRepositories(more = false) {
    const next = more ? page + 1 : 1;
    const data = await api(`/github/repositories?page=${next}`);
    if (!alive) return;
    repositories = more ? [...repositories, ...data.items] : data.items;
    hasMore = data.hasMore;
    page = next;
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
      token = '';
      await loadRepositories();
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
  function selectRepository() {
    const item = repositories.find((r) => r.full_name === repository);
    branch = item?.default_branch || 'main';
    if (!name)
      name =
        repository
          .split('/')[1]
          ?.toLowerCase()
          .replace(/[^a-z0-9-]/g, '-')
          .slice(0, 40) || '';
  }
  async function loadOptions() {
    optionsKey = String(serverId);
    options = null;
    const version = ++optionsVersion;
    try {
      const data = await api(`/servers/${serverId}/deploy/options`);
      if (alive && version === optionsVersion) {
        options = data;
        if (!data.available) access = 'port';
      }
    } catch (e) {
      if (alive && version === optionsVersion) {
        error = e.message;
        access = 'port';
      }
    }
  }
  async function review(event) {
    event.preventDefault();
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
          service,
          port: Number(port),
          hostPort: Number(hostPort),
          access,
          domain,
          https: secure,
          environment: environment
            .filter((e) => e.name || e.value)
            .map((e) => ({ name: e.name.trim(), value: e.value })),
        }),
      });
    } catch (e) {
      error = e.message;
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
      plan = await api(`/github/deployments/${item.id}/deploy`, { method: 'POST' });
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
        'Workflow publicado. Los siguientes pushes a la rama configurada iniciarán el despliegue.';
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
  const secrets = [
    [
      'HOST',
      'IP pública o dominio de la VPS, accesible desde GitHub. Los alias SSH de tu PC no funcionan aquí.',
    ],
    ['PORT', 'Puerto SSH de la VPS (normalmente 22).'],
    ['USER', 'El mismo usuario SSH que conectaste a Nodes.'],
    [
      'SSH_KEY',
      'Clave privada dedicada a estos despliegues, sin contraseña. Autoriza su clave pública en la VPS.',
    ],
    [
      'KNOWN_HOSTS',
      'Línea de known_hosts cuya huella hayas verificado. Para puertos distintos de 22, usa [host]:puerto.',
    ],
  ];
  onMount(() => {
    alive = true;
    load();
    const timer = setInterval(poll, 2500);
    return () => {
      alive = false;
      optionsVersion++;
      token = '';
      environment = [];
      clearInterval(timer);
    };
  });
</script>

<dialog
  use:modal
  class="form-dialog deployment-dialog github-dialog"
  aria-labelledby="github-title"
  on:cancel={close}
>
  <header>
    <div>
      <span class="eyebrow">DESPLIEGUES POR PUSH</span>
      <h2 id="github-title"><Github size={22} /> GitHub</h2>
    </div>
    <button class="icon-button" disabled={pending} on:click={close} aria-label="Cerrar GitHub"
      ><X size={20} /></button
    >
  </header>
  <p class="small muted">
    Despliega en tus VPS con Docker Compose. Los pushes se ejecutan con GitHub Actions, incluso con
    Nodes cerrado.
  </p>
  {#if loading}<p aria-live="polite">Cargando conexión…</p>
  {:else if !connection?.connected}
    <form on:submit={connect}>
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
      <p class="small muted">
        Crea un token de acceso granular para los repositorios elegidos: Contents y Workflows con
        escritura, y Actions con lectura. Si la organización exige autorización, apruébala en
        GitHub. Nodes lo cifra con protección de tu cuenta de Windows.
      </p>
      <div class="button-row">
        <button class="primary" disabled={pending}
          >{pending ? 'Conectando…' : 'Conectar GitHub'}</button
        >
        <a
          class="text-button"
          href="https://github.com/settings/personal-access-tokens/new"
          target="_blank"
          rel="noopener noreferrer">Crear token <ExternalLink size={14} /></a
        >
      </div>
    </form>
  {:else}
    <div class="section-heading">
      <span class="small">Conectado como <strong>{connection.login}</strong></span><button
        class="text-button"
        disabled={pending || !!plan || job?.status === 'running'}
        on:click={disconnect}>Desconectar</button
      >
    </div>
    {#if job}<div
        class="notice"
        class:error={job.status === 'failed' || job.status === 'interrupted'}
        aria-live="polite"
      >
        {#if job.status === 'running'}<LoaderCircle
            class="spinner"
            size={16}
          />{:else if job.status === 'succeeded'}<CheckCircle2 size={16} />{/if}
        {job.message}
        {#if job.status === 'running'}<p class="small">
            Mantén Nodes abierto durante esta operación. Los despliegues automáticos posteriores se
            ejecutan en GitHub.
          </p>{/if}
      </div>{/if}
    {#if !creating && !plan && job?.status !== 'running'}
      <div class="deploy-section">
        <div class="section-heading">
          <h3>Aplicaciones conectadas</h3>
          <button
            class="secondary"
            disabled={pending}
            on:click={() => {
              creating = true;
              job = null;
              notice = '';
            }}><Plus size={15} /> Nueva aplicación</button
          >
        </div>
        {#if !deployments.length}<p class="small muted">
            Conecta un repositorio para preparar tu primer despliegue.
          </p>{/if}
        {#each deployments as item (item.id)}
          <article class="github-application">
            <h3>{item.name}</h3>
            <p class="small muted">
              {item.repository} · {item.branch} · {servers.find((s) => s.id === item.serverId)
                ?.name || 'Servidor'}
            </p>
            <div class="button-row">
              <button class="primary" disabled={pending} on:click={() => deploy(item)}
                >Desplegar última versión</button
              >
              <button class="secondary" disabled={pending} on:click={() => refreshRuns(item)}
                ><RefreshCw size={14} /> Ejecuciones por push</button
              >
              {#if item.url}<a
                  class="text-button"
                  href={item.url}
                  target="_blank"
                  rel="noopener noreferrer">Abrir aplicación <ExternalLink size={14} /></a
                >{/if}
            </div>
            <details class="github-instructions">
              <summary>Activar despliegue automático · secretos SSH</summary>
              <p class="small">
                En el repositorio → Settings → Secrets and variables → Actions, añade estos cinco
                secretos. El usuario y la VPS deben coincidir con la conexión de Nodes. Usa una clave
                dedicada; Nodes no copia tu clave privada a GitHub.
              </p>
              <dl>
                {#each secrets as [suffix, description]}<dt>
                    <code>{item.secretPrefix}_{suffix}</code>
                  </dt>
                  <dd>{description}</dd>{/each}
              </dl>
              <p class="small muted">
                Verifica la huella del servidor por un canal de confianza antes de guardar
                KNOWN_HOSTS. La VPS debe aceptar conexiones SSH desde los runners de GitHub. Los
                minutos de Actions están sujetos a tu plan de GitHub.
              </p>
              <p class="small">
                Workflow: <code>.github/workflows/{item.workflow}</code>. Escucha únicamente los
                pushes a <strong>{item.branch}</strong>. Para detenerlos, desactiva ese workflow en
                Actions.
              </p>
              <button class="secondary" disabled={pending} on:click={() => publish(item)}
                >Publicar workflow pendiente</button
              >
              <a
                class="text-button"
                href={`https://github.com/${item.repository}/settings/secrets/actions`}
                target="_blank"
                rel="noopener noreferrer">Configurar secretos <ExternalLink size={14} /></a
              >
            </details>
            {#if runErrors[item.id]}<p class="notice error">{runErrors[item.id]}</p>{/if}
            {#if runs[item.id]}<div class="github-runs">
                <p class="small muted">
                  GitHub Actions · los despliegues manuales de Nodes aparecen en Actividad.
                </p>
                {#each runs[item.id] as run}<a
                    href={run.html_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    ><code>{run.head_sha.slice(0, 8)}</code><span
                      >{run.conclusion || run.status}</span
                    ><time>{new Date(run.created_at).toLocaleString('es-PE')}</time><ExternalLink
                      size={13}
                    /></a
                  >{:else}<p class="small muted">
                    Aún no hay ejecuciones. Comprueba que el workflow esté publicado y haz push a la
                    rama configurada.
                  </p>{/each}
              </div>{/if}
          </article>
        {/each}
      </div>
    {/if}
    {#if creating && !plan && job?.status !== 'running'}
      <form on:submit={review} class="deployment-form">
        <fieldset disabled={pending}>
          <div class="form-pair">
            <label class="field"
              >Nombre<input
                bind:value={name}
                required
                minlength="2"
                maxlength="40"
                pattern="[a-z][a-z0-9-]{(1, 39)}"
              /></label
            >
            <label class="field"
              >Servidor<select bind:value={serverId} required
                >{#each servers as server}<option value={server.id}>{server.name}</option
                  >{/each}</select
              ></label
            >
          </div>
          <label class="field"
            >Repositorio<select bind:value={repository} on:change={selectRepository} required
              ><option value="" disabled>Seleccionar repositorio</option
              >{#each repositories as repo}<option value={repo.full_name}
                  >{repo.full_name}{repo.private ? ' · privado' : ''}</option
                >{/each}</select
            ></label
          >
          {#if hasMore}<button
              type="button"
              class="text-button"
              on:click={async () => {
                try {
                  await loadRepositories(true);
                } catch (e) {
                  error = e.message;
                }
              }}>Cargar más repositorios</button
            >{/if}
          <div class="form-pair">
            <label class="field">Rama<input bind:value={branch} required maxlength="200" /></label
            ><label class="field"
              >Carpeta del proyecto<input
                bind:value={buildPath}
                placeholder=". o apps/web"
                required
              /></label
            >
          </div>
          <p class="small muted">
            Incluye un solo Compose principal o un Dockerfile en esa carpeta. Se revisará el código
            antes de preparar la VPS. No se admiten submódulos, Git LFS ni enlaces simbólicos.
          </p>
          <div class="form-pair">
            <label class="field"
              >Servicio público<input
                bind:value={service}
                placeholder="app · opcional si hay un solo servicio"
              /></label
            ><label class="field"
              >Puerto interno<input
                type="number"
                bind:value={port}
                min="1"
                max="65535"
                required
              /></label
            >
          </div>
          <div class="deploy-section">
            <h3>Acceso</h3>
            <div class="access-choices" role="group" aria-label="Acceso de la aplicación">
              {#each [{ value: 'port', label: 'IP y puerto' }, { value: 'auto', label: 'Automático' }, { value: 'custom', label: 'Mi dominio' }] as choice}<button
                  type="button"
                  class:active={access === choice.value}
                  disabled={choice.value !== 'port' && !options?.available}
                  on:click={() => (access = choice.value)}>{choice.label}</button
                >{/each}
            </div>
            {#if access === 'port'}<label class="field"
                >Puerto del servidor (Dockerfile)<input
                  type="number"
                  bind:value={hostPort}
                  min="1"
                  max="65535"
                  required
                /></label
              >
              <p class="small muted">
                Con Compose se conservan sus puertos publicados. Con Dockerfile se publica el puerto
                elegido.
              </p>
            {:else}
              {#if access === 'custom'}<label class="field"
                  >Dominio<input
                    bind:value={domain}
                    placeholder="app.midominio.com"
                    required
                  /></label
                >
                <p class="small muted">
                  Crea el registro A apuntando a {options?.publicIp || 'la IP de la VPS'}. El DNS se
                  comprobará al revisar.
                </p>{:else}<p class="small muted">
                  Se generará un dominio sslip.io para {options?.publicIp}.
                </p>{/if}
              <label class="deploy-checkbox"
                ><input type="checkbox" bind:checked={secure} disabled={!options?.https} /> HTTPS automático</label
              >
            {/if}
          </div>
          <div class="deploy-section">
            <h3>Variables del servicio público</h3>
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
              </div>{/each}
            <button
              type="button"
              class="text-button"
              on:click={() => (environment = [...environment, { name: '', value: '' }])}
              ><Plus size={14} /> Añadir variable</button
            >
            <p class="small muted">
              Se guardan en una carpeta privada de la VPS, fuera del código y del contexto de
              construcción. Los datos persistentes deben usar volúmenes Docker; las carpetas
              relativas pertenecen a cada versión del código.
            </p>
          </div>
        </fieldset>
        <div class="button-row">
          <button class="primary" disabled={pending}
            >{pending ? 'Revisando repositorio…' : 'Revisar conexión y despliegue'}</button
          ><button
            type="button"
            class="secondary"
            disabled={pending}
            on:click={() => (creating = false)}>Volver</button
          >
        </div>
      </form>
    {/if}
    {#if plan && !jobId}<div class="deployment-review" aria-live="polite">
        <h3>
          {plan.kind === 'github-setup' ? 'Preparar despliegue por push' : 'Desplegar este commit'}
        </h3>
        {#if plan.url}<p class="planned-url">{plan.url}</p>{/if}
        {#if plan.dns}<p class="notice" class:warning={!plan.dns.ready}>{plan.dns.message}</p>{/if}
        <ul>
          {#each plan.summary as line}<li>{line}</li>{/each}
        </ul>
        {#each plan.blockers as blocker}<p class="notice error">{blocker}</p>{/each}
        {#each plan.warnings as warning}<p class="small muted">{warning}</p>{/each}
        {#if plan.workflow}<details class="github-instructions">
            <summary>Ver el workflow que se publicará</summary>
            <pre>{plan.workflow}</pre>
          </details>{/if}
        <div class="button-row">
          <button class="primary" disabled={pending || !!plan.blockers.length} on:click={execute}
            >{plan.kind === 'github-setup'
              ? 'Preparar VPS y publicar workflow'
              : 'Desplegar commit'}</button
          ><button
            class="secondary"
            disabled={pending}
            on:click={async () => {
              try {
                await discard();
              } catch (e) {
                error = e.message;
              }
            }}>Volver</button
          >
        </div>
      </div>{/if}
  {/if}
  {#if notice}<p class="notice" aria-live="polite">{notice}</p>{/if}
  {#if error}<p class="notice error spaced" role="alert">{error}</p>{/if}
</dialog>
