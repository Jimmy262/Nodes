import { mount } from 'svelte';
import App from './App.svelte';
import './style.css';
import './branding.css';
import { initializeTheme } from './lib/themes.js';
initializeTheme();
mount(App, { target: document.getElementById('app') });
