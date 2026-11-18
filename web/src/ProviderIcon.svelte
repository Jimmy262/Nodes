<script>
  import { SquareTerminal } from '@lucide/svelte';
  import codexLogo from './assets/providers/codex.svg';
  import claudeLogo from './assets/providers/claude.svg';
  export let kind = 'shell';
  export let size = 22;
</script>

{#if kind === 'shell'}<SquareTerminal {size} aria-hidden="true" />{:else}
  <span
    class="provider-mark"
    class:claude={kind === 'claude'}
    aria-hidden="true"
    style={`width:${size}px;height:${size}px;--provider-logo:url("${kind === 'claude' ? claudeLogo : codexLogo}")`}
  ></span>
{/if}

<style>
  .provider-mark {
    display: inline-block;
    flex-shrink: 0;
    background: currentColor;
    mask: var(--provider-logo) center / contain no-repeat;
    -webkit-mask: var(--provider-logo) center / contain no-repeat;
  }
  .claude {
    color: #c56e50;
  }
</style>
