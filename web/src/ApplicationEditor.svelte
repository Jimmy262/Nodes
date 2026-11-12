<script>
  import { onMount, tick } from 'svelte';
  import {
    X,
    Eye,
    EyeOff,
    Plus,
    Trash2,
    Globe,
    CheckCircle2,
    LoaderCircle,
    Copy,
  } from '@lucide/svelte';
  import { modal } from './format.js';
  export let app;
  export let api;
  export let onClose;
  export let onSaved;
  let config = null,
    loading = true,
    pending = false,
    error = '',
    alive = false;
  let name = app.name,
    environment = [],
    envText = '',
    selectedService = '',
    reveal = false;
  let domainEnabled = false,
    domainID = '',
    host = '',
    domainMode = 'custom',
    domainService = '',
    port = 80,
    secure = true;
  let key = '',
    connectionOpen = false,
    copied = false,
    plan = null,
    job = null,
    jobID = '',
    polling = false,
    reviewPanel;
  $: chosenDomain = config?.domains.find((d) => d.id === domainID);
  $: selectedEnvironment = environment.find((service) => service.name === selectedService);
  $: hasDomainChange =
    domainEnabled &&
    (domainMode === 'auto' || !chosenDomain || host.trim().toLowerCase() !== chosenDomain.host);
  $: variableChanges = environment
    .map((service) => ({
      name: service.name,
      variables: service.variables
        .filter((v) => (v.name || v.value) && (v.original === undefined || v.value !== v.original))
        .map(({ name, value }) => ({ name: name.trim(), value })),
    }))
    .filter((service) => service.variables.length);
  $: environmentChanged =
    config?.environmentMode === 'dotenv' ? envText !== config.envText : variableChanges.length > 0;
  $: changed = !!config && (name.trim() !== config.name || hasDomainChange || environmentChanged);
  async function load() {
    loading = true;
    error = '';
    try {
      const data = await api(
        `/servers/${app.serverId}/application/configuration?appId=${encodeURIComponent(app.id)}`,
        { signal: AbortSignal.timeout(150000) },
      );
      if (!alive) return;
      config = data;
      name = data.name;
      envText = data.envText;
      environment = data.environment.map((service) => ({
        ...service,
        variables: service.variables.map((v) => ({ ...v, original: v.value })),
      }));
      selectedService = environment[0]?.name || '';
      domainID = data.domains[0]?.id || '';
      domainEnabled = !!domainID;
      chooseDomain();
    } catch (e) {
      if (alive) error = e.message;
    } finally {
      if (alive) loading = false;
    }
  }
  function chooseDomain() {
    const d = config?.domains.find((item) => item.id === domainID);
    host = d?.host || '';
    domainMode = 'custom';
    domainService = d?.service || environment[0]?.name || '';
    port = d?.port || 80;
    secure = d ? d.https : true;
  }
  async function connect() {
    pending = true;
    error = '';
    try {
      await api(`/servers/${app.serverId}/dokploy/connect`, {
        method: 'POST',
        body: JSON.stringify({ key }),
        signal: AbortSignal.timeout(150000),
      });
      key = '';
      connectionOpen = false;
      await load();
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  function addVariable() {
    environment = environment.map((service) =>
      service.name === selectedService
        ? { ...service, variables: [...service.variables, { name: '', value: '' }] }
        : service,
    );
  }
  function removeVariable(index) {
    environment = environment.map((service) =>
      service.name === selectedService
        ? { ...service, variables: service.variables.filter((_, i) => i !== index) }
        : service,
    );
  }
  async function copyIP() {
    try {
      await navigator.clipboard.writeText(config.publicIp);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      error = 'Selecciona y copia la IP que aparece en el formulario.';
    }
  }
  async function review(event) {
    event?.preventDefault();
    if (!changed || pending) return;
    pending = true;
    error = '';
    const body = { appId: app.id, revision: config.revision, name };
    if (environmentChanged && config.environmentMode === 'dotenv') body.envText = envText;
    else if (environmentChanged) body.environment = variableChanges;
    if (hasDomainChange)
      body.domain = {
        id: domainID,
        host,
        automatic: domainMode === 'auto',
        service: domainService,
        port: Number(port),
        https: secure,
      };
    try {
      plan = await api(`/servers/${app.serverId}/application/plan`, {
        method: 'POST',
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(150000),
      });
      await tick();
      reviewPanel?.scrollIntoView({ block: 'nearest' });
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function edit() {
    if (plan) {
      try {
        await api(`/operations/${plan.id}/plan`, { method: 'DELETE' });
      } catch {}
    }
    plan = null;
    error = '';
  }
  async function recheck() {
    await edit();
    await review();
  }
  async function save() {
    if (!plan || plan.blockers.length || pending) return;
    pending = true;
    error = '';
    try {
      const result = await api(`/operations/${plan.id}/execute`, {
        method: 'POST',
        body: JSON.stringify({ name: plan.name, acknowledged: true }),
      });
      jobID = result.id;
      job = { status: 'running', message: 'Comprobando y aplicando los cambios…' };
      environment = [];
      envText = '';
      config.envText = '';
      config.environment = [];
      poll();
      onSaved();
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function poll() {
    if (!jobID || polling || job?.status !== 'running') return;
    polling = true;
    try {
      const jobs = await api('/operations');
      if (alive) {
        job = jobs.find((j) => j.id === jobID) || job;
        error = '';
        if (job.status !== 'running') onSaved();
      }
    } catch {
      if (alive) error = 'No se pudo consultar el resultado. Revisa Actividad antes de reintentar.';
    } finally {
      polling = false;
    }
  }
  async function close(event) {
    event?.preventDefault();
    if (pending) return;
    if (!jobID) await edit();
    onClose();
  }
  onMount(() => {
    alive = true;
    load();
    const timer = setInterval(poll, 2000);
    return () => {
      alive = false;
      clearInterval(timer);
      key = '';
      envText = '';
      environment = [];
      config = null;
    };
  });
</script>

<dialog
  use:modal
  class="form-dialog deployment-dialog application-editor"
  aria-labelledby="edit-app-title"
  on:cancel={close}
>
  <header>
    <div>
      <span class="eyebrow">{app.server.name} · APLICACIÓN</span>
      <h2 id="edit-app-title">Editar configuración</h2>
    </div>
    <button
      class="icon-button"
      disabled={pending}
      on:click={close}
      aria-label="Cerrar configuración"><X size={20} /></button
    >
  </header>
  {#if loading}<p class="editor-loading">
      <LoaderCircle class="spinner" size={18} />Cargando la configuración actual…
    </p>
  {:else if job}
    <div class="job-result" aria-live="polite">
      {#if job.status === 'running'}<LoaderCircle
          class="spinner"
          size={28}
        />{:else if job.status === 'succeeded'}<CheckCircle2 size={28} />{/if}
      <h3>
        {job.status === 'running'
          ? 'Aplicando cambios…'
          : job.status === 'succeeded'
            ? 'Cambios guardados'
            : 'Revisa el resultado'}
      </h3>
      <p>{job.message}</p>
    </div>
    <button class="secondary" on:click={close}>Volver a la aplicación</button>
  {:else if config}
    <p class="small muted">Nombre, dominio y variables, sin salir de este formulario.</p>
    {#if config.connectionRequired || connectionOpen}
      <div class="editor-connection">
        <h3>Conectar Dokploy</h3>
        <p>
          Solo una vez para <strong>{app.server.name}</strong>. Crea una clave en Dokploy → Ajustes
          → Perfil → API/CLI y pégala aquí.
        </p>
        <label class="field"
          >Clave API existente<input
            type="password"
            bind:value={key}
            autocomplete="off"
            placeholder="Pega tu clave de Dokploy"
            disabled={pending}
          /></label
        >
        <button class="secondary" disabled={pending || !key.trim()} on:click={connect}
          >{pending ? 'Conectando…' : 'Conectar y cargar configuración'}</button
        >
        <small
          >Se guarda cifrada para tu cuenta de Windows. La conexión usa el SSH de este servidor.</small
        >
      </div>
    {:else if config.manager === 'Dokploy'}<div class="editor-manager">
        <span><CheckCircle2 size={14} />Dokploy conectado</span><button
          class="text-button small"
          on:click={() => (connectionOpen = true)}
          disabled={pending || !!plan}>Cambiar clave</button
        >
      </div>{/if}
    <form class="deployment-form" on:submit={review}>
      <fieldset disabled={pending || !!plan}>
        <label class="field"
          >Nombre de la aplicación<input
            bind:value={name}
            maxlength="80"
            required
            autocomplete="off"
          /></label
        >
        <p class="small muted">{config.message}</p>
        {#if config.canEdit}
          <div class="deploy-section">
            <div class="section-heading">
              <h3><Globe size={17} />Dominio público</h3>
              {#if !domainEnabled}<button
                  type="button"
                  class="text-button"
                  on:click={() => {
                    domainEnabled = true;
                    domainID = '';
                    chooseDomain();
                  }}>Añadir dominio</button
                >{:else if config.domains.length === 1 && domainID}<button
                  type="button"
                  class="text-button small"
                  on:click={() => {
                    domainID = '';
                    chooseDomain();
                  }}>Añadir otro</button
                >{/if}
            </div>
            {#if domainEnabled}
              {#if config.domains.length > 1 || (config.domains.length && !domainID)}<label
                  class="field spaced"
                  >Enlace a editar<select bind:value={domainID} on:change={chooseDomain}
                    >{#each config.domains as d}<option value={d.id}
                        >{d.host}{d.path && d.path !== '/' ? d.path : ''}{d.service
                          ? ' · ' + d.service
                          : ''}</option
                      >{/each}<option value="">Añadir otro dominio</option></select
                  ></label
                >{/if}
              <div class="access-choices" role="group" aria-label="Dominio de la aplicación">
                <button
                  type="button"
                  class:active={domainMode === 'custom'}
                  aria-pressed={domainMode === 'custom'}
                  on:click={() => (domainMode = 'custom')}>Mi dominio</button
                ><button
                  type="button"
                  class:active={domainMode === 'auto'}
                  aria-pressed={domainMode === 'auto'}
                  on:click={() => (domainMode = 'auto')}>Generar automático</button
                >
              </div>
              {#if domainMode === 'custom'}<label class="field"
                  >Dominio o subdominio<input
                    bind:value={host}
                    on:blur={() =>
                      (host = host
                        .trim()
                        .replace(/^https?:\/\//i, '')
                        .replace(/\/$/, ''))}
                    placeholder="app.midominio.com"
                    required
                    spellcheck="false"
                    autocomplete="off"
                  /></label
                >{:else}<p class="small muted">
                  Generaremos un enlace único en sslip.io al revisar los cambios.
                </p>{/if}
              {#if !domainID}<div class="form-pair">
                  {#if environment.length}<label class="field"
                      >Servicio público<select bind:value={domainService} required
                        >{#each environment as service}<option value={service.name}
                            >{service.name}</option
                          >{/each}</select
                      ></label
                    >{/if}<label class="field"
                    >Puerto interno<input
                      type="number"
                      min="1"
                      max="65535"
                      bind:value={port}
                      required
                    /></label
                  >
                </div>
                <label class="deploy-checkbox"
                  ><input type="checkbox" bind:checked={secure} /><span>HTTPS automático</span
                  ></label
                >
              {:else}<p class="small muted">
                  Se conservan el puerto, la ruta y la configuración {secure ? 'HTTPS' : 'HTTP'} actuales.
                </p>{/if}
              {#if hasDomainChange && domainMode === 'custom'}<div class="dns-hint">
                  <strong>Conecta el dominio en tu proveedor DNS</strong><span
                    >Registro A · {host || 'app.midominio.com'} →
                    <code>{config.publicIp || 'IP pública del servidor'}</code></span
                  ><button
                    type="button"
                    class="text-button"
                    disabled={!config.publicIp}
                    on:click={copyIP}
                    ><Copy size={13} />{copied ? 'IP copiada' : 'Copiar IP'}</button
                  ><small>Comprobaremos el DNS antes de aplicar el cambio.</small>
                </div>{/if}
              {#if !config.domains.length}<button
                  type="button"
                  class="text-button small spaced"
                  on:click={() => (domainEnabled = false)}>Dejar sin cambios de dominio</button
                >{/if}
            {:else}<p class="small muted">
                No hay dominios editables en la configuración de este proyecto. Sus rutas externas
                se conservan.
              </p>{/if}
          </div>
          <div class="deploy-section">
            <div class="section-heading">
              <h3>Variables de entorno</h3>
              <button
                type="button"
                class="icon-button"
                aria-label={reveal ? 'Ocultar valores' : 'Mostrar valores'}
                on:click={() => (reveal = !reveal)}
                >{#if reveal}<EyeOff size={16} />{:else}<Eye size={16} />{/if}</button
              >
            </div>
            {#if config.environmentMode === 'dotenv'}
              <label class="field spaced"
                >Una variable por línea · NOMBRE=valor<textarea
                  class="editor-env-text"
                  class:masked={!reveal}
                  bind:value={envText}
                  rows="7"
                  autocomplete="off"
                  spellcheck="false"
                  placeholder="NODE_ENV=production"></textarea></label
              >
              <p class="small muted">
                Puedes pegar tu .env completo. Conserva las líneas que quieras mantener; quitar una
                línea elimina esa variable del proyecto.
              </p>
            {:else}
              {#if environment.length > 1}<label class="field spaced"
                  >Servicio<select bind:value={selectedService}
                    >{#each environment as service}<option value={service.name}
                        >{service.name}</option
                      >{/each}</select
                  ></label
                >{/if}
              {#if selectedEnvironment}{#each selectedEnvironment.variables as variable, index}<div
                    class="environment-row"
                  >
                    <input
                      aria-label={`Nombre de variable ${index + 1}`}
                      bind:value={variable.name}
                      disabled={variable.original !== undefined}
                      placeholder="NOMBRE"
                      autocomplete="off"
                      spellcheck="false"
                    /><input
                      aria-label={`Valor de variable ${index + 1}`}
                      type={reveal ? 'text' : 'password'}
                      bind:value={variable.value}
                      placeholder="Valor"
                      autocomplete="off"
                      spellcheck="false"
                    />{#if variable.original === undefined}<button
                        type="button"
                        class="icon-button"
                        aria-label={`Quitar variable ${index + 1}`}
                        on:click={() => removeVariable(index)}><Trash2 size={15} /></button
                      >{/if}
                  </div>{/each}{/if}
              <button
                type="button"
                class="text-button spaced"
                on:click={addVariable}
                disabled={!selectedEnvironment}><Plus size={14} />Añadir variable</button
              >
              <p class="small muted">
                Edita el valor o añade variables al servicio elegido. Los valores que no cambies se
                conservarán.
              </p>
            {/if}
          </div>
        {/if}
      </fieldset>
      {#if !plan}<div class="deploy-footer">
          <span class="small muted">Los cambios se aplican después de tu revisión.</span><button
            class="primary"
            disabled={!changed || pending}>{pending ? 'Revisando…' : 'Revisar cambios'}</button
          >
        </div>{/if}
    </form>
    {#if plan}<div bind:this={reviewPanel} class="deployment-review" aria-live="polite">
        <h3>{plan.blockers.length ? 'Falta completar' : 'Revisa tus cambios'}</h3>
        {#if plan.url}<p class="planned-url">{plan.url}</p>{/if}{#if plan.dns}<p
            class="notice"
            class:success={plan.dns.ready}
            class:warning={!plan.dns.ready}
          >
            {plan.dns.message}
          </p>
          {#if !plan.dns.ready}<button class="text-button" disabled={pending} on:click={recheck}
              >Volver a comprobar DNS</button
            >{/if}{/if}
        <ul>
          {#each plan.summary as line}<li>{line}</li>{/each}
        </ul>
        {#if plan.environmentKeys?.length}<p class="small muted">
            Variables modificadas: {plan.environmentKeys.join(', ')}.
          </p>{/if}{#each plan.warnings as warning}<p class="small review-warning">
            {warning}
          </p>{/each}{#each plan.blockers as blocker}<p class="notice error">{blocker}</p>{/each}
        <div class="button-row">
          <button class="primary" disabled={pending || !!plan.blockers.length} on:click={save}
            >{pending ? 'Guardando…' : 'Guardar y aplicar'}</button
          ><button class="secondary" disabled={pending} on:click={edit}>Seguir editando</button>
        </div>
      </div>{/if}
  {/if}
  {#if error}<p class="notice error spaced" role="alert">{error}</p>
    {#if !config && !loading}<button class="secondary" on:click={load}>Volver a cargar</button
      >{/if}{/if}
</dialog>
