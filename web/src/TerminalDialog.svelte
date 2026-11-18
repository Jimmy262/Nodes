<script>
  import { onMount, tick } from 'svelte';
  import { X, RefreshCw, Keyboard, Send, SquareTerminal } from '@lucide/svelte';
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import '@xterm/xterm/css/xterm.css';
  import { modal } from './format.js';
  import { terminalInputFrames } from './lib/terminalInput.js';
  import { theme, themeTokens } from './lib/themes.js';

  export let server;
  export let kind;
  export let api;
  export let onClose;
  let surface, term, fit, socket;
  let state = 'Conectando',
    error = '',
    draft = '',
    connected = false;
  let requirements = null,
    installPlan = null,
    installAck = false,
    installPending = false;
  let installJob = null,
    installMessage = '',
    pollingInstall = false;
  let active = true,
    connecting = false,
    attempts = 0,
    retryTimer,
    stableTimer;
  let resizeFrame = null;
  let controlsOpen = false;
  let height = window.innerHeight - 24;
  const keys = [
    ['Ctrl+C', '\x03', 'Interrumpir con Ctrl+C'],
    ['Esc', '\x1b', 'Escape'],
    ['Tab', '\t', 'Tabulador'],
    ['↑', '\x1b[A', 'Flecha arriba'],
    ['↓', '\x1b[B', 'Flecha abajo'],
    ['←', '\x1b[D', 'Flecha izquierda'],
    ['→', '\x1b[C', 'Flecha derecha'],
    ['Enter', '\r', 'Enviar Enter'],
  ];
  function send(data) {
    if (!connected || socket?.readyState !== WebSocket.OPEN || socket.bufferedAmount > 65536)
      return false;
    if (data.length > 65536) {
      error = 'El texto es demasiado largo. Envíalo en partes más pequeñas.';
      return false;
    }
    const frames = terminalInputFrames(data);
    const encoder = new TextEncoder();
    const size = frames.reduce((total, frame) => total + encoder.encode(frame).length, 0);
    if (socket.bufferedAmount + size > 65536) {
      error = 'Hay demasiado texto pendiente. Espera y vuelve a enviarlo en partes más pequeñas.';
      return false;
    }
    for (const frame of frames) socket.send(frame);
    return true;
  }
  function resize() {
    height = Math.max(180, (window.visualViewport?.height || window.innerHeight) - 24);
    if (resizeFrame !== null) return;
    resizeFrame = requestAnimationFrame(() => {
      resizeFrame = null;
      if (!active || !term || requirements) return;
      fit.fit();
      if (connected && socket?.readyState === WebSocket.OPEN) {
        socket.send(
          JSON.stringify({
            type: 'resize',
            cols: Math.max(20, Math.min(500, term.cols)),
            rows: Math.max(5, Math.min(200, term.rows)),
          }),
        );
      }
    });
  }
  async function toggleControls() {
    controlsOpen = !controlsOpen;
    await tick();
    resize();
  }
  function submit(event) {
    event.preventDefault();
    if (!draft.trim()) return;
    const text = draft.replace(/\r\n/g, '\n');
    const data =
      kind === 'codex' ? `\x1b[200~${text}\x1b[201~\r` : text.replace(/\n/g, '\r') + '\r';
    if (send(data)) draft = '';
  }
  async function connect() {
    if (!active || connecting || connected || socket?.readyState === WebSocket.CONNECTING) return;
    connecting = true;
    clearTimeout(retryTimer);
    error = '';
    state = attempts ? 'Reconectando' : 'Conectando';
    try {
      const session = await api(`/servers/${server.id}/terminal`, {
        method: 'POST',
        body: JSON.stringify({ kind }),
      });
      if (!active) return;
      if (session.requirements) {
        requirements = session.requirements;
        state = 'Requisitos pendientes';
        return;
      }
      requirements = null;
      const url = new URL(`/api/servers/${server.id}/terminal/${session.id}`, location.href);
      url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const ws = new WebSocket(url);
      socket = ws;
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => {
        if (!active) {
          ws.close();
          return;
        }
        connected = true;
        state = 'Conectado';
        stableTimer = setTimeout(() => {
          attempts = 0;
        }, 10000);
        term.reset();
        resize();
      };
      ws.onmessage = ({ data }) => {
        if (!active) return;
        if (data instanceof ArrayBuffer) term.write(new Uint8Array(data));
      };
      ws.onclose = (event) => {
        clearTimeout(stableTimer);
        connected = false;
        if (!active) return;
        state = 'Desconectado';
        if (event.code === 4001) {
          error = 'Tu acceso caducó o se cerró. Inicia sesión de nuevo.';
        } else if (event.code !== 1000 && attempts < 3) {
          attempts += 1;
          state = 'Reconectando';
          retryTimer = setTimeout(connect, 1000 * 2 ** (attempts - 1));
        } else {
          error =
            event.code === 1000
              ? 'La sesión terminó. Puedes abrirla nuevamente.'
              : 'Se perdió la conexión. Puedes volver a conectar para retomar la sesión.';
        }
      };
      ws.onerror = () => {
        state = 'No se pudo conectar';
      };
    } catch (e) {
      if (active) {
        state = 'No se pudo conectar';
        error = e.message;
      }
    } finally {
      connecting = false;
    }
  }
  async function reviewInstall(packageID) {
    if (installPending || installJob) return;
    installPending = true;
    error = '';
    try {
      if (installPlan) await api(`/operations/${installPlan.id}/plan`, { method: 'DELETE' });
      installPlan = await api(`/servers/${server.id}/terminal/install/plan`, {
        method: 'POST',
        body: JSON.stringify({ package: packageID }),
      });
      installAck = false;
    } catch (e) {
      if (active) error = e.message;
    } finally {
      installPending = false;
    }
  }
  async function executeInstall() {
    if (!installPlan || !installAck || installPending || installJob) return;
    installPending = true;
    error = '';
    try {
      const result = await api(`/operations/${installPlan.id}/execute`, {
        method: 'POST',
        body: JSON.stringify({ name: installPlan.name, acknowledged: installAck }),
      });
      installJob = result.id;
      installMessage = 'Instalación en curso. Puedes seguirla también en Actividad.';
      installPlan = null;
    } catch (e) {
      if (active) error = e.message;
    } finally {
      installPending = false;
    }
  }
  async function pollInstallation() {
    if (!active || !installJob || pollingInstall) return;
    pollingInstall = true;
    try {
      const jobs = await api('/operations');
      if (!active) return;
      const job = jobs.find((item) => item.id === installJob);
      if (!job) return;
      installMessage = job.message;
      if (job.status !== 'running') {
        installJob = null;
        requirements = await api(`/servers/${server.id}/terminal/options`);
        if (job.status === 'succeeded') connect();
        else error = job.message;
      }
    } catch (e) {
      if (active) error = e.message;
    } finally {
      pollingInstall = false;
    }
  }
  onMount(() => {
    term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Consolas, Menlo, monospace',
      scrollback: 2000,
      theme: { background: '#140e21', foreground: '#F4F2F8', cursor: '#C7A5FF' },
    });
    const unsubscribeTheme = theme.subscribe((palette) => {
      const colors = themeTokens(palette);
      term.options.theme = {
        background: colors['terminal-bg'],
        foreground: colors['terminal-text'],
        cursor: colors['terminal-cursor'],
        selectionBackground: '#FFFFFF33',
      };
    });
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(surface);
    const input = term.onData(send);
    const observer = new ResizeObserver(resize);
    observer.observe(surface);
    window.visualViewport?.addEventListener('resize', resize);
    window.addEventListener('online', connect);
    resize();
    connect();
    const installTimer = setInterval(pollInstallation, 2000);
    return () => {
      unsubscribeTheme();
      active = false;
      if (resizeFrame !== null) cancelAnimationFrame(resizeFrame);
      clearTimeout(retryTimer);
      clearTimeout(stableTimer);
      clearInterval(installTimer);
      if (installPlan)
        api(`/operations/${installPlan.id}/plan`, { method: 'DELETE' }).catch(() => {});
      socket?.close(1000, 'Vista cerrada');
      observer.disconnect();
      window.visualViewport?.removeEventListener('resize', resize);
      window.removeEventListener('online', connect);
      input.dispose();
      term.dispose();
    };
  });
