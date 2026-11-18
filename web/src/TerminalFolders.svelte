<script>
  import { onMount, onDestroy } from 'svelte';
  import { Folder, ArrowUp, House, ArrowRight, RefreshCw } from '@lucide/svelte';
  export let api;
  export let server;
  export let initialPath = '';
  export let onSelect;
  let folder = null,
    path = initialPath,
    pending = false,
    error = '',
    generation = 0;
  async function load(next = '') {
    const version = ++generation;
    pending = true;
    error = '';
    try {
      const data = await api(`/servers/${server.id}/directories?path=${encodeURIComponent(next)}`);
      if (version !== generation) return;
      folder = data;
      path = data.path;
    } catch (e) {
      if (version === generation) error = e.message;
    } finally {
      if (version === generation) pending = false;
    }
  }
  onMount(() => {
    load(initialPath);
  });
  onDestroy(() => {
    generation++;
  });
</script>

<aside class="terminal-folders" aria-label="Carpetas del servidor">
  <form class="folder-location" on:submit|preventDefault={() => load(path)}>
    <div class="folder-navigation">
      <button
        type="button"
        class="icon-button"
        disabled={pending}
        on:click={() => load('')}
        aria-label="Carpeta personal"
        title="Carpeta personal"><House size={17} /></button
      >
      <button
        type="button"
        class="icon-button"
        disabled={pending || !folder || folder.path === folder.parent}
        on:click={() => load(folder.parent)}
        aria-label="Subir una carpeta"
        title="Subir una carpeta"><ArrowUp size={17} /></button
      >
      <span>Carpetas</span>
      <button
        type="button"
        class="icon-button"
        disabled={pending}
        on:click={() => load(folder?.path || '')}
        aria-label="Actualizar carpetas"
        title="Actualizar"><RefreshCw size={15} /></button
      >
    </div>
    <label class="sr-only" for="terminal-folder-path">Ruta de la carpeta</label>
    <div class="folder-path">
      <input
        id="terminal-folder-path"
        bind:value={path}
        placeholder="/ruta/del/proyecto"
        autocomplete="off"
        spellcheck="false"
      /><button
        class="icon-button"
        disabled={pending || !path.startsWith('/')}
        aria-label="Ir a la carpeta"
        title="Ir"><ArrowRight size={16} /></button
      >
    </div>
  </form>
  <div class="folder-entries" aria-busy={pending}>
    {#if pending}<p role="status">Cargando carpetas…</p>{:else if error}<p role="alert">
        {error}
      </p>{:else if folder}
      {#each folder.entries as entry}<button
          type="button"
          class="folder-entry"
          on:click={() => load(entry.path)}
          title={entry.path}><Folder size={17} /><span>{entry.name}</span></button
        >{:else}<p>Sin subcarpetas.</p>{/each}
      {#if folder.truncated}<p>
          Se muestran 500 carpetas. Puedes escribir una ruta para abrir otra.
        </p>{/if}
    {/if}
  </div>
  <footer>
    <button
      type="button"
      class="primary"
      disabled={pending || !!error || !folder}
      on:click={() => onSelect(folder.path)}>Abrir aquí<ArrowRight size={16} /></button
    >
    <p>La sesión anterior se conserva.</p>
  </footer>
</aside>
