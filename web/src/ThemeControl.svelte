<script>
  import { Palette, X, Check } from '@lucide/svelte';
  import { modal } from './format.js';
  import { theme, palettes, selectTheme } from './lib/themes.js';
  import BrandLogo from './BrandLogo.svelte';
  export let placement = 'sidebar';
  let open = false;
</script>

<button
  type="button"
  class="appearance-shortcut"
  class:login-appearance={placement === 'login'}
  on:click={() => (open = true)}
  aria-label="Apariencia"
  title="Cambiar colores"
  aria-haspopup="dialog"
>
  <Palette size={18} /><span class="appearance-label">Apariencia</span>
</button>

{#if open}
  <dialog
    class="appearance-dialog"
    use:modal
    aria-labelledby="appearance-title"
    aria-describedby="appearance-description"
    on:cancel={(event) => {
      event.preventDefault();
      open = false;
    }}
  >
    <header class="appearance-heading">
      <div>
        <h2 id="appearance-title">Apariencia</h2>
        <p id="appearance-description">Elige la paleta de Nodes.</p>
      </div>
      <button
        class="icon-button"
        type="button"
        aria-label="Cerrar apariencia"
        on:click={() => (open = false)}><X size={21} /></button
      >
    </header>
    <fieldset class="palette-grid">
      <legend class="sr-only">Paleta de colores</legend>
      {#each palettes as palette, index}
        <label class="palette-option" class:chosen={$theme.id === palette.id}>
          <input
            type="radio"
            name="nodes-palette"
            value={palette.id}
            checked={$theme.id === palette.id}
            on:change={() => selectTheme(palette.id)}
          />
          <span class="palette-strip" aria-hidden="true">
            <i style:background={palette.accent}></i><i style:background={palette.secondary}></i><i
              style:background={palette.paper}
            ></i>
          </span>
          <span class="palette-label"
            ><small>{String(index + 1).padStart(2, '0')}</small>{palette.name}</span
          >
          {#if $theme.id === palette.id}<span class="palette-check" aria-hidden="true"
              ><Check size={13} /></span
            >{/if}
        </label>
      {/each}
    </fieldset>
    <div class="appearance-preview" aria-label="Vista previa de la marca">
      <BrandLogo /><span>{$theme.name}</span>
    </div>
    <footer class="appearance-footer">
      <p>
        Se guarda en este navegador.<br /><span
          >Los estados de seguridad conservan sus colores.</span
        >
      </p>
      <button type="button" class="primary" on:click={() => (open = false)}>Listo</button>
    </footer>
  </dialog>
{/if}
