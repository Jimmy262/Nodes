<script>
  import { onMount, tick } from 'svelte';
  import { X, RefreshCw, Keyboard, Send, Folder, SquareTerminal, Copy } from '@lucide/svelte';
  import ProviderIcon from './ProviderIcon.svelte';
  import TerminalFolders from './TerminalFolders.svelte';
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
    installPending = false;
  let installPackage = '';
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
  let foldersOpen = false,
    directory = '',
    connectionVersion = 0;
  let localOpen = false,
    localOptions = null,
    localPending = false,
    localMessage = '';
  async function toggleLocal() {
    localOpen = !localOpen;
    if (!localOpen) return;
    localPending = true;
    localMessage = '';
    localOptions = null;
    try {
      localOptions = await api(
        `/servers/${server.id}/terminal/local?directory=${encodeURIComponent(directory)}`,
      );
    } catch (e) {
      localMessage = e.message;
    } finally {
      localPending = false;
      await tick();
      resize();
    }
  }
  async function openLocal() {
    localPending = true;
    localMessage = '';
    try {
      await api(`/servers/${server.id}/terminal/local`, {
        method: 'POST',
        body: JSON.stringify({ directory }),
      });
      localMessage = 'Se abrió PowerShell con una nueva conexión SSH.';
    } catch (e) {
      localMessage = e.message;
    } finally {
      localPending = false;
    }
  }
  async function copyLocal() {
    try {
      await navigator.clipboard.writeText(localOptions.command);
      localMessage = 'Comando copiado. Pégalo en tu terminal.';
    } catch {
      localMessage = 'Selecciona y copia el comando que aparece debajo.';
    }
  }
  $: providerName = kind === 'codex' ? 'Codex' : kind === 'claude' ? 'Claude' : 'Terminal';
  $: agent = kind === 'codex' || kind === 'claude';
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
    const data = agent ? `\x1b[200~${text}\x1b[201~\r` : text.replace(/\n/g, '\r') + '\r';
    if (send(data)) draft = '';
  }
  async function connect() {
    if (!active || connecting || connected || socket?.readyState === WebSocket.CONNECTING) return;
    connecting = true;
    const version = connectionVersion;
    clearTimeout(retryTimer);
    error = '';
    state = attempts ? 'Reconectando' : 'Conectando';
    try {
      const session = await api(`/servers/${server.id}/terminal`, {
        method: 'POST',
        body: JSON.stringify({ kind, directory }),
      });
      if (!active || version !== connectionVersion) return;
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
        if (!active || version !== connectionVersion) {
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
        if (!active || version !== connectionVersion) return;
        if (data instanceof ArrayBuffer) term.write(new Uint8Array(data));
      };
      ws.onclose = (event) => {
        if (version !== connectionVersion) return;
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
        if (version === connectionVersion) state = 'No se pudo conectar';
      };
    } catch (e) {
      if (active && version === connectionVersion) {
        state = 'No se pudo conectar';
        error = e.message;
      }
    } finally {
      if (version === connectionVersion) connecting = false;
    }
  }
  function openDirectory(path) {
    localOpen = false;
    localOptions = null;
    connectionVersion++;
    clearTimeout(retryTimer);
    clearTimeout(stableTimer);
    socket?.close(1000, 'Cambio de carpeta');
    socket = null;
    connected = false;
    connecting = false;
    attempts = 0;
    directory = path;
    foldersOpen = false;
    term?.reset();
    connect();
  }
  async function installDependency(dependency) {
    if (installPending || installJob) return;
    installPending = true;
    installPackage = dependency.id;
    installMessage = `Preparando la instalación de ${dependency.name}…`;
    error = '';
    try {
      if (installPlan) await api(`/operations/${installPlan.id}/plan`, { method: 'DELETE' });
      installPlan = await api(`/servers/${server.id}/terminal/install/plan`, {
        method: 'POST',
        body: JSON.stringify({ package: dependency.id }),
      });
      if (!active) return;
      if (
        installPlan.kind !== 'terminal-install' ||
        installPlan.name !== dependency.name ||
        installPlan.blockers?.length
      )
        throw new Error('La preparación cambió. Comprueba los requisitos y vuelve a intentarlo.');
      const result = await api(`/operations/${installPlan.id}/execute`, {
        method: 'POST',
        body: JSON.stringify({ name: installPlan.name, acknowledged: true }),
      });
      installJob = result.id;
      installMessage = 'Instalación en curso. Puedes seguirla también en Actividad.';
      installPlan = null;
      if (active) pollInstallation();
    } catch (e) {
      if (active) {
        error = e.message;
        installMessage = '';
      }
    } finally {
      if (installPlan) {
        await api(`/operations/${installPlan.id}/plan`, { method: 'DELETE' }).catch(() => {});
        installPlan = null;
      }
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
        installPackage = '';
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
    <h2 id="terminal-title" aria-label={`${providerName} · ${server.name}`} title={providerName}>
      <ProviderIcon {kind} size={19} /><span>{server.name}</span>
    </h2>
    {#if directory}<span class="terminal-start-directory" title={`Carpeta de inicio: ${directory}`}
        >{directory}</span
      >{/if}
    <span
      class="terminal-connection"
      class:is-connected={connected}
      role="status"
      aria-label={state}
      title={state}><i aria-hidden="true"></i><span>{state}</span></span
    >
    <div class="terminal-actions">
      <button
        type="button"
        class="icon-button"
        class:active={foldersOpen}
        disabled={!!requirements || connecting || installPending || !!installJob}
        aria-label={foldersOpen ? 'Ocultar carpetas' : 'Explorar carpetas'}
        aria-expanded={foldersOpen}
        title="Explorar carpetas del servidor"
        on:click={() => (foldersOpen = !foldersOpen)}><Folder size={18} /></button
      >
      <button
        type="button"
        class="icon-button"
        class:active={localOpen}
        disabled={localPending}
        aria-label="Abrir SSH en mi PC"
        title="Abrir SSH en mi PC"
        aria-expanded={localOpen}
        aria-controls="local-ssh-options"
        on:click={toggleLocal}><SquareTerminal size={18} /></button
      >
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
  {#if localOpen}<section
      id="local-ssh-options"
      class="terminal-local-options"
      aria-label="SSH en tu PC"
    >
      <strong>SSH en mi PC</strong>
      <p class="small muted">
        Abre una conexión SSH independiente para ejecutar comandos en el servidor desde tu terminal.
      </p>
      {#if localOptions}
        <div class="terminal-local-actions">
          {#if localOptions.available}<button
              class="secondary"
              disabled={localPending}
              on:click={openLocal}><SquareTerminal size={15} />Abrir PowerShell</button
            >{/if}
          <button class="secondary" disabled={localPending} on:click={copyLocal}
            ><Copy size={15} />Copiar comando SSH</button
          >
        </div>
        <textarea
          class="terminal-local-command"
          aria-label="Comando para conectar por SSH"
          readonly
          value={localOptions.command}
          rows="2"
          spellcheck="false"></textarea>
        {#if !localOptions.available}<p class="small muted">
            Copia el comando en tu PC. La apertura directa de PowerShell está disponible al usar
            Nodes en Windows por localhost.
          </p>{/if}
      {:else if localPending}<p role="status">Preparando conexión…</p>{/if}
      {#if localMessage}<p class="small" role="status">{localMessage}</p>{/if}
    </section>{/if}
  {#if error}<div class="terminal-feedback" role="alert">{error}</div>{/if}
  {#if requirements}<section class="terminal-requirements" aria-label="Requisitos del servidor">
      <h3>Preparar {providerName} en {server.name}</h3>
      <p>
        Instala lo que falta desde aquí. Se descargarán los paquetes oficiales en este servidor.
      </p>
      {#each requirements.dependencies.filter((dependency) => dependency.id === 'tmux' || (agent && (dependency.id === 'nodejs' || dependency.id === kind))) as dependency}
        <div class="terminal-dependency">
          <div>
            <strong>{dependency.name}</strong>
            <p>{dependency.installed ? 'Instalado' : dependency.reason || 'Falta instalar'}</p>
          </div>
          {#if !dependency.installed}<button
              class="primary"
              aria-label={`Instalar ${dependency.name} en ${server.name}`}
              disabled={!dependency.canInstall || installPending || !!installJob}
              on:click={() => installDependency(dependency)}
              >{installPackage === dependency.id && (installPending || installJob)
                ? installJob
                  ? 'Instalando…'
                  : 'Preparando…'
                : 'Instalar'}</button
            >{/if}
        </div>
      {/each}
      {#if agent}<p class="small muted">
          Se instala para el usuario SSH. La autenticación en {providerName} se realiza después, al abrirlo.
        </p>{/if}
      {#if installMessage}<p role="status">{installMessage}</p>{/if}
    </section>{/if}
  <div class="terminal-workspace" class:terminal-hidden={!!requirements}>
    {#if foldersOpen}<TerminalFolders
        {api}
        {server}
        initialPath={directory}
        onSelect={openDirectory}
      />{/if}
    <div class="terminal-surface"><div class="terminal-viewport" bind:this={surface}></div></div>
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
          >{agent ? `Instrucciones para ${providerName}` : 'Comando del servidor'}</label
        >
        <textarea
          id="terminal-message"
          bind:value={draft}
          maxlength="4000"
          rows="1"
          placeholder={agent
            ? `Escribe instrucciones para ${providerName}…`
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
