import './app.css';
import App from './App.svelte';

const theme = localStorage.getItem('theme') || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
if (theme === 'dark') {
  document.documentElement.classList.add('dark');
} else {
  document.documentElement.classList.remove('dark');
}

const app = new App({
  target: document.getElementById('app') || document.body,
});

export default app;
