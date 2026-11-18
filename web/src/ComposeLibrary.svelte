<script>
  import { onMount, tick } from 'svelte';
  import {
    X,
    Search,
    ArrowLeft,
    ArrowUpRight,
    Database,
    Workflow,
    Layers,
    ShieldCheck,
    Eye,
    EyeOff,
    RefreshCw,
    LoaderCircle,
    CheckCircle2,
    BookOpen,
  } from '@lucide/svelte';
  import { modal } from './format.js';
  export let servers;
  export let sourceId = null;
  export let api;
  export let onClose;
  export let onStarted;
  let catalog = [],
    loading = true,
    query = '',
    category = 'Todas';
  let template = null,
    values = {},
    serverId = sourceId || servers[0]?.id || '',
    name = '';
  let access = 'port',
    bind = '127.0.0.1',
    hostPort = 5432,
    domain = '',
    secure = false,
    reveal = false;
  let options = null,
    optionsKey = '',
    optionsVersion = 0,
    optionsLoading = false;
  let error = '',
    pending = false,
    plan = null,
    job = null,
    jobId = '',
    alive = false,
    polling = false;
  let reviewPanel,
    lastSuggestion = '';
  $: categories = ['Todas', ...new Set(catalog.map((item) => item.category))];
  $: filtered = catalog.filter(
    (item) =>
      (category === 'Todas' || item.category === category) &&
      `${item.name} ${item.description}`
        .toLocaleLowerCase()
        .includes(query.toLocaleLowerCase().trim()),
  );
  $: if (alive && template?.web && String(serverId) !== optionsKey) loadOptions();
  $: if (template?.web && access === 'port') suggestURL(serverId, hostPort, bind);
  function suggestURL(id, port, binding) {
    const server = servers.find((item) => item.id === Number(id));
    const host = binding === '127.0.0.1' ? '127.0.0.1' : options?.publicIp || server?.host || '';
    const suggestion = host ? `http://${host.includes(':') ? `[${host}]` : host}:${port}` : '';
    if (!values.PUBLIC_URL || values.PUBLIC_URL === lastSuggestion)
      values = { ...values, PUBLIC_URL: suggestion };
    lastSuggestion = suggestion;
  }
  async function choose(item) {
    pending = true;
    error = '';
    job = null;
    reveal = false;
    values = {};
    lastSuggestion = '';
    try {
      const defaults = await api(`/templates/${item.id}/defaults`, { method: 'POST' });
      if (!alive) return;
      values = defaults;
      optionsVersion++;
      optionsLoading = false;
      options = null;
      name = item.id;
      hostPort = item.hostPort;
      bind = item.web ? '0.0.0.0' : '127.0.0.1';
      access = 'port';
      domain = '';
      secure = false;
      optionsKey = '';
      template = item;
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function loadOptions() {
    optionsKey = String(serverId);
    optionsLoading = true;
    options = null;
    const version = ++optionsVersion;
    try {
      const data = await api(`/servers/${serverId}/deploy/options`);
      if (alive && version === optionsVersion) {
        options = data;
        if (!data.available && (access === 'auto' || access === 'custom')) access = 'port';
        if (!data.https) secure = false;
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
  async function regenerate(field) {
    pending = true;
    error = '';
    try {
      const defaults = await api(`/templates/${template.id}/defaults`, { method: 'POST' });
      if (alive) values = { ...values, [field]: defaults[field] };
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function review(event) {
    event?.preventDefault();
    pending = true;
    error = '';
    try {
      plan = await api(`/servers/${serverId}/templates/${template.id}/plan`, {
        method: 'POST',
        signal: AbortSignal.timeout(150000),
        body: JSON.stringify({
          name,
          values: Object.fromEntries(
            Object.entries(values).map(([key, value]) => [key, String(value ?? '')]),
          ),
          access,
          bind,
          hostPort: Number(hostPort),
          domain,
          https: secure,
        }),
      });
      await tick();
      reviewPanel?.scrollIntoView({ block: 'nearest' });
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
  async function deploy() {
    pending = true;
    error = '';
    try {
      const result = await api(`/operations/${plan.id}/execute`, {
        method: 'POST',
        body: JSON.stringify({ name: plan.name, acknowledged: true }),
      });
      jobId = result.id;
      job = { status: 'running', message: 'Subiendo la plantilla e iniciando los servicios…' };
      values = {};
      reveal = false;
      onStarted();
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function poll() {
    if (!jobId || polling || job?.status !== 'running') return;
    polling = true;
    try {
      const operations = await api('/operations');
      if (alive) {
        job = operations.find((item) => item.id === jobId) || job;
        if (job.status !== 'running') onStarted();
      }
    } catch {
      if (alive) error = 'No se pudo consultar el progreso. Revisa Actividad antes de reintentar.';
    } finally {
      polling = false;
    }
  }
  async function back() {
    try {
      await discard();
      template = null;
      values = {};
      optionsVersion++;
      optionsKey = '';
    } catch (e) {
      error = e.message;
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
    values = {};
    onClose();
  }
  async function recheck() {
    try {
      await discard();
      await review();
    } catch (e) {
      error = e.message;
    }
  }
  onMount(() => {
    alive = true;
    api('/templates')
      .then((items) => {
        if (alive) catalog = items;
      })
      .catch((e) => {
        if (alive) error = e.message;
      })
      .finally(() => {
        if (alive) loading = false;
      });
    const timer = setInterval(poll, 2000);
    return () => {
      alive = false;
      optionsVersion++;
      values = {};
      clearInterval(timer);
    };
  });
</script>

<dialog
  use:modal
  class="form-dialog deployment-dialog compose-library"
  aria-labelledby="library-title"
  on:cancel={close}
>
  <header>
    <div>
      <span class="eyebrow">COMPOSE · BIBLIOTECA DE NODO</span>
      <h2 id="library-title">{template ? template.name : 'Aplicaciones listas para desplegar'}</h2>
    </div>
    <button class="icon-button" disabled={pending} on:click={close} aria-label="Cerrar biblioteca"
      ><X size={20} /></button
    >
  </header>
  {#if job}
    <div class="job-result" aria-live="polite">
      {#if job.status === 'running'}<LoaderCircle
          class="spinner"
          size={28}
        />{:else if job.status === 'succeeded'}<CheckCircle2 size={28} />{/if}
      <h3>
        {job.status === 'running'
          ? 'Desplegando servicios…'
          : job.status === 'succeeded'
            ? 'Aplicación iniciada'
            : 'Revisa el resultado'}
      </h3>
      <p>{job.message}</p>
      {#if job.status === 'running'}<p class="small muted">
          Puedes seguir el progreso en Actividad. Mantén Nodes abierto hasta que termine.
        </p>{/if}
    </div>
    {#if job.status === 'succeeded'}
      {#if plan?.url}<a
          class="primary deployment-link"
          href={plan.url}
          target="_blank"
          rel="noopener noreferrer">Abrir {template.name}<ArrowUpRight size={16} /></a
        >{/if}
      <p class="small muted">
        La configuración privada y los registros están en <code
          >~/.local/share/nodo/deployments/{plan?.name}-{plan?.id.slice(0, 8)}/</code
        >. Guarda las claves y respalda los volúmenes antes de cargar datos importantes.
      </p>
    {/if}
  {:else if !template}
    <p class="muted small">
      Elige una aplicación, configura sus variables y revisa el despliegue en tu VPS.
    </p>
    <label class="search library-search"
      ><Search size={17} /><input
        bind:value={query}
        aria-label="Buscar en la biblioteca"
        placeholder="Buscar aplicación…"
      /></label
    >
    <div class="library-categories" role="group" aria-label="Categoría de plantilla">
      {#each categories as item}<button
          class:active={category === item}
          aria-pressed={category === item}
          on:click={() => (category = item)}>{item}</button
        >{/each}
    </div>
    {#if loading}<p class="small muted">Cargando biblioteca…</p>{:else}
      <div class="template-gallery">
        {#each filtered as item}<button
            class="template-option"
            disabled={pending}
            on:click={() => choose(item)}
          >
            <span
              class="template-symbol"
              class:automation={item.id === 'n8n'}
              class:backend={item.id === 'supabase'}
              >{#if item.id === 'n8n'}<Workflow size={24} />{:else if item.id === 'supabase'}<Layers
                  size={24}
                />{:else}<Database size={24} />{/if}</span
            >
            <span class="template-heading"
              ><strong>{item.name}</strong><small>{item.category} · {item.version}</small></span
            >
            <span class="template-description">{item.description}</span><span
              class="template-select">Configurar <ArrowUpRight size={14} /></span
            >
          </button>{:else}<p class="small muted">
            No se encontraron aplicaciones con esa búsqueda.
          </p>{/each}
      </div>
      <p class="library-footnote">
        <ShieldCheck size={15} />Contraseñas por instalación · Volúmenes persistentes · Despliegue
        por SSH
      </p>
    {/if}
  {:else}
    <button class="text-button library-back" disabled={pending} on:click={back}
      ><ArrowLeft size={14} /> Volver a la biblioteca</button
    >
    <p class="small muted">{template.description}</p>
    <form on:submit={review} class="deployment-form">
      <fieldset disabled={pending || !!plan}>
        <div class="form-pair">
          <label class="field"
            >Nombre de la aplicación<input
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
        <div class="deploy-section">
          <h3>Acceso a la aplicación</h3>
          <div class="access-choices" role="group" aria-label="Tipo de acceso">
            {#if template.web}
              <button
                type="button"
                class:active={access === 'port'}
                on:click={() => (access = 'port')}>IP y puerto</button
              >
              <button
                type="button"
                class:active={access === 'auto'}
                disabled={!options?.available}
                on:click={() => (access = 'auto')}>Dominio automático</button
              >
              <button
                type="button"
                class:active={access === 'custom'}
                disabled={!options?.available}
                on:click={() => (access = 'custom')}>Mi dominio</button
              >
            {:else}
              <button
                type="button"
                class:active={access === 'port'}
                on:click={() => (access = 'port')}>Puerto de la VPS</button
              >
              <button
                type="button"
                class:active={access === 'internal'}
                on:click={() => (access = 'internal')}>Solo red del proyecto</button
              >
            {/if}
          </div>
          {#if access === 'port'}<div class="form-pair">
              <label class="field"
                >Puerto de la VPS<input
                  type="number"
                  bind:value={hostPort}
                  min="1"
                  max="65535"
                  required
                /></label
              ><label class="field"
                >Escuchar en<select bind:value={bind}
                  ><option value="127.0.0.1">Solo VPS · 127.0.0.1</option><option value="0.0.0.0"
                    >Todas las interfaces · público</option
                  ></select
                ></label
              >
            </div>
            {#if bind === '127.0.0.1'}<p class="small muted">
                Accede desde la VPS o mediante un túnel SSH desde tu equipo.
              </p>{:else}<p class="notice warning small">
                Se publicará el puerto en todas las interfaces. Revisa las reglas del firewall.
              </p>{/if}
          {:else if access === 'internal'}<p class="small muted">
              No se publican puertos en el host. Los servicios solo se conectan dentro de la red
              propia del proyecto.
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
                Crea el registro A apuntando a {options?.publicIp || 'la IP pública de la VPS'}. Se
                comprobará al revisar.
              </p>{:else}<p class="small muted">
                Se generará un dominio sslip.io para {options?.publicIp}. Solo se publicará el
                servicio web.
              </p>{/if}
            <label class="deploy-checkbox"
              ><input type="checkbox" bind:checked={secure} disabled={!options?.https} /> HTTPS automático</label
            >
          {/if}
          {#if optionsLoading}<p class="small muted">
              Comprobando las opciones del servidor…
            </p>{:else if template.web && !options?.available}<p class="small muted">
              Los dominios automáticos requieren un Traefik compatible. Puedes continuar por IP y
              puerto.
            </p>{/if}
        </div>
        <div class="deploy-section">
          <div class="section-heading">
            <h3>Variables de configuración</h3>
            <button type="button" class="text-button" on:click={() => (reveal = !reveal)}
              >{#if reveal}<EyeOff size={15} />Ocultar claves{:else}<Eye size={15} />Mostrar claves{/if}</button
            >
          </div>
          {#each template.variables as variable}
            {#if variable.name !== 'PUBLIC_URL' || access === 'port'}
              <div class="template-variable">
                <label class="field"
                  ><span
                    >{variable.label}{#if variable.required}<span
                        class="required-marker"
                        aria-hidden="true">*</span
                      >{/if}</span
                  >
                  {#if variable.type === 'boolean'}<select
                      bind:value={values[variable.name]}
                      required={variable.required}
                      ><option value="false">No</option><option value="true">Sí</option></select
                    >
                  {:else}<input
                      type={variable.secret && !reveal
                        ? 'password'
                        : variable.type === 'port'
                          ? 'number'
                          : variable.type === 'url'
                            ? 'url'
                            : 'text'}
                      bind:value={values[variable.name]}
                      required={variable.required ||
                        (variable.name === 'PUBLIC_URL' && access === 'port')}
                      pattern={variable.pattern || undefined}
                      minlength={variable.secret && variable.generate ? 16 : undefined}
                      min={variable.type === 'port' ? 1 : undefined}
                      max={variable.type === 'port' ? 65535 : undefined}
                      maxlength={4096}
                      autocomplete="off"
                      spellcheck="false"
                    />{/if}
                </label>
                <div class="template-variable-help">
                  <small>{variable.help}</small>{#if variable.generate}<button
                      type="button"
                      class="text-button"
                      on:click={() => regenerate(variable.name)}
                      aria-label={`Generar ${variable.label}`}
                      ><RefreshCw size={12} /> Generar</button
                    >{/if}
                </div>
              </div>
            {/if}
          {/each}
          <p class="small muted">
            Los valores no se guardan en el navegador ni se muestran en Actividad. Podrás
            consultarlos en la configuración privada de la VPS.
          </p>
        </div>
        <details class="template-notes">
          <summary>Antes de desplegar</summary>
          <ul>
            {#each template.notes as note}<li>{note}</li>{/each}
          </ul>
          <p>
            Referencia de recursos: {template.memoryMiB >= 1024
              ? `${template.memoryMiB / 1024} GiB`
              : `${template.memoryMiB} MiB`} de RAM como punto de partida; ajusta según la carga.
          </p>
          <a href={template.source} target="_blank" rel="noopener noreferrer"
            >Documentación oficial <ArrowUpRight size={13} /></a
          >
        </details>
      </fieldset>
      {#if !plan}<div class="deploy-footer">
          <span class="small muted">Se creará una aplicación nueva con sus propios datos.</span
          ><button class="primary" disabled={pending || optionsLoading}
            >{pending ? 'Preparando revisión…' : 'Revisar despliegue'}</button
          >
        </div>{/if}
    </form>
    {#if plan}<div class="deployment-review" bind:this={reviewPanel} aria-live="polite">
        <h3>{plan.blockers.length ? 'Falta completar' : 'Listo para desplegar'}</h3>
        {#if plan.url}<p class="planned-url">{plan.url}</p>{/if}
        {#if plan.dns}<p class="notice" class:warning={!plan.dns.ready}>{plan.dns.message}</p>
          {#if !plan.dns.ready}<button class="text-button" disabled={pending} on:click={recheck}
              >Volver a comprobar DNS</button
            >{/if}{/if}
        <ul>
          {#each plan.summary as line}<li>{line}</li>{/each}
        </ul>
        {#each plan.blockers as line}<p class="notice error">
            {line}
          </p>{/each}{#each plan.warnings as line}<p class="small muted">{line}</p>{/each}
        <details class="template-notes">
          <summary><BookOpen size={14} /> Ver Compose preparado</summary>
          <pre>{plan.composePreview}</pre>
          <p class="small muted">
            Las referencias a variables se resolverán en la VPS; sus valores se mantienen ocultos
            aquí.
          </p>
        </details>
        <div class="button-row">
          <button class="primary" disabled={pending || !!plan.blockers.length} on:click={deploy}
            >{pending ? 'Iniciando…' : `Desplegar ${template.name}`}</button
          ><button
            class="secondary"
            disabled={pending}
            on:click={async () => {
              try {
                await discard();
              } catch (e) {
                error = e.message;
              }
            }}>Editar configuración</button
          >
        </div>
      </div>{/if}
  {/if}
  {#if error}<p class="notice error spaced" role="alert">{error}</p>{/if}
</dialog>
