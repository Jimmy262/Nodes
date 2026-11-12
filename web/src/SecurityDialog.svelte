<script>
  import { onMount } from 'svelte';
  import { X, ShieldCheck, LogOut, KeyRound } from '@lucide/svelte';
  import { modal } from './format.js';
  export let api;
  export let onClose;
  export let onLogout;
  export let onSignedOut;
  let session = null,
    error = '',
    message = '',
    changing = false,
    busy = false;
  let currentPassword = '',
    password = '',
    confirmation = '';
  let active = true;
  onMount(() => {
    api('/session')
      .then((data) => {
        if (active) session = data;
      })
      .catch((e) => {
        if (active) error = e.message;
      });
    return () => (active = false);
  });
  async function change(event) {
    event.preventDefault();
    error = '';
    message = '';
    if (password !== confirmation) {
      error = 'Los PIN no coinciden';
      return;
    }
    busy = true;
    try {
      const data = await api('/auth/password', {
        method: 'POST',
        retainSessionOn401: true,
        body: JSON.stringify({ currentPassword, password, confirmPassword: confirmation }),
      });
      session = data;
      currentPassword = '';
      password = '';
      confirmation = '';
      changing = false;
      message = 'PIN actualizado. Las otras sesiones quedaron cerradas.';
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
      >{#if session}<p>
          Válida hasta el {new Date(session.expiresAt).toLocaleString('es-PE', {
            day: 'numeric',
            month: 'long',
            hour: '2-digit',
            minute: '2-digit',
          })}.
        </p>
        <p>Reiniciar Nodo no cierra esta sesión.</p>{/if}
    </div>
  </div>
  {#if message}<p class="notice success" role="status">{message}</p>{/if}
  {#if error}<p class="notice error" role="alert">{error}</p>{/if}
  {#if changing}
    <form on:submit={change}>
      <label class="field"
        >PIN actual<input
          type="password"
          bind:value={currentPassword}
          autocomplete="current-password"
          required
          maxlength="8"
          inputmode="numeric"
          pattern={'[0-9]{8}'}
          title="Escribe exactamente 8 números"
        /></label
      >
      <label class="field"
        >Nuevo PIN<input
          type="password"
          bind:value={password}
          autocomplete="new-password"
          required
          minlength="8"
          maxlength="8"
          inputmode="numeric"
          pattern={'[0-9]{8}'}
          title="Escribe exactamente 8 números"
          placeholder="8 números"
        /></label
      >
      <label class="field"
        >Repite el nuevo PIN<input
          type="password"
          bind:value={confirmation}
          autocomplete="new-password"
          required
          minlength="8"
          maxlength="8"
          inputmode="numeric"
          pattern={'[0-9]{8}'}
          title="Repite los mismos 8 números"
        /></label
      >
      <p class="small muted">Al cambiarlo, se cerrarán las demás sesiones.</p>
      <div class="button-row spaced">
        <button class="primary" disabled={busy}>{busy ? 'Guardando…' : 'Guardar PIN'}</button
        ><button
          type="button"
          class="secondary"
          disabled={busy}
          on:click={() => {
            changing = false;
            currentPassword = '';
            password = '';
            confirmation = '';
            error = '';
          }}>Cancelar</button
        >
      </div>
    </form>
  {:else}<button
      class="secondary"
      on:click={() => {
        changing = true;
        error = '';
        message = '';
      }}><KeyRound size={16} />Cambiar PIN</button
    >{/if}
  <div class="security-actions">
    <button class="text-button" disabled={busy} on:click={onLogout}
      ><LogOut size={16} />Cerrar esta sesión</button
    ><button class="text-button danger-text" disabled={busy} on:click={logoutAll}
      >Cerrar todas las sesiones</button
    >
  </div>
</dialog>
