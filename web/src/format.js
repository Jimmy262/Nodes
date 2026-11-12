export function bytes(value) {
  if (value == null || !Number.isFinite(value)) return '—';
  if (value < 1024) return `${Math.round(value)} B`;
  const unit = Math.min(4, Math.floor(Math.log(value) / Math.log(1024)));
  return `${(value / 1024 ** unit).toLocaleString('es-PE', { maximumFractionDigits: unit > 2 ? 1 : 0 })} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][unit]}`;
}
export function percent(value) {
  if (value > 0 && value < 0.1) return '<0.1%';
  return value == null ? '—' : `${value.toLocaleString('es-PE', { maximumFractionDigits: 1 })}%`;
}
export function age(at, now = Date.now()) {
  if (!at) return 'Esperando datos';
  const seconds = Math.max(0, Math.round((now - new Date(at).getTime()) / 1000));
  if (seconds < 5) return 'Hace un momento';
  if (seconds < 60) return `Hace ${seconds} s`;
  if (seconds < 3600) return `Hace ${Math.floor(seconds / 60)} min`;
  return `Hace ${Math.floor(seconds / 3600)} h`;
}
export function stale(at, now = Date.now(), threshold = 75000) {
  return !at || now - new Date(at).getTime() > threshold;
}
export function hostname(link) {
  try {
    return new URL(link).host;
  } catch {
    return link;
  }
}
export function statusLabel(status) {
  return (
    { running: 'En ejecución', partial: 'Revisar', stopped: 'Detenida' }[status] || 'Pendiente'
  );
}
export function storageRead(key, fallback) {
  try {
    return JSON.parse(localStorage.getItem(key)) ?? fallback;
  } catch {
    return fallback;
  }
}
export function storageWrite(key, value) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* Private browser mode. */
  }
}
export function modal(node) {
  const previous = document.activeElement;
  node.showModal();
  return {
    destroy() {
      node.close();
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    },
  };
}
