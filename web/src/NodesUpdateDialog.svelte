<script>
  import { onMount } from 'svelte';
  import { RefreshCw, X } from '@lucide/svelte';
  import { modal } from './format.js';
  import { usePasskey } from './lib/passkeys.js';

  export let api;
  export let onClose;
  let info = null;
  let busy = false;
  let error = '';
  let target = '';
  let active = true;
  let polling = false;

  async function check() {
    busy = true;
    error = '';
    try {
      info = await api('/nodes-update?check=1');
      if (info.pending) target = info.pending;
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  async function update() {
    if (busy || target || !info?.enabled || !info.available) return;
    busy = true;
    error = '';
    const version = info.latest;
    try {
      const session = await api('/session');
      await usePasskey(api, { remember: session.remember });
      const result = await api('/nodes-update', {
        method: 'POST',
        body: JSON.stringify({ version }),
      });
      target = result.pending;
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  async function poll() {
    if (!target || polling || !active) return;
    polling = true;
    try {
      const status = await api('/nodes-update');
      if (!active) return;
      if (status.current === target || status.lastResult === `failed ${target}`) {
        if (status.current !== target && status.pending) return;
        if (status.current !== target) error = 'La actualización falló. Consulta el registro del servicio nodes-update.';
        target = '';
        info = { ...info, ...status, pending: '', available: status.current !== info.latest };
      }
    } catch {
      // A brief disconnection is expected while the service restarts.
    } finally {
      polling = false;
    }
  }

  function close(event) {
    event?.preventDefault();
    if (!busy) onClose();
  }

  onMount(() => {
    check();
    const timer = setInterval(poll, 3000);
    return () => { active = false; clearInterval(timer); };
  });
</script>

<dialog use:modal class="form-dialog" aria-labelledby="nodes-update-title" on:cancel={close}>
  <header>
    <h2 id="nodes-update-title">Actualizaciones de Nodes</h2>
    <button class="icon-button" on:click={close} disabled={busy} aria-label="Cerrar"><X size={20} /></button>
  </header>
  {#if info}
    <dl class="connection-details">
      <dt>Versión instalada</dt><dd>{info.current === 'dev' ? 'Compilación personalizada' : info.current}</dd>
      {#if info.latest}<dt>Publicada en GitHub</dt><dd>{info.latest}</dd>{/if}
    </dl>
    {#if target}
      <p class="notice" role="status">Actualizando Nodes a {target}. El panel puede desconectarse durante el reinicio.</p>
    {:else if !info.enabled}
      <p class="notice">Esta instalación se actualiza desde el servidor. Consulta las instrucciones de su instalación.</p>
    {:else if info.available}
      <p class="muted">Nodes verificará el paquete, respaldará sus datos y reiniciará el panel.</p>
      <button class="primary" disabled={busy} on:click={update}><RefreshCw size={16} />{busy ? 'Verificando…' : `Actualizar a ${info.latest}`}</button>
    {:else}
      <p class="notice" role="status">Nodes está al día.</p>
    {/if}
  {:else if busy}
    <p role="status">Consultando GitHub…</p>
  {/if}
  {#if error}<p class="notice error" role="alert">{error}</p>{/if}
  {#if !target}<button class="text-button" on:click={check} disabled={busy}>Comprobar de nuevo</button>{/if}
</dialog>
