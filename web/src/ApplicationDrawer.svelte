<script>
  import { onMount } from 'svelte';
  import {
    X,
    ArrowUpRight,
    Layers,
    Copy,
    Pause,
    Play,
    Check,
    Trash2,
    ArrowRightLeft,
    Pencil,
  } from '@lucide/svelte';
  import { bytes, percent, hostname, statusLabel, age, modal } from './format.js';
  export let app;
  export let api;
  export let onClose;
  export let now;
  export let onManage;
  export let onEdit;
  let tab = 'summary';
  let expandedContainers = new Map();
  let containerID = app.services[0]?.ID || '';
  let logs = '',
    logError = '',
    logsAt = '',
    pending = false,
    paused = false,
    copied = false;
  let activeKey = '',
    requestVersion = 0,
    mounted = false;
  $: if (!app.services.some((service) => service.ID === containerID))
    containerID = app.services[0]?.ID || '';
  $: logKey = tab === 'logs' ? `${app.serverId}:${containerID}` : '';
  $: if (mounted && logKey !== activeKey) {
    activeKey = logKey;
    requestVersion++;
    pending = false;
    logs = '';
    logError = '';
    logsAt = '';
    if (logKey) fetchLogs();
  }
  async function fetchLogs() {
    if (pending || !activeKey || !containerID) return;
    pending = true;
    const version = ++requestVersion;
    try {
      const data = await api(`/servers/${app.serverId}/containers/${containerID}/logs`);
      if (version === requestVersion && mounted) {
        logs = data.logs;
        logError = '';
        logsAt = new Date().toISOString();
      }
    } catch (error) {
      if (version === requestVersion && mounted) logError = error.message;
    } finally {
      if (version === requestVersion && mounted) pending = false;
    }
  }
  async function copyLogs() {
    try {
      await navigator.clipboard.writeText(logs);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      logError = 'No se pudieron copiar los logs. Puedes seleccionar el texto.';
    }
  }
  onMount(() => {
    mounted = true;
    const timer = setInterval(() => {
      if (tab === 'logs' && !paused && !document.hidden) fetchLogs();
    }, 15000);
    return () => {
      mounted = false;
      requestVersion++;
      clearInterval(timer);
    };
  });
</script>

