<script>
  import { onMount } from 'svelte';
  import { X, RefreshCw, LoaderCircle, CheckCircle2, ArrowUpRight } from '@lucide/svelte';
  import { modal } from './format.js';
  export let deployment;
  export let appName;
  export let api;
  export let onClose;
  export let onUpdated;
  let plan = null,
    job = null,
    jobId = '',
    pending = false,
    error = '',
    alive = false,
    polling = false;
  async function review() {
    pending = true;
    error = '';
    try {
      const result = await api(`/github/deployments/${deployment.id}/deploy`, {
        method: 'POST',
        signal: AbortSignal.timeout(150000),
      });
      if (alive) plan = result;
      else await api(`/operations/${result.id}/plan`, { method: 'DELETE' });
    } catch (e) {
      if (alive) error = e.message;
    } finally {
      if (alive) pending = false;
    }
  }
  async function execute() {
    if (pending || !plan || plan.blockers.length || jobId) return;
    pending = true;
    error = '';
    try {
      const result = await api(`/operations/${plan.id}/execute`, {
        method: 'POST',
        body: JSON.stringify({ name: plan.name, acknowledged: true }),
      });
      jobId = result.id;
      job = { status: 'running', message: 'Preparando la actualización…' };
      onUpdated();
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
      const jobs = await api('/operations');
      if (alive) {
        job = jobs.find((item) => item.id === jobId) || job;
        if (job.status !== 'running') onUpdated();
      }
    } catch {
      if (alive) error = 'No se pudo consultar el progreso. Revisa Actividad antes de reintentar.';
    } finally {
      polling = false;
    }
  }
  async function close(event) {
    event?.preventDefault();
    if (pending) return;
    if (plan && !jobId) {
      try {
        await api(`/operations/${plan.id}/plan`, { method: 'DELETE' });
      } catch {}
    }
    onClose();
  }
  onMount(() => {
    alive = true;
    review();
    const timer = setInterval(poll, 2000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  });
</script>

<dialog
  use:modal
  class="form-dialog deployment-dialog"
  aria-labelledby="github-update-title"
  on:cancel={close}
>
  <header>
    <div>
      <span class="eyebrow">{appName}</span>
      <h2 id="github-update-title">Actualizar desde GitHub</h2>
    </div>
    <button class="icon-button" disabled={pending} on:click={close} aria-label="Cerrar"
      ><X size={20} /></button
    >
  </header>
  <a
    class="detail-link"
    href={`https://github.com/${deployment.repository}`}
    target="_blank"
    rel="noopener noreferrer">{deployment.repository}<ArrowUpRight size={15} /></a
  >
  <p class="small muted">
    Último commit de la rama {deployment.branch}. Se actualizará la aplicación existente y se
    conservarán sus volúmenes y datos.
  </p>
  {#if job}
    <div class="notice" role="status">
      {#if job.status === 'running'}<LoaderCircle
          size={18}
          class="spin"
        />{:else if job.status === 'succeeded'}<CheckCircle2 size={18} />{/if}
      <p>{job.message}</p>
      {#if job.result}<p>{job.result}</p>{/if}
    </div>
    <p class="small muted">Puedes consultar el resultado en Actividad.</p>
    <button class="secondary" on:click={close}>Volver a la aplicación</button>
  {:else if plan}
    <section class="deployment-review" aria-label="Revisión de la actualización">
      <h3>Revisar actualización</h3>
      <ul>
        {#each plan.summary as item}<li>{item}</li>{/each}
      </ul>
      {#each plan.warnings as warning}<p class="notice warning">{warning}</p>{/each}
      {#each plan.blockers as blocker}<p class="notice error">{blocker}</p>{/each}
      <button class="primary" disabled={pending || !!plan.blockers.length} on:click={execute}
        ><RefreshCw size={15} />{pending ? 'Iniciando…' : 'Actualizar aplicación'}</button
      >
    </section>
  {:else if pending}
    <p role="status">Consultando el último commit de GitHub…</p>
  {:else}
    <button class="secondary" on:click={review}>Reintentar</button>
  {/if}
  {#if error}<p class="notice error" role="alert">{error}</p>{/if}
</dialog>
