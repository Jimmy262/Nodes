<script>
  import { onMount } from 'svelte';
  export let servers = [];
  export let histories = {};
  export let now = Date.now();
  let metric = 'cpu';
  let panel,
    width = 980;
  onMount(() => {
    const observer = new ResizeObserver(
      ([entry]) => (width = Math.max(260, entry.contentRect.width)),
    );
    observer.observe(panel);
    return () => observer.disconnect();
  });
  const colors = ['#326A48', '#7684BA', '#C48A42', '#348B98', '#AC6880', '#797C42'];
  $: start = now - 60 * 60 * 1000;
  $: count = servers.reduce(
    (total, server) =>
      total + (histories[server.id] || []).filter((p) => Date.parse(p.at) >= start).length,
    0,
  );
  function paths(points, field, from, chartWidth) {
    const segments = [];
    let last = 0;
    let segment = '';
    for (const point of points || []) {
      const at = Date.parse(point.at);
      if (at < from || at > from + 3600000 || !Number.isFinite(point[field])) continue;
      const x = 40 + ((at - from) / 3600000) * (chartWidth - 52);
      const y = 200 - (point[field] / 100) * 170;
      if (last && at - last > 45000) {
        segments.push(segment);
        segment = '';
      }
      segment += `${segment ? ' L' : 'M'}${x.toFixed(2)},${y.toFixed(2)}`;
      last = at;
    }
    if (segment) segments.push(segment);
    return segments;
  }
  function lastPoint(points, field, from, chartWidth) {
    const point = points?.at(-1);
    if (!point || Date.parse(point.at) < from || !Number.isFinite(point[field])) return null;
    return {
      x: 40 + ((Date.parse(point.at) - from) / 3600000) * (chartWidth - 52),
      y: 200 - (point[field] / 100) * 170,
    };
  }
</script>

<section class="history-panel" bind:this={panel}>
  <div class="section-heading">
    <div>
      <h2>Consumo por servidor</h2>
      <span class="muted">Última hora · porcentaje de su capacidad</span>
    </div>
    <div class="segmented" aria-label="Métrica de la gráfica">
      <button
        class:active={metric === 'cpu'}
        on:click={() => (metric = 'cpu')}
        aria-pressed={metric === 'cpu'}>CPU</button
      ><button
        class:active={metric === 'memory'}
        on:click={() => (metric = 'memory')}
        aria-pressed={metric === 'memory'}>RAM</button
      >
    </div>
  </div>
  {#if count}
    <svg
      class="history-chart"
      viewBox={`0 0 ${width} 240`}
      role="img"
      aria-label={`${metric === 'cpu' ? 'CPU' : 'RAM'} de los servidores seleccionados durante la última hora. Los valores actuales están en la tabla.`}
    >
      {#each [0, 25, 50, 75, 100] as level}<line
          x1="40"
          x2={width - 12}
          y1={200 - level * 1.7}
          y2={200 - level * 1.7}
          class="grid-line"
        /><text x="30" y={204 - level * 1.7} text-anchor="end">{level}%</text>{/each}
      {#each [0, 15, 30, 45, 60] as minutes}<text
          x={40 + (minutes / 60) * (width - 52)}
          y="228"
          text-anchor={minutes === 0 ? 'start' : minutes === 60 ? 'end' : 'middle'}
          >{minutes === 60 ? 'Ahora' : `−${60 - minutes} min`}</text
        >{/each}
      {#each servers as server, i}
        {#each paths(histories[server.id], metric, start, width) as path}<path
            d={path}
            fill="none"
            stroke={colors[i % colors.length]}
            stroke-width="2"
            vector-effect="non-scaling-stroke"
          />{/each}
        {@const point = lastPoint(histories[server.id], metric, start, width)}
        {#if point}<circle cx={point.x} cy={point.y} r="3" fill={colors[i % colors.length]} />{/if}
      {/each}
    </svg>
  {:else}<div class="chart-empty">
      El histórico aparecerá con las primeras lecturas automáticas.
    </div>{/if}
  <div class="chart-legend">
    {#each servers as server, i}<span
        ><i style={`background:${colors[i % colors.length]}`}></i>{server.name}</span
      >{/each}
  </div>
</section>