<dialog use:modal class="drawer" aria-labelledby="application-title" on:cancel={onClose}>
  <header class="drawer-heading">
    <span class="eyebrow">APLICACIÓN</span><button
      class="icon-button"
      on:click={onClose}
      aria-label="Cerrar detalle"><X size={20} /></button
    >
  </header>
  <div class="drawer-intro">
    <span class="app-icon large"><Layers size={25} strokeWidth={1.5} /></span>
    <h2 id="application-title">{app.name}</h2>
    <div class="inline-meta">
      <span class="status" class:warning={app.status !== 'running' || app.stale}
        ><i></i>{app.stale ? 'Sin actualizar' : statusLabel(app.status)}</span
      ><span
        >{app.server.name} · {app.services.length}
        {app.services.length === 1 ? 'servicio' : 'servicios'}</span
      >
    </div>
    <button class="secondary edit-application-button" on:click={onEdit}
      ><Pencil size={15} />Editar configuración</button
    >
    {#if app.links[0]}<a
        class="public-entry"
        href={app.links[0].url}
        target="_blank"
        rel="noopener noreferrer"
        ><span>{hostname(app.links[0].url)}</span><span>Abrir <ArrowUpRight size={15} /></span></a
      >{/if}
  </div>
  <div class="tabs drawer-tabs" role="tablist" aria-label="Detalle de aplicación">
    {#each [{ id: 'summary', label: 'Resumen' }, { id: 'logs', label: 'Logs' }, { id: 'details', label: 'Detalles' }] as item}<button
        id={`tab-${item.id}`}
        role="tab"
        aria-selected={tab === item.id}
        aria-controls="drawer-content"
        class:active={tab === item.id}
        on:click={() => (tab = item.id)}>{item.label}</button
      >{/each}
  </div>
  <div
    class="drawer-content"
    id="drawer-content"
    role="tabpanel"
    aria-labelledby={`tab-${tab}`}
    tabindex="0"
  >
    {#if app.stale}<p class="notice warning">
        Últimos datos disponibles: {age(app.server.docker.at, now).toLowerCase()}. Se reintentará
        automáticamente.
      </p>{/if}
    {#if tab === 'summary'}
      <div class="resource-summary">
        <div>
          <span>CPU utilizada</span><strong
            >{percent(app.cpu)}{app.partial && app.cpu != null ? '*' : ''}</strong
          >
        </div>
        <div>
          <span>Memoria utilizada</span><strong
            >{bytes(app.memory)}{app.partial && app.memory != null ? '*' : ''}</strong
          >
        </div>
      </div>
      {#if app.partial}<p class="notice warning">
          Consumo parcial: faltan métricas de algún servicio. Los valores disponibles siguen
          visibles.
        </p>{/if}
      <div class="section-heading">
        <h3>Servicios</h3>
        <span class="muted">{age(app.server.docker.statsAt, now)}</span>
      </div>
      <button class="text-button small" on:click={() => (tab = 'details')}
        >Administrar contenedores<ArrowRightLeft size={14} /></button
      >
      <div class="service-list">
        {#each app.services as service}<div class="service-item">
            <div>
              <strong title={service.Names}>{service.name}</strong><span
                class="status"
                class:warning={service.Status !== 'running' || service.health === 'unhealthy'}
                ><i></i>{service.health === 'unhealthy'
                  ? 'Sin salud'
                  : service.Status === 'running'
                    ? 'En ejecución'
                    : service.Status}</span
              >
            </div>
            <div class="service-metrics">
              <span>{percent(service.cpu)} <small>CPU</small></span><span
                >{bytes(service.memory)} <small>RAM</small></span
              >
            </div>
          </div>{/each}
      </div>
      {#if app.links.length > 1}<h3 class="spaced">Otros enlaces</h3>
        {#each app.links.slice(1) as link}<a
            class="detail-link"
            href={link.url}
            target="_blank"
            rel="noopener noreferrer"><span>{hostname(link.url)}</span><ArrowUpRight size={15} /></a
          >{/each}{/if}
      {#if !app.links.length}<p class="muted spaced">
          No se detectó un dominio público para esta aplicación.
        </p>{/if}
      {#if app.server.docker.warnings.length}<div class="notice warning spaced">
          {#each app.server.docker.warnings as warning}<p>{warning}</p>{/each}
        </div>{/if}
    {:else if tab === 'logs'}
      <label class="field"
        >Servicio<select bind:value={containerID}
          >{#each app.services as service}<option value={service.ID}>{service.name}</option
            >{/each}</select
        ></label
      >
      <div class="log-toolbar">
        <span class="muted">Últimas 200 líneas</span>
        <div>
          <button
            class="icon-button"
            on:click={() => (paused = !paused)}
            aria-label={paused ? 'Reanudar logs' : 'Pausar logs'}
            title={paused ? 'Reanudar' : 'Pausar'}
            >{#if paused}<Play size={16} />{:else}<Pause size={16} />{/if}</button
          ><button
            class="icon-button"
            on:click={copyLogs}
            disabled={!logs}
            aria-label="Copiar logs"
            title="Copiar logs"
            >{#if copied}<Check size={16} />{:else}<Copy size={16} />{/if}</button
          >
        </div>
      </div>
      {#if logError}<p class="notice error" role="alert">{logError}</p>{/if}
      <pre class="logs">{logs ||
          (pending
            ? 'Cargando logs…'
            : logError
              ? 'Sin logs disponibles.'
              : 'Este servicio todavía no tiene logs.')}</pre>
      <p class="small muted">
        {paused ? 'Actualización pausada' : 'Actualización automática cada 15 s'}{logsAt
          ? ` · ${age(logsAt, now)}`
          : ''}
      </p>
    {:else}
      <h3>Contenedores</h3>
      {#each app.services as service (service.ID)}<details
          class="container-detail"
          open={expandedContainers.get(service.ID) ?? app.services.length === 1}
          on:toggle={(event) => {
            const expanded = event.currentTarget.open;
            if (expandedContainers.get(service.ID) !== expanded)
              expandedContainers = new Map(expandedContainers).set(service.ID, expanded);
          }}
        >
          <summary>{service.name}</summary>
          <dl>
            <dt>Nombre</dt>
            <dd>{service.Names}</dd>
            <dt>Imagen</dt>
            <dd>{service.Image}</dd>
            <dt>ID</dt>
            <dd class="mono">{service.ID}</dd>
            <dt>Estado Docker</dt>
            <dd>{service.Status}{service.health ? ` · ${service.health}` : ''}</dd>
          </dl>
          <div class="container-actions">
            <button class="text-button" on:click={() => onManage('migrate', service)}
              ><ArrowRightLeft size={15} />Trasladar</button
            ><button class="text-button danger-text" on:click={() => onManage('delete', service)}
              ><Trash2 size={15} />Eliminar</button
            >
          </div>
        </details>{/each}
      <h3 class="spaced">Origen de los enlaces</h3>
      {#each app.links as link}<div class="link-source">
          <a href={link.url} target="_blank" rel="noopener noreferrer"
            >{hostname(link.url)} <ArrowUpRight size={14} /></a
          ><small>{link.source}</small>
        </div>{:else}<p class="muted">Sin enlaces detectados.</p>{/each}
      <p class="small muted spaced">
        Detectados desde la configuración del proxy. Su disponibilidad en Internet no se comprueba.
      </p>
    {/if}
  </div>
</dialog>
