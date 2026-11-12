<script>
  import { Pencil, Check } from '@lucide/svelte';
  export let server;
  export let api;
  export let onSaved;
  let editing = false,
    name = '',
    error = '',
    pending = false;
  async function save(event) {
    event.preventDefault();
    pending = true;
    error = '';
    try {
      await api(`/servers/${server.id}`, { method: 'PATCH', body: JSON.stringify({ name }) });
      await onSaved();
      editing = false;
    } catch (e) {
      error = e.message;
    } finally {
      pending = false;
    }
  }
</script>

{#if editing}
  <form class="rename-form" on:submit={save}>
    <label class="field"
      >Nombre del servidor<input bind:value={name} required maxlength="80" /></label
    >
    <div class="button-row">
      <button class="primary" disabled={pending || !name.trim()}
        ><Check size={16} />{pending ? 'Guardando…' : 'Guardar nombre'}</button
      ><button type="button" class="secondary" disabled={pending} on:click={() => (editing = false)}
        >Cancelar</button
      >
    </div>
    {#if error}<p class="notice error" role="alert">{error}</p>{/if}
  </form>
{:else}
  <button
    class="text-button"
    on:click={() => {
      name = server.name;
      error = '';
      editing = true;
    }}><Pencil size={14} />Editar nombre</button
  >
{/if}
