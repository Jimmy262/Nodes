<script>
  import { onMount } from 'svelte';
  import {
    X,
    ArrowRight,
    Upload,
    Trash2,
    ArrowRightLeft,
    CheckCircle2,
    LoaderCircle,
  } from '@lucide/svelte';
  import { modal } from './format.js';
  export let kind;
  export let servers;
  export let service = null;
  export let sourceId = null;
  export let api;
  export let onClose;
  export let onStarted;
  let serverId = sourceId || servers[0]?.id || '';
  let targetId = servers.find((s) => s.id !== Number(serverId))?.id || '';
  let name = '',
    files,
    hostPort = 8080,
    containerPort = 80;
  let plan = null,
    step = 0,
    pending = false,
    error = '',
    acknowledged = false,
    confirmation = '';
  let jobId = '',
    job = null,
    alive = true,
    polling = false;
  const labels = {
    delete: 'Eliminar contenedor',
    migrate: 'Trasladar contenedor',
    deploy: 'Desplegar desde ZIP',
  };
  $: title =
    kind === 'migrate' && plan?.manager?.startsWith('Swarm')
      ? 'Trasladar aplicación Swarm'
      : labels[kind];
  $: canExecute = plan && !plan.blockers.length && acknowledged && confirmation === plan.name;
  $: compactReview = kind === 'migrate' && !!plan?.review;
  async function review(event) {
    event?.preventDefault();
    pending = true;
    error = '';
    try {
      if (kind === 'deploy') {
        const data = new FormData();
        data.set('name', name);
        data.set('file', files[0]);
        data.set('hostPort', String(hostPort));
        data.set('containerPort', String(containerPort));
        plan = await api(`/servers/${serverId}/deploy/plan`, {
          method: 'POST',
          body: data,
          signal: AbortSignal.timeout(150000),
        });
      } else {
        plan = await api(`/servers/${serverId}/containers/${service.ID}/plan`, {
          method: 'POST',
          body: JSON.stringify({ kind, targetId: Number(targetId) }),
          signal: AbortSignal.timeout(150000),
        });
      }
      step = 1;
      acknowledged = false;
      confirmation = '';
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function discard() {
    if (plan && !jobId) {
      try {
        await api(`/operations/${plan.id}/plan`, { method: 'DELETE' });
      } catch {
        /* Expires automatically. */
      }
    }
    plan = null;
    step = 0;
    error = '';
  }
  async function execute(event) {
    event.preventDefault();
    if (!canExecute || pending) return;
    pending = true;
    error = '';
    try {
      const data = await api(`/operations/${plan.id}/execute`, {
        method: 'POST',
        body: JSON.stringify({ name: confirmation, acknowledged }),
      });
      jobId = data.id;
      step = 3;
      job = { status: 'running', message: 'Comprobando la operación…' };
      onStarted();
      poll();
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
  async function poll() {
    if (!jobId || polling || (job && job.status !== 'running')) return;
    polling = true;
    try {
      const jobs = await api('/operations');
      if (alive) {
        job = jobs.find((j) => j.id === jobId) || job;
        error = '';
        if (job?.status !== 'running') onStarted();
      }
    } catch (e) {
      if (alive)
        error =
          'No se pudo consultar el progreso. La operación puede seguir en el servidor. Revisa Actividad.';
    } finally {
      polling = false;
    }
  }
  async function close(event) {
    event?.preventDefault();
    if (pending) return;
    await discard();
    onClose();
  }
  onMount(() => {
    const timer = setInterval(poll, 2000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  });
</script>

<dialog
  use:modal
  class="form-dialog operation-dialog"
  class:compact-operation={kind === 'migrate'}
  aria-labelledby="operation-title"
  on:cancel={close}
>
  <header>
    <h2 id="operation-title">{title}</h2>
    <button class="icon-button" disabled={pending} on:click={close} aria-label="Cerrar operación"
      ><X size={20} /></button
    >
  </header>
  {#if step === 0}
    <p class="muted">
      {kind === 'deploy'
        ? 'Sube el proyecto y revisa lo que se desplegará.'
        : kind === 'migrate'
          ? 'Comprueba si se puede trasladar con su configuración y datos.'
          : 'Primero revisaremos qué se eliminará y qué se conservará.'}
    </p>
    <form class="operation-form" on:submit={review}>
      {#if kind === 'deploy'}
        <label class="field"
          >Servidor de destino<select bind:value={serverId} required
            >{#each servers as server}<option value={server.id}>{server.name}</option
              >{/each}</select
          ></label
        >
        <label class="field"
          >Nombre de la aplicación<input
            bind:value={name}
            placeholder="mi-aplicacion"
            required
            minlength="2"
            maxlength="40"
            pattern={'[a-z][a-z0-9-]{1,39}'}
            title="De 2 a 40 letras minúsculas, números o guiones; empieza por una letra"
          /></label
        >
        <label class="upload-zone field"
          ><Upload size={25} /><strong>Proyecto en ZIP</strong><span class="small muted"
            >Hasta 64 MiB. Incluye compose.yaml o Dockerfile.</span
          ><input type="file" accept=".zip,application/zip" bind:files required /></label
        >
        <details class="port-options">
          <summary>Puertos para proyectos con Dockerfile</summary>
          <p class="small muted">Si incluyes Compose se utilizarán los puertos de ese archivo.</p>
          <div class="form-pair">
            <label class="field"
              >Puerto del servidor<input
                type="number"
                bind:value={hostPort}
                min="1"
                max="65535"
                required
              /></label
            ><label class="field"
              >Puerto del contenedor<input
                type="number"
                bind:value={containerPort}
                min="1"
                max="65535"
                required
              /></label
            >
          </div>
        </details>
      {:else}
        <div class="operation-target">
          <strong>{service.Names}</strong><span
            >{servers.find((s) => s.id === Number(serverId))?.name}</span
          >
        </div>
        {#if kind === 'migrate'}
          <label class="field"
            >Servidor de destino<select bind:value={targetId} required
              ><option value="" disabled>Seleccionar servidor</option
              >{#each servers.filter((s) => s.id !== Number(serverId)) as server}<option
                  value={server.id}>{server.name}</option
                >{/each}</select
            ></label
          >
          <p class="notice warning">
            El original se detendrá durante el traslado. Al terminar quedará conservado y detenido
            hasta que compruebes la copia.
          </p>
        {/if}
      {/if}
      <button
        class="primary"
        disabled={pending ||
          !serverId ||
          (kind === 'migrate' && !targetId) ||
          (kind === 'deploy' && !files?.length)}
        >{pending ? 'Revisando…' : 'Revisar operación'}<ArrowRight size={16} /></button
      >
    </form>
  {:else if step === 1}
    <span class="eyebrow">Revisar · 1 de 2</span>
    <div class="operation-target">
      <strong>{plan.name}</strong><span
        >{plan.source}{plan.destination ? ` → ${plan.destination}` : ''}{!compactReview &&
        plan.manager
          ? ` · ${plan.manager}`
          : ''}</span
      >
    </div>
    <ul class="operation-list">
      {#each compactReview ? plan.review.summary : plan.summary as item}<li>{item}</li>{/each}
    </ul>
    {#if compactReview}
      <div class="operation-link"><span>Enlace al terminar</span><span>{plan.url}</span></div>
      <details class="operation-details">
        <summary>Detalles técnicos y volúmenes</summary>
        <div class="operation-details-content">
          <ul class="operation-list">
            {#each plan.summary as item}<li>{item}</li>{/each}
          </ul>
          {#each plan.warnings as warning}<p>{warning}</p>{/each}
        </div>
      </details>
    {/if}
    {#each plan.blockers as blocker}<p class="notice error">{blocker}</p>{/each}
    {#if !plan.blockers.length}
      {#if compactReview}
        <ul class="operation-cautions" aria-label="Efectos del traslado">
          {#each plan.review.warnings as warning}<li>{warning}</li>{/each}
        </ul>
      {:else}
        {#each plan.warnings as warning}<p class="notice warning">{warning}</p>{/each}
      {/if}
      <div class="operation-review-actions">
        <label class="remember-option"
          ><input type="checkbox" bind:checked={acknowledged} /><span
            >{compactReview
              ? 'Entiendo la interrupción y los efectos del traslado.'
              : 'He revisado los efectos de esta operación.'}</span
          ></label
        >
        <div class="button-row">
          <button
            class={kind === 'delete' ? 'danger' : 'primary'}
            disabled={!acknowledged}
            on:click={() => (step = 2)}>Continuar<ArrowRight size={16} /></button
          ><button class="secondary" on:click={discard}>Volver</button>
        </div>
      </div>
    {:else}
      <p class="muted small">La revisión no ha modificado ningún contenedor.</p>
      <button class="secondary spaced" on:click={discard}>Volver</button>
    {/if}
  {:else if step === 2}
    <span class="eyebrow">Confirmar · 2 de 2</span>
    <p class="spaced">
      {kind === 'delete'
        ? 'Esta acción eliminará el contenedor de'
        : kind === 'migrate'
          ? 'Se detendrá el contenedor en'
          : 'Se desplegará en'} <strong>{plan.source}</strong>{kind === 'migrate'
        ? ` y se copiará a ${plan.destination}.`
        : '.'}
    </p>
    <form class="operation-form" on:submit={execute}>
      <label class="field"
        >Escribe <strong class="exact-name">{plan.name}</strong> para confirmar<input
          bind:value={confirmation}
          autocomplete="off"
          spellcheck="false"
          required
        /></label
      >
      <div class="button-row">
        <button class={kind === 'delete' ? 'danger' : 'primary'} disabled={!canExecute || pending}
          >{#if kind === 'delete'}<Trash2 size={16} />{:else if kind === 'migrate'}<ArrowRightLeft
              size={16}
            />{:else}<Upload size={16} />{/if}{pending
            ? 'Iniciando…'
            : kind === 'delete'
              ? 'Eliminar definitivamente'
              : kind === 'migrate'
                ? 'Detener y trasladar'
                : 'Desplegar aplicación'}</button
        ><button type="button" class="secondary" disabled={pending} on:click={() => (step = 1)}
          >Volver</button
        >
      </div>
    </form>
  {:else}
    <div class="job-result" aria-live="polite">
      {#if job?.status === 'running'}<LoaderCircle
          class="spinner"
          size={28}
        />{:else if job?.status === 'succeeded'}<CheckCircle2 size={28} />{/if}
      <h3>
        {job?.status === 'running'
          ? 'Operación en curso'
          : job?.status === 'succeeded'
            ? 'Operación completada'
            : 'Revisa el resultado'}
      </h3>
      <p>{job?.message}</p>
    </div>
    {#if job?.status === 'running'}<p class="small muted">
        Puedes cerrar esta ventana y seguir el progreso en Actividad. Mantén Nodes abierto hasta que
        termine.
      </p>{/if}
    <button class="secondary spaced" on:click={close}>Cerrar</button>
  {/if}
  {#if error}<p class="notice error spaced" role="alert">{error}</p>{/if}
</dialog>
