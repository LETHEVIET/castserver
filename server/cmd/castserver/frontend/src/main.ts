import './app.css';
import App from './App.svelte';

document.documentElement.classList.add('dark');

const app = new App({
  target: document.getElementById('app') || document.body,
});

export default app;
