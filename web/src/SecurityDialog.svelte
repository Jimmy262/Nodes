<script>
  import { onMount } from 'svelte';
  import { X, ShieldCheck, LogOut, Fingerprint, Plus, Trash2 } from '@lucide/svelte';
  import { modal } from './format.js';
  import { usePasskey } from './lib/passkeys.js';
  export let api;
  export let onClose;
  export let onLogout;
  export let onSignedOut;
  let session = null,
    keys = [],
    error = '',
    message = '',
    busy = false;
  let name = '',
    adding = false,
    removing = '';
  let active = true;
  async function load() {
    const [current, passkeys] = await Promise.all([api('/session'), api('/auth/passkeys')]);
    if (active) {
      session = current;
      keys = passkeys;
    }
  }
  onMount(() => {
    load().catch((e) => {
      if (active) error = e.message;
    });
    return () => {
      active = false;
    };
  });
  async function add(event) {
    event.preventDefault();
    busy = true;
    error = '';
    message = '';
    try {
      session = await usePasskey(api, { remember: session?.remember ?? true });
      session = await usePasskey(api, { register: true, remember: session.remember, name });
      await load();
      adding = false;
      name = '';
      message = 'Passkey añadida. Ya puedes usarla para entrar.';
    } catch (e) {
      error = e.message;
    } finally {
      busy = false;
    }
  }
  async function remove(id) {
    busy = true;
    error = '';
    message = '';
    try {
      session = await usePasskey(api, { remember: session?.remember ?? true });
      await api(`/auth/passkeys/${encodeURIComponent(id)}`, { method: 'DELETE' });
      removing = '';
      await load();
      message =
        'Passkey eliminada y otras sesiones cerradas. Puedes borrarla también de tu gestor de passkeys.';
    } catch (e) {
      error = e.message;
    } finally {
      busy = false;
    }
  }
  async function logoutAll() {
    busy = true;
    error = '';
    try {
      await api('/auth/logout-all', { method: 'POST' });
      onSignedOut();
    } catch (e) {
      error = e.message;
    } finally {
      busy = false;
    }
  }
  function date(seconds) {
    return new Date(seconds * 1000).toLocaleDateString('es-PE');
  }
</script>

<dialog
  use:modal
  class="form-dialog security-dialog"
  aria-labelledby="security-title"
  on:cancel={onClose}
>
  <header>
    <h2 id="security-title">Acceso y seguridad</h2>
    <button class="icon-button" on:click={onClose} aria-label="Cerrar seguridad"
      ><X size={20} /></button
    >
  </header>
  <div class="security-session">
    <ShieldCheck size={23} strokeWidth={1.6} />
    <div>
      <strong
        >{session
          ? session.remember
            ? 'Este navegador recuerda tu sesión'
            : 'Sesión del navegador'
          : 'Comprobando sesión…'}</strong
      >
      {#if session}<p>
          Válida hasta el {new Date(session.expiresAt).toLocaleString('es-PE')}.
        </p>{/if}
    </div>
  </div>
  {#if message}<p class="notice success" role="status">{message}</p>{/if}
  {#if error}<p class="notice error" role="alert">{error}</p>{/if}
  <h3>Tus passkeys</h3>
  <p class="small muted">
    Registra una segunda passkey para conservar el acceso si pierdes un dispositivo.
  </p>
  <ul class="passkey-list">
    {#each keys as key (key.id)}
      <li>
        <Fingerprint size={20} />
        <div>
          <strong>{key.name}</strong>
          <p class="small muted">
            Creada: {date(key.createdAt)} · Último uso: {date(key.lastUsedAt)}
          </p>
        </div>
        <button
          class="icon-button"
          disabled={busy || keys.length < 2}
          aria-label={`Eliminar ${key.name}`}
          title={keys.length < 2
            ? 'Registra otra passkey antes de eliminar esta'
            : 'Eliminar passkey'}
          on:click={() => {
            removing = key.id;
            adding = false;
          }}><Trash2 size={17} /></button
        >
      </li>
      {#if removing === key.id}
        <li class="passkey-confirm">
          <p>¿Eliminar «{key.name}»? Se cerrarán las otras sesiones.</p>
          <div class="button-row">
            <button class="secondary danger-text" disabled={busy} on:click={() => remove(key.id)}
              >{busy ? 'Verificando…' : 'Verificar y eliminar'}</button
            ><button class="secondary" disabled={busy} on:click={() => (removing = '')}
              >Cancelar</button
            >
          </div>
        </li>
      {/if}
    {/each}
  </ul>
  {#if adding}
    <form on:submit={add}>
      <label class="field"
        >Nombre de la passkey<input
          bind:value={name}
          maxlength="80"
          placeholder="Por ejemplo, mi celular"
          required
          disabled={busy}
        /></label
      >
      <p class="small muted">
        Primero verificaremos tu acceso actual; después podrás guardar la nueva passkey.
      </p>
      <div class="button-row spaced">
        <button class="primary" disabled={busy}
          >{busy ? 'Esperando tu dispositivo…' : 'Continuar'}</button
        ><button type="button" class="secondary" disabled={busy} on:click={() => (adding = false)}
          >Cancelar</button
        >
      </div>
    </form>
  {:else}
    <button
      class="secondary"
      disabled={busy || !session || keys.length >= 10}
      on:click={() => {
        adding = true;
        removing = '';
      }}><Plus size={16} />Añadir passkey</button
    >
  {/if}
  <div class="security-actions">
    <button class="text-button" disabled={busy} on:click={onLogout}
      ><LogOut size={16} />Cerrar esta sesión</button
    >
    <button class="text-button danger-text" disabled={busy} on:click={logoutAll}
      >Cerrar todas las sesiones</button
    >
  </div>
</dialog>

<style>
  .passkey-list {
    list-style: none;
    margin: 1rem 0;
    padding: 0;
  }
  .passkey-list li {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.8rem 0;
    border-bottom: 1px solid var(--line);
  }
  .passkey-list li > div {
    flex: 1;
    min-width: 0;
  }
  .passkey-list strong {
    overflow-wrap: anywhere;
  }
  .passkey-list p {
    margin: 0.25rem 0 0;
  }
  .passkey-list .passkey-confirm {
    display: block;
  }
  .passkey-confirm .button-row {
    margin-top: 0.7rem;
  }
</style>
