<script>
  import { Copy, Check, ExternalLink } from '@lucide/svelte';
  export let deployment;
  export let server;
  export let pending = false;
  export let permissionError = '';
  export let onPublish;
  export let onActivate;
  let copied = '',
    copyError = '';
  const secrets = [
    ['HOST', 'IP pública de la VPS'],
    ['PORT', `Puerto SSH: ${server?.port || 22}`],
    ['USER', `Usuario SSH: ${server?.user || 'el de la conexión'}`],
    ['SSH_KEY', 'Clave privada dedicada, sin contraseña'],
    ['KNOWN_HOSTS', 'Huella SSH del servidor, verificada'],
  ];
  async function copy(value) {
    copyError = '';
    try {
      await navigator.clipboard.writeText(value);
      copied = value;
    } catch {
      copyError = 'No se pudo copiar. Selecciona el nombre del secreto y cópialo.';
    }
  }
</script>

<section class="gh-push-setup" aria-label={`Despliegue por push de ${deployment.name}`}>
  <div class="gh-field-heading">
    <strong>Despliegue por push</strong>
    <small>{deployment.pushConfigured ? 'Configurado en Nodes' : 'Opcional'}</small>
  </div>
  {#if deployment.pushConfigured}
    <p>Los pushes a <code>{deployment.branch}</code> inician el despliegue.</p>
    <a class="text-button" href={`https://github.com/${deployment.repository}/actions/workflows/${deployment.workflow}`} target="_blank" rel="noopener noreferrer">Abrir workflow en GitHub <ExternalLink size={14} /></a>
  {:else}
    <p>Los pushes a <code>{deployment.branch}</code> pueden desplegar esta aplicación con GitHub Actions.</p>
    <button type="button" class="secondary" disabled={pending} on:click={() => onActivate(deployment)}>Activar despliegue automático</button>
  {/if}
  {#if permissionError}
    <div class="notice warning" role="alert">
      <p>{permissionError}</p>
      <a class="text-button" href="https://github.com/settings/personal-access-tokens" target="_blank" rel="noopener noreferrer">Revisar permisos del token <ExternalLink size={14} /></a>
    </div>
  {/if}
  <details class="gh-inline-help">
    <summary>Configuración manual alternativa</summary>
    <ol class="gh-setup-steps">
      <li>
        <strong>Añade los secretos SSH</strong>
        <p>En GitHub → Settings → Secrets and variables → Actions.</p>
        <a
          class="secondary"
          href={`https://github.com/${deployment.repository}/settings/secrets/actions`}
          target="_blank"
          rel="noopener noreferrer">Abrir secretos en GitHub<ExternalLink size={14} /></a
        >
      </li>
      <li>
        <strong>Copia estos nombres y completa sus valores</strong>
        <div class="gh-secret-list">
          {#each secrets as [suffix, description]}<div>
              <span
                ><code>{deployment.secretPrefix}_{suffix}</code><small>{description}</small></span
              ><button
                type="button"
                class="icon-button"
                on:click={() => copy(`${deployment.secretPrefix}_${suffix}`)}
                aria-label={`Copiar nombre ${suffix}`}
                title="Copiar nombre"
                >{#if copied === `${deployment.secretPrefix}_${suffix}`}<Check
                    size={15}
                  />{:else}<Copy size={15} />{/if}</button
              >
            </div>{/each}
        </div>
        <p class="small muted">
          Usa una clave exclusiva para estos despliegues y autoriza su clave pública en {server?.name ||
            'la VPS'}. Verifica la huella antes de guardarla. Nodes no sube tus claves a GitHub.
        </p>
        {#if copyError}<p role="alert">{copyError}</p>{/if}
      </li>
      <li>
        <strong>Haz push a {deployment.branch}</strong>
        <p>Comprueba el resultado en GitHub Actions. Nodes puede estar cerrado.</p>
        <a
          class="text-button"
          href={`https://github.com/${deployment.repository}/actions`}
          target="_blank"
          rel="noopener noreferrer">Abrir Actions<ExternalLink size={14} /></a
        >
      </li>
    </ol>
  </details>
  <details class="gh-inline-help">
    <summary>Workflow y conexión</summary>
    {#if deployment.pushConfigured}<button type="button" class="secondary" disabled={pending} on:click={() => onActivate(deployment)}>Renovar configuración automática</button>{/if}
    <p><code>.github/workflows/{deployment.workflow}</code></p>
    <p>
      HOST debe ser una IP o dominio accesible desde GitHub; el alias SSH de tu PC no sirve. HOST,
      PORT y USER deben coincidir con la conexión de Nodes. Para detener los pushes, desactiva el
      workflow en Actions.
    </p>
    <button
      type="button"
      class="text-button"
      disabled={pending}
      on:click={() => onPublish(deployment)}>Volver a publicar workflow</button
    >
  </details>
</section>
