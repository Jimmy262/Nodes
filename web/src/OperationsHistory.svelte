<script>
  import { onMount } from 'svelte';
  import { X, CheckCircle2, LoaderCircle, AlertCircle } from '@lucide/svelte';
  import { modal } from './format.js';
  export let operations;
  export let servers;
  export let onClose;
  let now = Date.now();
  const states = {
    running: 'En curso',
    succeeded: 'Completada',
    failed: 'Falló',
    interrupted: 'Interrumpida',
  };
  function duration(item) {
    if (!item.startedAt) return '';
    const seconds = Math.max(
      0,
      Math.floor(
        ((item.finishedAt ? Date.parse(item.finishedAt) : now) - Date.parse(item.startedAt)) / 1000,
      ),
    );
    return seconds < 60 ? `${seconds} s` : `${Math.floor(seconds / 60)} min ${seconds % 60} s`;
  }
  function date(value) {
    return new Date(value).toLocaleString('es-PE');
  }
  onMount(() => {
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });
  const labels = {
    'terminal-install': 'Instalar requisito de terminal',
    'template-deploy': 'Desplegar plantilla Compose',
    'github-setup': 'Conectar despliegue GitHub',
    'github-deploy': 'Desplegar commit GitHub',
    delete: 'Eliminar contenedor',
    migrate: 'Trasladar contenedor',
    deploy: 'Desplegar ZIP',
    configure: 'Editar aplicación',
  };
</script>

<dialog
  use:modal
  class="form-dialog operation-dialog"
  aria-labelledby="activity-title"
  on:cancel={onClose}
>
  <header>
    <h2 id="activity-title">Actividad</h2>
    <button class="icon-button" on:click={onClose} aria-label="Cerrar actividad"
      ><X size={20} /></button
    >
  </header>
  <p class="muted small">Últimas 20 operaciones. El progreso se actualiza automáticamente.</p>
  <div class="operation-history">
    {#each operations as item (item.id)}
      <article>
        {#if item.status === 'running'}<LoaderCircle
            class="spinner"
            size={20}
          />{:else if item.status === 'succeeded'}<CheckCircle2 size={20} />{:else}<AlertCircle
            size={20}
          />{/if}
        <div class="activity-content">
          <div class="activity-heading">
            <strong>{item.details?.name || labels[item.kind]}</strong><span
              class="activity-state"
              class:warning={item.status === 'failed' || item.status === 'interrupted'}
              >{states[item.status] || item.status}</span
            >
          </div>
          <p class="activity-context">
            {labels[item.kind]} · {item.details?.source ||
              servers.find((s) => s.id === item.serverId)?.name ||
              'Servidor retirado'}{item.details?.destination
              ? ' → ' + item.details.destination
              : ''}
          </p>
          {#if item.details?.application && item.details.application !== item.details.name}<p
              class="small muted"
            >
              Aplicación: {item.details.application}
            </p>{/if}
          {#if item.details?.containerId}<p class="small muted">
              Contenedor <code title={item.details.containerId}
                >{item.details.containerId.slice(0, 12)}</code
              >
            </p>{/if}
          <p>{item.message}</p>
          <small class="muted"
            >{item.startedAt ? 'Inicio' : 'Actualizada'}: {date(
              item.startedAt || item.at,
            )}{item.startedAt ? ' · ' + duration(item) : ''}</small
          >
          <details class="activity-details">
            <summary>Ver pasos y detalles</summary>
            {#if !item.details?.name}<p class="small muted">
                Esta operación anterior no registró el nombre ni el ID del recurso. Los nuevos
                registros sí incluirán esos datos.
              </p>{/if}
            {#if item.details?.containerId}<p class="small">
                <b>ID completo:</b> <code>{item.details.containerId}</code>
              </p>{/if}
            {#if item.details?.image}<p class="small">
                <b>Imagen:</b> <code>{item.details.image}</code>
              </p>{/if}
            {#if item.details?.manager}<p class="small">
                <b>Administrador:</b>
                {item.details.manager}
              </p>{/if}
            {#if item.details?.url}<p class="small">
                <b>Enlace:</b>
                <a href={item.details.url} target="_blank" rel="noopener noreferrer"
                  >{item.details.url}</a
                >
              </p>{/if}
            {#if item.details?.changes?.length}<ul class="activity-effects">
                {#each item.details.changes as line}<li>{line}</li>{/each}
              </ul>{/if}
            {#if item.events?.length}<ol class="activity-timeline">
                {#each item.events as event}<li>
                    <time datetime={event.at}>{new Date(event.at).toLocaleTimeString('es-PE')}</time
                    ><span>{event.message}</span>
                  </li>{/each}
              </ol>{/if}
            {#if item.finishedAt}<p class="small muted">Fin: {date(item.finishedAt)}</p>{/if}
          </details>
        </div>
      </article>
    {:else}<p class="muted spaced">Todavía no has realizado operaciones.</p>{/each}
  </div>
</dialog>
