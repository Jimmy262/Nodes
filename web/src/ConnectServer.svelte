<script>
  import { ArrowRight, X, ShieldCheck, ArrowLeft } from '@lucide/svelte';
  import { modal } from './format.js';
  export let api;
  export let onClose;
  export let onConnected;
  let form = { name: '', host: '', port: 22, user: 'root' };
  let probe = null,
    password = '',
    confirmed = false,
    busy = false,
    error = '';
  let existing = false,
    identity = '';
  function back() {
    probe = null;
    password = '';
    confirmed = false;
    error = '';
  }
  async function submit(event) {
    event.preventDefault();
    busy = true;
    error = '';
    try {
      const details = { ...form, port: Number(form.port) };
      if (existing) {
        const server = await api('/servers', {
          method: 'POST',
          body: JSON.stringify({ ...details, identity }),
        });
        await onConnected(server);
      } else if (!probe) {
        probe = await api('/servers/ssh/probe', { method: 'POST', body: JSON.stringify(details) });
      } else {
        const server = await api('/servers/connect', {
          method: 'POST',
          body: JSON.stringify({ ...details, token: probe.token, password, confirmed }),
        });
        password = '';
        await onConnected(server);
      }
    } catch (e) {
      error = e.message;
      // A connection attempt consumes the verification token. Never replay a
      // password or leave it available when the user edits the target server.
      if (probe) {
        probe = null;
        confirmed = false;
      }
    } finally {
      password = '';
      busy = false;
    }
  }
</script>

<dialog use:modal class="form-dialog" aria-labelledby="connect-title" on:cancel={onClose}>
  <header>
    <h2 id="connect-title">Conectar servidor</h2>
    <button class="icon-button" disabled={busy} on:click={onClose} aria-label="Cerrar formulario"
      ><X size={20} /></button
    >
  </header>
  <p class="muted">
    {existing
      ? 'Usa un acceso SSH que ya esté configurado en Nodes.'
      : 'Nodes crea una clave SSH exclusiva y la autoriza en tu servidor. La clave queda protegida y se gestiona internamente.'}
  </p>
  <form on:submit={submit} autocomplete="off">
    {#if !probe}
      <label class="field"
        >Nombre<input
          bind:value={form.name}
          placeholder="Producción"
          maxlength="80"
          required
          disabled={busy}
        /></label
      >
      <label class="field"
        >{existing ? 'Host o alias SSH' : 'IP o dominio del servidor'}<input
          bind:value={form.host}
          placeholder={existing ? 'mi-servidor' : '203.0.113.10'}
          required
          disabled={busy}
        /></label
      >
      <div class="form-pair">
        <label class="field">Usuario<input bind:value={form.user} required disabled={busy} /></label
        >
        <label class="field"
          >Puerto<input
            type="number"
            bind:value={form.port}
            min="1"
            max="65535"
            required
            disabled={busy}
          /></label
        >
      </div>
      {#if existing}
        <label class="field"
          >Ruta de identidad SSH existente<input
            bind:value={identity}
            placeholder="Opcional si está en la configuración de OpenSSH"
            disabled={busy}
          /></label
        >
      {:else}
        <p class="small muted">
          Primero comprobaremos la identidad del servidor, sin enviar contraseñas.
        </p>
      {/if}
    {:else}
      <div class="notice">
        <strong>{form.name}</strong>
        <p>{form.user}@{form.host}:{form.port}</p>
      </div>
      <label class="field" for="server-fingerprint">Huella SSH del servidor</label>
      <code id="server-fingerprint" class="server-fingerprint">{probe.fingerprint}</code>
      <p class="small muted">
        Compárala con la huella que muestra la consola de tu proveedor o un canal de confianza. Esta
        huella identifica al servidor.
      </p>
      <label class="remember-option"
        ><input type="checkbox" bind:checked={confirmed} required disabled={busy} /><span
          >He verificado la huella del servidor.</span
        ></label
      >
      <label class="field"
        >Contraseña SSH actual<input
          type="password"
          bind:value={password}
          autocomplete="off"
          maxlength="4096"
          required
          disabled={busy}
        /></label
      >
      <p class="small muted">
        Se usa una sola vez para autorizar el acceso y no se guarda. Necesitas una cuenta con acceso
        SSH mediante contraseña.
      </p>
      <p class="small muted">
        Las claves existentes del servidor se conservan. No se cambiará su contraseña ni la
        configuración de SSH.
      </p>
    {/if}
    {#if error}<p class="notice error" role="alert">{error}</p>{/if}
    <div class="button-row spaced">
      {#if probe}<button type="button" class="secondary" disabled={busy} on:click={back}
          ><ArrowLeft size={16} />Volver</button
        >{/if}
      <button class="primary" disabled={busy || (!!probe && !confirmed)}>
        {#if probe}<ShieldCheck size={17} />{/if}
        {busy
          ? probe
            ? 'Autorizando y comprobando…'
            : 'Comprobando…'
          : existing
            ? 'Conectar servidor'
            : probe
              ? 'Autorizar clave y conectar'
              : 'Verificar servidor'}<ArrowRight size={16} />
      </button>
    </div>
    {#if !probe}
      <button
        type="button"
        class="text-button access-mode"
        disabled={busy}
        on:click={() => {
          existing = !existing;
          error = '';
          password = '';
        }}
        >{existing
          ? 'Crear una clave dedicada automáticamente'
          : 'Ya tengo un acceso SSH configurado'}</button
      >
    {/if}
  </form>
</dialog>

<style>
  .server-fingerprint {
    display: block;
    padding: 0.8rem;
    border: 1px solid var(--line);
    border-radius: 8px;
    overflow-wrap: anywhere;
    font-size: 0.8rem;
  }
  .access-mode {
    margin-top: 1.2rem;
  }
  .notice p {
    margin-bottom: 0;
    overflow-wrap: anywhere;
  }
</style>
