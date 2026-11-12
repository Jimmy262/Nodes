<script>
  import { onMount, tick } from 'svelte';
  import {
    X,
    Upload,
    Plus,
    Trash2,
    Eye,
    EyeOff,
    Globe,
    CheckCircle2,
    LoaderCircle,
    Copy,
    ArrowUpRight,
  } from '@lucide/svelte';
  import { modal } from './format.js';
  export let servers;
  export let sourceId = null;
  export let api;
  export let onClose;
  export let onStarted;
  let serverId = sourceId || servers[0]?.id || '';
  let name = '',
    files,
    project = null,
    fileError = '',
    inspecting = false;
  let options = null,
    optionsLoading = false,
    optionsError = '',
    optionsKey = '',
    optionsVersion = 0;
  let access = 'auto',
    domain = '',
    secure = true,
    service = '',
    port = 80,
    hostPort = 8080;
  let env = [],
    envTarget = '',
    reveal = false,
    pasteOpen = false,
    pasted = '',
    pasteError = '';
  let plan = null,
    pending = false,
    error = '',
    copied = false,
    job = null,
    jobId = '',
    alive = false;
  let polling = false,
    inspectVersion = 0;
  let reviewPanel;
  $: if (alive && String(serverId) !== optionsKey) loadOptions();
  $: canReview =
    !!project &&
    !!name.trim() &&
    !pending &&
    !inspecting &&
    !optionsLoading &&
    (access === 'port' || options?.available);
  async function loadOptions() {
    optionsKey = String(serverId);
    optionsLoading = true;
    optionsError = '';
    options = null;
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
        optionsError = e.message;
        access = 'port';
      }
    } finally {
      if (alive && version === optionsVersion) optionsLoading = false;
    }
  }
  async function chooseFile() {
    project = null;
    fileError = '';
    const version = ++inspectVersion;
    if (!files?.[0]) return;
    if (files[0].size > 64 * 1024 * 1024) {
      fileError = 'El ZIP debe ocupar como máximo 64 MiB.';
      return;
    }
    if (!name) {
      name = files[0].name
        .replace(/\.zip$/i, '')
        .toLowerCase()
        .replace(/[^a-z0-9-]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, 40);
      if (!/^[a-z]/.test(name)) name = ('app-' + name).slice(0, 40);
      if (name.length < 2) name = 'mi-aplicacion';
    }
    inspecting = true;
    try {
      const body = new FormData();
      body.set('file', files[0]);
      const data = await api('/deploy/inspect', {
        method: 'POST',
        body,
        signal: AbortSignal.timeout(150000),
      });
      if (alive && version === inspectVersion) {
        project = data;
        service =
          data.services.find((s) => /^(web|app|frontend|front|ui|api)$/i.test(s.name))?.name ||
          (data.services.length === 1 ? data.services[0].name : '');
        envTarget = service;
        port = data.services.find((s) => s.name === service)?.port || 80;
      }
    } catch (e) {
      if (alive && version === inspectVersion) fileError = e.message;
    } finally {
      if (alive && version === inspectVersion) inspecting = false;
    }
  }
  function selectService() {
    port = project?.services.find((s) => s.name === service)?.port || 80;
    if (!env.length || !envTarget) envTarget = service;
  }
  function addVariable() {
    env = [...env, { name: '', value: '' }];
  }
  function importVariables() {
    pasteError = '';
    const additions = [];
    for (const raw of pasted.split(/\r?\n/)) {
      const line = raw.trim();
      if (!line || line.startsWith('#')) continue;
      const match = line.match(/^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/);
      if (!match) {
        pasteError = 'Usa una variable por línea: NOMBRE=valor.';
        return;
      }
      let value = match[2];
      if (value.startsWith('"')) {
        try {
          value = JSON.parse(value);
        } catch {
          pasteError = `Revisa las comillas de ${match[1]}.`;
          return;
        }
      } else if (value.startsWith("'")) {
        if (!value.endsWith("'")) {
          pasteError = `Revisa las comillas de ${match[1]}.`;
          return;
        }
        value = value.slice(1, -1).replace(/\\'/g, "'");
      } else value = value.replace(/\s+#.*$/, '').trimEnd();
      additions.push({ name: match[1], value });
    }
    const combined = new Map(env.filter((row) => row.name).map((row) => [row.name, row]));
    for (const row of additions) combined.set(row.name, row);
    env = [...combined.values()];
    pasted = '';
    pasteOpen = false;
  }
  async function review(event) {
    event?.preventDefault();
    if (!canReview) return;
    pending = true;
    error = '';
    try {
      const body = new FormData();
      body.set('file', files[0]);
      body.set('name', name);
      body.set('hostPort', String(hostPort));
      body.set('containerPort', String(port));
      body.set('service', service);
      body.set('access', access);
      body.set('domain', domain);
      body.set('https', String(secure));
      body.set('envTarget', envTarget);
      body.set(
        'environment',
        JSON.stringify(
          env
            .filter((row) => row.name || row.value)
            .map((row) => ({ name: row.name.trim(), value: row.value })),
        ),
      );
      plan = await api(`/servers/${serverId}/deploy/plan`, {
        method: 'POST',
        body,
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
  async function recheckDNS() {
    await edit();
    await review();
  }
  async function deploy() {
    if (!plan || plan.blockers.length || pending) return;
    pending = true;
    error = '';
    try {
      const result = await api(`/operations/${plan.id}/execute`, {
        method: 'POST',
        body: JSON.stringify({ name: plan.name, acknowledged: true }),
      });
      jobId = result.id;
      job = { status: 'running', message: 'Preparando el despliegue…' };
      env = [];
      pasted = '';
      onStarted();
      poll();
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
      const jobs = await api('/operations');
      if (alive) {
        job = jobs.find((item) => item.id === jobId) || job;
        error = '';
        if (job.status !== 'running') onStarted();
      }
    } catch {
      if (alive) error = 'No se pudo consultar el progreso. Revisa Actividad antes de reintentar.';
    } finally {
      polling = false;
    }
  }
  async function copyDNS() {
    try {
      await navigator.clipboard.writeText(plan?.dns?.expectedIp || options?.publicIp || '');
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      error = 'Puedes seleccionar y copiar la IP que aparece aquí.';
    }
  }
  async function close(event) {
    event?.preventDefault();
    if (pending) return;
    if (!jobId) await edit();
    env = [];
    pasted = '';
    onClose();
  }
  onMount(() => {
    alive = true;
    const timer = setInterval(poll, 2000);
    return () => {
      alive = false;
      inspectVersion++;
      optionsVersion++;
      clearInterval(timer);
      env = [];
      pasted = '';
    };
  });
</script>

<dialog
  use:modal
  class="form-dialog deployment-dialog"
  aria-labelledby="deploy-title"
  on:cancel={close}
>
  <header>
    <div>
      <span class="eyebrow">NUEVA APLICACIÓN</span>
      <h2 id="deploy-title">Desplegar proyecto</h2>
    </div>
    <button class="icon-button" disabled={pending} on:click={close} aria-label="Cerrar despliegue"
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
          ? 'Desplegando…'
          : job.status === 'succeeded'
            ? 'Aplicación iniciada'
            : 'Revisa el resultado'}
      </h3>
      <p>{job.message}</p>
    </div>
    {#if job.status === 'succeeded' && plan?.url}<a
        class="primary deployment-link"
        href={plan.url}
        target="_blank"
        rel="noopener noreferrer">Abrir aplicación<ArrowUpRight size={16} /></a
      >{/if}
    {#if job.status === 'running'}<p class="small muted">
        Puedes seguir trabajando y consultar Actividad. Mantén Nodo abierto hasta que termine.
      </p>{/if}
  {:else}
    <p class="muted small">Proyecto, enlace y variables, en un solo lugar.</p>
    <form on:submit={review} class="deployment-form">
      <fieldset disabled={pending || !!plan}>
        <div class="form-pair">
          <label class="field"
            >Nombre<input
              bind:value={name}
              placeholder="mi-aplicacion"
              required
              minlength="2"
              maxlength="40"
              pattern={'[a-z][a-z0-9-]{1,39}'}
              title="De 2 a 40 letras minúsculas, números o guiones; empieza por una letra"
            /></label
          ><label class="field"
            >Servidor<select bind:value={serverId} required
              >{#each servers as server}<option value={server.id}>{server.name}</option
                >{/each}</select
            ></label
          >
        </div>
        <label class="deployment-upload"
          ><Upload size={20} /><span
            ><strong>Proyecto ZIP</strong><small>Dockerfile o Compose · hasta 64 MiB</small></span
          ><input
            type="file"
            accept=".zip,application/zip"
            bind:files
            on:change={chooseFile}
            required
          /></label
        >
        {#if inspecting}<p class="small muted" aria-live="polite">
            Leyendo servicios y puertos…
          </p>{:else if project}<p class="project-detected">
            <CheckCircle2 size={14} />{project.services.length}
            {project.services.length === 1
              ? 'servicio detectado'
              : 'servicios detectados'}{project.hasDotEnv ? ' · Incluye .env' : ''}
          </p>{/if}
        {#if fileError}<p class="notice error" role="alert">{fileError}</p>{/if}
        <div class="deploy-section">
          <h3><Globe size={17} />Enlace de la aplicación</h3>
          <div class="access-choices" role="group" aria-label="Tipo de acceso">
            {#each [{ value: 'auto', label: 'Automático' }, { value: 'custom', label: 'Mi dominio' }, { value: 'port', label: 'IP y puerto' }] as choice}<button
                type="button"
                class:active={access === choice.value}
                aria-pressed={access === choice.value}
                disabled={choice.value !== 'port' && !options?.available}
                on:click={() => (access = choice.value)}>{choice.label}</button
              >{/each}
          </div>
          {#if optionsLoading}<p class="small muted">
              Detectando Traefik…
            </p>{:else if optionsError}<p class="notice warning">
              {optionsError}
            </p>{:else if options && !options.available}<p class="small muted">
              {options.message}
            </p>{/if}
          {#if access === 'custom'}
            <label class="field"
              >Dominio o subdominio<input
                bind:value={domain}
                on:blur={() =>
                  (domain = domain
                    .trim()
                    .replace(/^https?:\/\//i, '')
                    .replace(/\/$/, ''))}
                placeholder="app.midominio.com"
                autocomplete="off"
                spellcheck="false"
                required
              /></label
            >
            <div class="dns-hint">
              <strong>En el proveedor donde administras tu dominio:</strong><span
                >Registro <b>A</b> · {domain || 'app.midominio.com'} →
                <code>{options?.publicIp || 'IP pública del servidor'}</code></span
              ><button
                type="button"
                class="text-button"
                disabled={!options?.publicIp}
                on:click={copyDNS}><Copy size={13} />{copied ? 'IP copiada' : 'Copiar IP'}</button
              ><small
                >La comprobación DNS se hará al revisar. El registro se configura una vez en tu
                proveedor.</small
              >
            </div>
          {:else if access === 'auto'}<p class="small muted">
              Crearemos un enlace único en <strong>sslip.io</strong>{options?.publicIp
                ? ` para ${options.publicIp}`
                : ''}. No necesitas comprar un dominio ni configurar DNS.
            </p>
          {:else}<p class="small muted">
              Se usarán los puertos del Compose. Con Dockerfile puedes elegir el puerto del
              servidor.
            </p>{/if}
          {#if project && (access !== 'port' || project.dockerfile)}<div class="form-pair">
              {#if project.services.length > 1}<label class="field"
                  >Servicio público<select bind:value={service} on:change={selectService} required
                    ><option value="" disabled>Seleccionar servicio</option
                    >{#each project.services as item}<option value={item.name}>{item.name}</option
                      >{/each}</select
                  ></label
                >{/if}<label class="field"
                >Puerto interno<input
                  type="number"
                  bind:value={port}
                  min="1"
                  max="65535"
                  required
                /></label
              >{#if access === 'port' && project.dockerfile}<label class="field"
                  >Puerto del servidor<input
                    type="number"
                    bind:value={hostPort}
                    min="1"
                    max="65535"
                    required
                  /></label
                >{/if}
            </div>{/if}
          {#if access !== 'port'}<label class="deploy-checkbox"
              ><input type="checkbox" bind:checked={secure} disabled={!options?.https} /><span
                >HTTPS automático<small
                  >{optionsLoading
                    ? 'Comprobando los certificados del servidor…'
                    : options?.https
                      ? 'Traefik gestionará el certificado cuando el DNS apunte al servidor.'
                      : 'Este servidor necesita un gestor de certificados compatible.'}</small
                ></span
              ></label
            >{/if}
        </div>
        <div class="deploy-section">
          <div class="section-heading">
            <h3>Variables de entorno <span class="muted small">Opcional</span></h3>
            <button
              type="button"
              class="icon-button"
              on:click={() => (reveal = !reveal)}
              aria-label={reveal ? 'Ocultar valores' : 'Mostrar valores'}
              >{#if reveal}<EyeOff size={16} />{:else}<Eye size={16} />{/if}</button
            >
          </div>
          {#if project?.services.length > 1 && env.length}<label class="field"
              >Aplicar a<select bind:value={envTarget} required
                ><option value="" disabled>Seleccionar servicio</option
                >{#each project.services as item}<option value={item.name}>{item.name}</option
                  >{/each}<option value="*">Todos los servicios</option></select
              ></label
            >{/if}
          {#each env as variable, index}<div class="environment-row">
              <input
                aria-label={`Nombre de variable ${index + 1}`}
                bind:value={variable.name}
                placeholder="API_KEY"
                spellcheck="false"
                autocomplete="off"
              /><input
                aria-label={`Valor de variable ${index + 1}`}
                type={reveal ? 'text' : 'password'}
                bind:value={variable.value}
                placeholder="Valor"
                autocomplete="off"
                spellcheck="false"
              /><button
                type="button"
                class="icon-button"
                on:click={() => (env = env.filter((_, i) => i !== index))}
                aria-label={`Quitar variable ${index + 1}`}><Trash2 size={15} /></button
              >
            </div>{/each}
          <div class="variable-actions">
            <button type="button" class="text-button" on:click={addVariable}
              ><Plus size={14} />Añadir variable</button
            ><button type="button" class="text-button" on:click={() => (pasteOpen = !pasteOpen)}
              >Pegar .env</button
            >
          </div>
          {#if pasteOpen}<div class="paste-env">
              <label class="field"
                >Una variable por línea<textarea
                  class:masked={!reveal}
                  bind:value={pasted}
                  rows="4"
                  placeholder="API_KEY=valor&#10;NODE_ENV=production"
                  spellcheck="false"
                  autocomplete="off"></textarea></label
              ><button type="button" class="secondary" on:click={importVariables}
                >Importar variables</button
              >{#if pasteError}<p class="notice error">{pasteError}</p>{/if}
            </div>{/if}
          <p class="small muted">
            Se añaden al servicio elegido y están disponibles para las referencias del Compose. Los
            valores no se guardan en el navegador ni se muestran en Actividad.
          </p>
        </div>
      </fieldset>
      {#if !plan}<div class="deploy-footer">
          <span class="muted small">Primero revisaremos el DNS y la configuración.</span><button
            class="primary"
            disabled={!canReview}>{pending ? 'Revisando…' : 'Revisar despliegue'}</button
          >
        </div>{/if}
    </form>
    {#if plan}
      <div bind:this={reviewPanel} class="deployment-review" aria-live="polite">
        <h3>{plan.blockers.length ? 'Falta completar' : 'Listo para desplegar'}</h3>
        {#if plan.url}<p class="planned-url">{plan.url}</p>{/if}
        {#if plan.dns}<p
            class:success={plan.dns.ready}
            class="notice"
            class:warning={!plan.dns.ready}
          >
            {plan.dns.message}
          </p>{/if}
        {#if plan.dns && !plan.dns.ready}<div class="variable-actions">
            <button class="text-button" disabled={pending} on:click={recheckDNS}
              >Volver a comprobar DNS</button
            >{#if plan.dns.expectedIp}<button
                class="text-button"
                disabled={pending}
                on:click={copyDNS}><Copy size={13} />{copied ? 'IP copiada' : 'Copiar IP'}</button
              >{/if}
          </div>{/if}
        <ul>
          {#each plan.summary as line}<li>{line}</li>{/each}
        </ul>
        {#each plan.blockers as blocker}<p class="notice error">{blocker}</p>{/each}
        {#each plan.warnings as warning}<p class="small muted review-warning">{warning}</p>{/each}
        <div class="button-row">
          <button class="primary" disabled={pending || !!plan.blockers.length} on:click={deploy}
            >{pending ? 'Iniciando…' : 'Desplegar aplicación'}<Upload size={16} /></button
          ><button class="secondary" disabled={pending} on:click={edit}>Editar configuración</button
          >
        </div>
      </div>
    {/if}
  {/if}
  {#if error}<p class="notice error spaced" role="alert">{error}</p>{/if}
</dialog>
