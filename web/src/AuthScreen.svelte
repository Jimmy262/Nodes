<script>
  import { ArrowRight, Fingerprint, ShieldCheck } from '@lucide/svelte';
  import BrandLogo from './BrandLogo.svelte';
  import ThemeControl from './ThemeControl.svelte';
  import { passkeysSupported, usePasskey } from './lib/passkeys.js';
  export let api;
  export let state;
  export let initialError = '';
  export let onAuthenticated;
  let remember = true,
    busy = false,
    error = '';
  const supported = passkeysSupported();
  $: setup = !state.configured;
  async function submit(event) {
    event.preventDefault();
    error = '';
    busy = true;
    try {
      await usePasskey(api, { register: setup, remember });
      await onAuthenticated();
    } catch (e) {
      error = e.message;
    } finally {
      busy = false;
    }
  }
</script>

<main class="login-shell">
  <ThemeControl placement="login" />
  <form class="login-form" on:submit={submit}>
    <BrandLogo />
    <h1>{setup ? 'Crea tu acceso' : 'Entrar a Nodes'}</h1>
    <p class="muted">
      {setup
        ? 'Guarda una passkey para entrar con tu huella, rostro o el desbloqueo de tu dispositivo.'
        : 'Usa tu passkey para acceder a tus servidores y aplicaciones.'}
    </p>
    {#if !supported || state.passkeyAvailable === false}
      <p class="notice warning" role="status">
        Necesitas un navegador compatible y el dominio HTTPS de Nodes. En tu computadora, abre
        <a href={`http://localhost:${location.port || '8080'}`}>Nodes en localhost</a>. Para el
        primer acceso, usa el enlace privado de la consola con ese mismo dominio.
      </p>
    {:else if !setup || state.setupAuthorized}
      <label class="remember-option">
        <input type="checkbox" bind:checked={remember} disabled={busy} />
        <span
          >Recordar este navegador durante 30 días<small
            >Solo en tus dispositivos de confianza.</small
          ></span
        >
      </label>
      <button class="primary" disabled={busy}>
        <Fingerprint size={20} />{busy
          ? 'Esperando tu dispositivo…'
          : setup
            ? 'Crear passkey y entrar'
            : 'Entrar con passkey'}<ArrowRight size={17} />
      </button>
      <p class="small muted auth-local">
        <ShieldCheck size={14} /> Tu dispositivo verifica tu identidad.
      </p>
      <p class="small muted">
        También puedes usar una passkey de tu celular o una llave de seguridad.
      </p>
    {:else}
      <p class="notice warning">
        Abre el enlace privado que aparece en la consola al iniciar Nodes para registrar tu primera
        passkey. Caduca a los 15 minutos del arranque y solo se puede abrir una vez.
      </p>
    {/if}
    {#if error || initialError}<p class="notice error" role="alert">{error || initialError}</p>{/if}
    {#if !setup}
      <details class="recovery-help">
        <summary>¿No tienes acceso a tu passkey?</summary>
        <p>Prueba otra passkey registrada o el dispositivo donde la guardaste.</p>
        <p>
          Si perdiste todas, detén Nodes y ejecuta su binario en el servidor con <code
            >-reset-access -data ruta/a/panel.db</code
          >, usando la misma base de datos y configuración de inicio.
        </p>
        <p>
          El comando revoca las passkeys y sesiones, conserva tus servidores y genera un enlace
          privado para registrar un nuevo acceso.
        </p>
      </details>
    {/if}
  </form>
</main>
