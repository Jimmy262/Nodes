<script>
  import { GitBranch, RefreshCw, Search, ChevronRight, LockKeyhole } from '@lucide/svelte';
  export let repositories = [],
    value = '',
    loading = false,
    error = '',
    hasMore = false;
  export let onSelect, onRefresh, onMore, onLookup;
  let query = '',
    choosing = !value,
    previousValue = value;
  $: selected = repositories.find((repo) => repo.full_name === value);
  $: filtered = repositories.filter((repo) =>
    repo.full_name.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()),
  );
  $: if (value !== previousValue) {
    previousValue = value;
    choosing = false;
    query = '';
  }
  function choose(repo) {
    value = repo.full_name;
    choosing = false;
    query = '';
    onSelect(value);
  }
</script>

<section class="github-repository-picker" aria-label="Repositorio de GitHub">
  <div class="gh-field-heading">
    <span>Repositorio</span><button
      type="button"
      class="icon-button"
      disabled={loading}
      on:click={onRefresh}
      aria-label="Actualizar repositorios"
      title="Actualizar repositorios"
      ><RefreshCw size={16} class={loading ? 'spinner' : ''} /></button
    >
  </div>
  {#if selected && !choosing}
    <div class="gh-selected-repository">
      <GitBranch size={19} />
      <div>
        <strong>{selected.full_name}</strong><span
          >{selected.private ? 'Privado' : 'Público'} · rama {selected.default_branch ||
            'main'}</span
        >
      </div>
      <button type="button" class="text-button" on:click={() => (choosing = true)}>Cambiar</button>
    </div>
  {:else}
    <div class="gh-search">
      <Search size={16} /><input
        type="search"
        aria-label="Buscar repositorio"
        placeholder="Buscar o pegar una URL de GitHub"
        bind:value={query}
        on:keydown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            if (filtered.length === 1) choose(filtered[0]);
            else if (query.includes('/')) onLookup(query);
          }
        }}
      />
    </div>
    <div class="gh-repository-results" aria-busy={loading}>
      {#if loading && !repositories.length}<p class="small muted" role="status">
          Cargando repositorios…
        </p>{/if}
      {#each filtered as repo}<button
          type="button"
          class="gh-repository-option"
          on:click={() => choose(repo)}
          ><GitBranch size={16} /><span>{repo.full_name}</span>{#if repo.private}<LockKeyhole
              size={13}
            />{/if}<ChevronRight size={15} /></button
        >{/each}
      {#if !filtered.length && !loading}<p class="small muted">
          {query ? 'No hay coincidencias.' : 'No hay repositorios autorizados.'}
        </p>{/if}
    </div>
    {#if hasMore}<button type="button" class="text-button" disabled={loading} on:click={onMore}
        >Cargar más repositorios</button
      >{/if}
    {#if query.trim() && !filtered.length}<button
        type="button"
        class="secondary"
        disabled={loading || !query.includes('/')}
        on:click={() => onLookup(query)}
        >{loading ? 'Comprobando…' : 'Comprobar URL o usuario/repositorio'}</button
      >{/if}
    {#if selected}<button type="button" class="text-button" on:click={() => (choosing = false)}
        >Conservar selección</button
      >{/if}
  {/if}
  {#if error}<p class="notice error" role="alert">{error}</p>{/if}
  <details class="gh-inline-help">
    <summary>No encuentro mi repositorio</summary>
    <p>
      Autorízalo en <a
        href="https://github.com/settings/personal-access-tokens"
        target="_blank"
        rel="noopener noreferrer">Repository access del token</a
      > y actualiza. También puedes pegar su URL en el buscador.
    </p>
  </details>
</section>
