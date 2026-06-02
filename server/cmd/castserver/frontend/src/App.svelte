<script>
  import Admin from './Admin.svelte';
  import Viewer from './Viewer.svelte';

  let isViewer = window.location.pathname === '/web';
</script>

<svelte:head>
  <title>{isViewer ? 'Viewer - CastServer' : 'Admin - CastServer'}</title>
</svelte:head>

{#if isViewer}
  <Viewer />
{:else}
  <Admin />
{/if}
