<script>
  import { ArrowRight, LockKeyhole, Eye, EyeOff } from '@lucide/svelte';
  export let api;
  export let state;
  export let initialError = '';
  export let onAuthenticated;
  let password = '',
    confirmation = '',
    remember = true,
    visible = false,
    busy = false,
    error = '';
  $: setup = !state.configured;
  async function submit(event) {
    event.preventDefault();
    error = '';
    if (setup && password !== confirmation) {
      error = 'Los PIN no coinciden';
      return;
    }
    busy = true;
    try {
      await api(setup ? '/auth/setup' : '/login', {
        method: 'POST',
        body: JSON.stringify(
          setup ? { password, confirmPassword: confirmation, remember } : { password, remember },
        ),
      });
      password = '';
      confirmation = '';
      visible = false;
      await onAuthenticated();
    } catch (e) {
      error = e.message;
    } finally {
      busy = false;
    }
  }
</script>

<main class="login-shell">
  <form class="login-form" on:submit={submit}>
    <span class="wordmark">nodo<span>.</span></span>
    <h1>{setup ? 'Crea tu PIN' : 'Entrar a Nodo'}</h1>
    <p class="muted">
      {setup
        ? 'Elige 8 números. Tu PIN seguirá funcionando cuando reinicies Nodo.'
        : 'Entra con tu PIN para ver tus servidores y aplicaciones.'}
    </p>
    {#if !setup || state.setupAuthorized}
      <label class="field" for="nodo-password"
        >{setup ? 'Crea tu PIN' : 'PIN'}
        <span class="password-field"
          ><input
            id="nodo-password"
            name="password"
            type={visible ? 'text' : 'password'}
            bind:value={password}
            required
            minlength="8"
            maxlength="8"
            inputmode="numeric"
            pattern={'[0-9]{8}'}
            title="Escribe exactamente 8 números, sin espacios ni letras"
            autocomplete={setup ? 'new-password' : 'current-password'}
            placeholder={setup ? '8 números' : 'Tu PIN de Nodo'}
          /><button
            type="button"
            class="icon-button"
            on:click={() => (visible = !visible)}
            aria-label={visible ? 'Ocultar PIN' : 'Mostrar PIN'}
            >{#if visible}<EyeOff size={17} />{:else}<Eye size={17} />{/if}</button
          ></span
        >
      </label>
      {#if setup}<label class="field" for="nodo-confirm"
          >Repite tu PIN<input
            id="nodo-confirm"
            name="confirmPassword"
            type="password"
            bind:value={confirmation}
            required
            minlength="8"
            maxlength="8"
            inputmode="numeric"
            pattern={'[0-9]{8}'}
            title="Repite los mismos 8 números"
            autocomplete="new-password"
            placeholder="Escríbelo una vez más"
          /></label
        >{/if}
      <label class="remember-option"
        ><input type="checkbox" bind:checked={remember} /><span
          >Recordarme durante 30 días<small
            >En este navegador. Úsalo en tu equipo de confianza.</small
          ></span
        ></label
      >
      {#if error || initialError}<p class="notice error" role="alert">
          {error || initialError}
        </p>{/if}
      <button class="primary" disabled={busy}
        >{busy ? 'Comprobando…' : setup ? 'Guardar PIN y entrar' : 'Entrar al panel'}<ArrowRight
          size={17}
        /></button
      >
      <p class="small muted auth-local">
        <LockKeyhole size={13} /> Acceso local · protección de Windows
      </p>
    {:else}
      <div class="notice warning">
        Abre el enlace privado de configuración que aparece en la consola al iniciar Nodo. Así solo
        quien inició el programa puede crear el PIN.
      </div>
      {#if initialError}<p class="notice error" role="alert">{initialError}</p>{/if}
    {/if}
    {#if !setup}<details class="recovery-help">
        <summary>¿Olvidaste tu PIN?</summary>
        <p>Detén Nodo y ejecuta este comando desde la carpeta del proyecto:</p>
        <code>.\bin\nodo.exe -reset-password</code>
        <p>
          Abre el enlace privado que aparecerá para crear otro PIN. Tus servidores e históricos se
          conservan.
        </p>
      </details>{/if}
  </form>
</main>
