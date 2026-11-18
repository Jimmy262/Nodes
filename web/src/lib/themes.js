import { writable } from 'svelte/store';
import { storageRead, storageWrite } from '../format.js';

export const palettes = [
  {
    id: 'electric',
    name: 'Azul eléctrico',
    accent: '#245BFF',
    secondary: '#77C8FF',
    ink: '#101B38',
    paper: '#F3F7FF',
  },
  {
    id: 'solar',
    name: 'Naranja solar',
    accent: '#FF6828',
    secondary: '#FFC66E',
    ink: '#241A17',
    paper: '#FFF6EE',
  },
  {
    id: 'violet',
    name: 'Violeta pulso',
    accent: '#7638F4',
    secondary: '#C7A5FF',
    ink: '#201533',
    paper: '#F8F4FF',
  },
  {
    id: 'cyan',
    name: 'Cian digital',
    accent: '#04C5EC',
    secondary: '#73E8FF',
    ink: '#092432',
    paper: '#EFFBFF',
  },
  {
    id: 'coral',
    name: 'Coral activo',
    accent: '#FF4E63',
    secondary: '#FFB4A7',
    ink: '#301924',
    paper: '#FFF4F3',
  },
  {
    id: 'indigo',
    name: 'Índigo ultravioleta',
    accent: '#4B46FF',
    secondary: '#AA9CFF',
    ink: '#171833',
    paper: '#F5F5FF',
  },
  {
    id: 'volt',
    name: 'Amarillo voltio',
    accent: '#FFD234',
    secondary: '#FFF0A3',
    ink: '#242424',
    paper: '#FFFCF0',
  },
  {
    id: 'signal',
    name: 'Fucsia señal',
    accent: '#E83D9B',
    secondary: '#FFACD4',
    ink: '#30192B',
    paper: '#FFF4FA',
  },
  {
    id: 'duo',
    name: 'Azul + mandarina',
    accent: '#2862EF',
    secondary: '#FF963B',
    ink: '#132341',
    paper: '#F4F7FC',
  },
];

const key = 'nodes.appearance.v1';
export const defaultPalette = palettes[2];
export function resolvePalette(id) {
  return palettes.find((palette) => palette.id === id) || defaultPalette;
}
export function mix(a, b, weight) {
  const parts = [1, 3, 5].map((offset) =>
    Math.round(
      parseInt(a.slice(offset, offset + 2), 16) * (1 - weight) +
        parseInt(b.slice(offset, offset + 2), 16) * weight,
    )
      .toString(16)
      .padStart(2, '0'),
  );
  return `#${parts.join('')}`;
}
export function contrast(a, b) {
  const luminance = (hex) =>
    [1, 3, 5]
      .map((offset) => {
        const channel = parseInt(hex.slice(offset, offset + 2), 16) / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      })
      .reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0);
  const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (light + 0.05) / (dark + 0.05);
}
export function themeTokens(palette) {
  const { accent, secondary, ink, paper } = palette;
  let inkWeight = 0.32;
  let accentInk = mix(accent, ink, inkWeight);
  while (contrast(accentInk, paper) < 4.5 && inkWeight < 1) {
    inkWeight = Math.min(1, inkWeight + 0.05);
    accentInk = mix(accent, ink, inkWeight);
  }
  const actionText =
    contrast(accent, '#FFFFFF') >= 4.5 ? '#FFFFFF' : contrast(accent, ink) >= 4.5 ? ink : '#111111';
  // Lighten actions with dark labels; darken actions with white labels.
  const hover = mix(accent, actionText === '#FFFFFF' ? ink : '#FFFFFF', 0.12);
  return {
    accent,
    'accent-ink': accentInk,
    'accent-hover': hover,
    'on-accent': actionText,
    'brand-secondary': secondary,
    ink,
    paper,
    white: '#FFFFFF',
    muted: mix(ink, '#FFFFFF', 0.35),
    line: mix(ink, '#FFFFFF', 0.87),
    selected: mix(accent, '#FFFFFF', 0.92),
    'surface-soft': mix(accent, '#FFFFFF', 0.97),
    focus: accentInk,
    'focus-ring': mix(accent, '#FFFFFF', 0.8),
    'sidebar-bg': ink,
    'sidebar-text': '#FFFFFF',
    'sidebar-muted': mix(ink, '#FFFFFF', 0.76),
    'chart-1': accentInk,
    'chart-2': '#087E99',
    'chart-3': '#B65414',
    'chart-4': '#C1356A',
    'chart-5': '#5366B7',
    'chart-6': '#71602B',
    'terminal-bg': mix(ink, '#000000', 0.35),
    'terminal-text': '#F4F2F8',
    'terminal-cursor': secondary,
  };
}
export const theme = writable(resolvePalette(storageRead(key, 'violet')));
export function selectTheme(id) {
  theme.set(resolvePalette(id));
}
export function initializeTheme() {
  return theme.subscribe((palette) => {
    const root = document.documentElement;
    root.dataset.palette = palette.id;
    for (const [name, value] of Object.entries(themeTokens(palette))) {
      root.style.setProperty(`--${name}`, value);
    }
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', palette.ink);
    storageWrite(key, palette.id);
  });
}
