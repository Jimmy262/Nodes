<script>
  import { Layers, ArrowUpRight, Star, ChevronRight, ArrowDown } from '@lucide/svelte';
  import { bytes, percent, hostname, statusLabel } from './format.js';
  export let items = [];
  export let favorites = [];
  export let onOpen;
  export let onFavorite;
  export let sort = 'name';
  export let onSort;
</script>

<div class="app-table table-surface">
  <div class="app-columns table-heading">
    <button class:sorted={sort === 'name'} on:click={() => onSort('name')}>Aplicación</button>
    <span>Servidor</span><span>Estado</span>
    <button class:sorted={sort === 'cpu'} on:click={() => onSort('cpu')}
      >CPU {#if sort === 'cpu'}<ArrowDown size={13} />{/if}</button
    >
    <button class:sorted={sort === 'memory'} on:click={() => onSort('memory')}
      >RAM {#if sort === 'memory'}<ArrowDown size={13} />{/if}</button
    >
    <span>Enlace público</span><span></span>
  </div>
  {#each items as app (app.id)}
    <div class="app-row app-columns" class:outdated={app.stale}>
      <button
        class="app-name"
        on:click={(event) => onOpen(app, event)}
        aria-label={`Ver ${app.name}`}
      >
        <span class="app-icon"><Layers size={20} strokeWidth={1.5} /></span>
        <span class="name-copy"
          ><strong>{app.name}</strong>{#if app.services.length > 1}<small
              >{app.services.length} servicios</small
            >{/if}</span
        >
      </button>
      <span class="app-server">{app.server.name}</span>
      <span class="app-status status" class:warning={app.status !== 'running' || app.stale}>
        <i></i>{app.stale ? 'Sin actualizar' : statusLabel(app.status)}
      </span>
      <span class="usage cpu-usage" title="Porcentaje de la CPU total del servidor">
        <span class="mobile-label">CPU</span>
        <span class="meter"><span style={`width:${Math.min(100, app.cpu || 0)}%`}></span></span>
        <span>{percent(app.cpu)}{app.partial && app.cpu != null ? '*' : ''}</span>
      </span>
      <span
        class="usage ram-usage"
        title={app.partial
          ? 'Consumo parcial: faltan métricas de uno o más servicios'
          : 'RAM de todos los contenedores de esta aplicación'}
        ><span class="mobile-label">RAM</span>{bytes(app.memory)}{app.partial && app.memory != null
          ? '*'
          : ''}</span
      >
      <span class="app-link">
        {#if app.links[0]}<a
            href={app.links[0].url}
            target="_blank"
            rel="noopener noreferrer"
            title={app.links[0].url}
            ><span>{hostname(app.links[0].url)}</span><ArrowUpRight size={15} /></a
          >{:else}<span class="muted">Sin dominio detectado</span>{/if}
      </span>
      <div class="row-actions">
        <button
          class="icon-button favorite"
          class:active={favorites.includes(app.id)}
          on:click={() => onFavorite(app.id)}
          aria-label={`${favorites.includes(app.id) ? 'Quitar de' : 'Añadir a'} favoritas: ${app.name}`}
          aria-pressed={favorites.includes(app.id)}
          ><Star size={15} fill={favorites.includes(app.id) ? 'currentColor' : 'none'} /></button
        >
        <button
          class="icon-button mobile-details"
          on:click={() => onOpen(app)}
          aria-label={`Detalles de ${app.name}`}><ChevronRight size={16} /></button
        >
      </div>
    </div>
  {/each}
</div>