</script>

<dialog
  use:modal
  class="terminal-dialog"
  style={`height:${height}px`}
  aria-labelledby="terminal-title"
  on:cancel|preventDefault
>
  <header class="terminal-header">
    <h2 id="terminal-title">
      <SquareTerminal size={17} /><span
        >{kind === 'codex' ? 'Codex' : 'Terminal'} · {server.name}</span
      >
    </h2>
    <span
      class="terminal-connection"
      class:is-connected={connected}
      role="status"
      aria-label={state}
      title={state}><i aria-hidden="true"></i><span>{state}</span></span
    >
    <div class="terminal-actions">
      {#if !connected && !requirements}<button
          type="button"
          class="icon-button"
          disabled={connecting || state === 'Reconectando'}
          on:click={() => {
            attempts = 0;
            connect();
          }}
          aria-label="Reconectar terminal"
          title="Reconectar"><RefreshCw size={17} /></button
        >{/if}
      <button
        type="button"
        class="icon-button"
        class:active={controlsOpen}
        disabled={!!requirements}
        on:click={toggleControls}
        aria-expanded={controlsOpen}
        aria-controls="terminal-controls"
        aria-label={controlsOpen
          ? 'Ocultar controles de terminal'
          : 'Mostrar controles de terminal'}
        title="Teclas y campo de texto"><Keyboard size={19} /></button
      >
      <button
        type="button"
        class="icon-button"
        on:click={onClose}
        aria-label="Cerrar vista de terminal"
        title="Cerrar vista · La sesión se conserva en el servidor"><X size={20} /></button
      >
    </div>
  </header>
  {#if error}<div class="terminal-feedback" role="alert">{error}</div>{/if}
  {#if requirements}<section class="terminal-requirements" aria-label="Requisitos del servidor">
      <h3>Preparar {kind === 'codex' ? 'Codex' : 'la terminal'} en {server.name}</h3>
      <p>
        Comprueba e instala los requisitos que faltan. Cada instalación se revisa antes de
        ejecutarse.
      </p>
      {#each requirements.dependencies.filter((dependency) => kind === 'codex' || dependency.id === 'tmux') as dependency}
        <div class="terminal-dependency">
          <div>
            <strong>{dependency.name}</strong>
            <p>{dependency.installed ? 'Instalado' : dependency.reason || 'Falta instalar'}</p>
          </div>
          {#if !dependency.installed}<button
              class="secondary"
              disabled={!dependency.canInstall || installPending || !!installJob}
              on:click={() => reviewInstall(dependency.id)}>Revisar instalación</button
            >{/if}
        </div>
      {/each}
      {#if installPlan}<div class="terminal-install-review">
          <h3>Instalar {installPlan.name}</h3>
          <ul>
            {#each installPlan.summary as effect}<li>{effect}</li>{/each}
          </ul>
          {#each installPlan.warnings as warning}<p class="small">{warning}</p>{/each}
          <label class="terminal-install-ack"
            ><input type="checkbox" bind:checked={installAck} />Acepto estos cambios en {server.name}.</label
          >
          <button
            class="primary"
            disabled={!installAck || installPending || !!installJob}
            on:click={executeInstall}>Confirmar e instalar</button
          >
        </div>{/if}
      {#if installMessage}<p role="status">{installMessage}</p>{/if}
    </section>{/if}
  <div class="terminal-surface" class:terminal-hidden={!!requirements}>
    <div class="terminal-viewport" bind:this={surface}></div>
  </div>
  {#if controlsOpen && !requirements}
    <section id="terminal-controls" class="terminal-controls" aria-label="Controles de terminal">
      <div class="terminal-keys" role="group" aria-label="Teclas de la terminal">
        {#each keys as [label, data, description]}
          <button
            type="button"
            class="secondary"
            disabled={!connected}
            aria-label={description}
            on:pointerdown|preventDefault={() => {}}
            on:click={() => send(data)}>{label}</button
          >
        {/each}
      </div>
      <form class="terminal-composer" on:submit={submit}>
        <label class="sr-only" for="terminal-message"
          >{kind === 'codex' ? 'Instrucciones para Codex' : 'Comando del servidor'}</label
        >
        <textarea
          id="terminal-message"
          bind:value={draft}
          maxlength="4000"
          rows="1"
          placeholder={kind === 'codex'
            ? 'Escribe instrucciones para Codex…'
            : 'Escribe un comando…'}
          autocapitalize="off"
          autocomplete="off"
          autocorrect="off"
          spellcheck="false"
          on:keydown={(event) => {
            if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) submit(event);
          }}></textarea>
        <button class="primary" disabled={!connected || !draft.trim()}
          ><Send size={17} />Enviar</button
        >
      </form>
    </section>
  {/if}
</dialog>
